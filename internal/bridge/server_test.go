package bridge

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/petoshi/qday-stratum/internal/nodeapi"
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
		"params": []string{"rig.one", jobID, "", ntime, "0000000000000000"},
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
