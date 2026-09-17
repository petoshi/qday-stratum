package bridge

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/petoshi/qday-stratum/internal/nodeapi"
	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
)

type fakeNode struct {
	template nodeapi.Template
	ready    chan struct{}
	once     sync.Once
	submits  chan string
}

func (n *fakeNode) GetBlockTemplate(ctx context.Context, longPollID string) (nodeapi.Template, error) {
	if longPollID == "" {
		n.once.Do(func() { close(n.ready) })
		return n.template, nil
	}
	<-ctx.Done()
	return nodeapi.Template{}, ctx.Err()
}

func (n *fakeNode) SubmitBlock(_ context.Context, block string) (string, error) {
	n.submits <- block
	return stringsOfByte('a', 64), nil
}

type wireMessage struct {
	ID     any               `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
	Result json.RawMessage   `json:"result"`
	Error  json.RawMessage   `json:"error"`
}

func TestSiaStratumRoundTrip(t *testing.T) {
	var target [32]byte
	for i := range target {
		target[i] = 0xff
	}
	node := &fakeNode{
		template: syntheticTemplate(t, target),
		ready:    make(chan struct{}),
		submits:  make(chan string, 1),
	}
	server, err := NewServer(node, Config{
		JobInterval: time.Hour,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	<-node.ready
	deadline := time.Now().Add(time.Second)
	for server.currentJob() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writeRPC(t, conn, map[string]any{"id": 1, "method": "mining.subscribe", "params": []string{"gominer"}})
	subscribe := readWire(t, reader)
	if string(subscribe.Result) == "null" || string(subscribe.Error) != "null" {
		t.Fatalf("invalid subscribe response: %+v", subscribe)
	}
	var subscription []json.RawMessage
	if err := json.Unmarshal(subscribe.Result, &subscription); err != nil || len(subscription) != 3 {
		t.Fatalf("invalid subscription result: %s, %v", subscribe.Result, err)
	}
	var extraNonce1 string
	var extraNonce2Size int
	if json.Unmarshal(subscription[1], &extraNonce1) != nil || json.Unmarshal(subscription[2], &extraNonce2Size) != nil || len(extraNonce1) != 8 || extraNonce2Size != 4 {
		t.Fatalf("invalid extranonce assignment: %s", subscribe.Result)
	}
	writeRPC(t, conn, map[string]any{"id": 2, "method": "mining.authorize", "params": []string{"rig.one", "x"}})
	authorize := readWire(t, reader)
	if string(authorize.Result) != "true" || string(authorize.Error) != "null" {
		t.Fatalf("invalid authorize response: %+v", authorize)
	}
	difficulty := readWire(t, reader)
	if difficulty.Method != "mining.set_difficulty" {
		t.Fatalf("expected difficulty notification, got %q", difficulty.Method)
	}
	notify := readWire(t, reader)
	if notify.Method != "mining.notify" || len(notify.Params) != 9 {
		t.Fatalf("invalid work notification: %+v", notify)
	}
	var jobID, ntime string
	if err := json.Unmarshal(notify.Params[0], &jobID); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(notify.Params[7], &ntime); err != nil {
		t.Fatal(err)
	}
	writeRPC(t, conn, map[string]any{
		"id":     3,
		"method": "mining.submit",
		"params": []string{"rig.one", jobID, "00000000", ntime, "0000000000000000"},
	})
	submit := readWire(t, reader)
	if string(submit.Result) != "true" || string(submit.Error) != "null" {
		t.Fatalf("invalid submit response: %+v", submit)
	}
	select {
	case encoded := <-node.submits:
		block, err := hex.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if len(block) < 48 || !allZero(block[32:40]) {
			t.Fatal("bridge submitted a block without the solved nonce")
		}
		decoder := types.NewBufDecoder(block)
		var encodedBlock types.V2Block
		encodedBlock.DecodeFrom(decoder)
		if decoder.Err() != nil {
			t.Fatal(decoder.Err())
		}
		decoded := encodedBlock.Cast()
		envelope, err := consensus.ParseQdayEnvelope(decoded.V2.Transactions[len(decoded.V2.Transactions)-1].ArbitraryData)
		if err != nil {
			t.Fatal(err)
		}
		assigned, _ := hex.DecodeString(extraNonce1)
		var wantNonce [8]byte
		copy(wantNonce[:4], assigned)
		if envelope.Nonce != binary.LittleEndian.Uint64(wantNonce[:]) {
			t.Fatalf("submitted work nonce %x does not contain assigned extranonce %x", envelope.Nonce, assigned)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not submit solved block to QDAY")
	}
}

func writeRPC(t *testing.T, conn net.Conn, message any) {
	t.Helper()
	if err := json.NewEncoder(conn).Encode(message); err != nil {
		t.Fatal(err)
	}
}

func readWire(t *testing.T, reader *bufio.Reader) wireMessage {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var message wireMessage
	if err := json.Unmarshal(line, &message); err != nil {
		t.Fatalf("invalid JSON %q: %v", line, err)
	}
	return message
}

func stringsOfByte(b byte, n int) string { return string(makeBytes(b, n)) }

func makeBytes(b byte, n int) []byte {
	value := make([]byte, n)
	for i := range value {
		value[i] = b
	}
	return value
}

func allZero(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return false
		}
	}
	return true
}
