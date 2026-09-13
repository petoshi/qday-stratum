package bridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/petoshi/qday-stratum/internal/nodeapi"
)

func syntheticTemplate(t *testing.T, target [32]byte) nodeapi.Template {
	t.Helper()
	parent := [32]byte{0: 0x51, 31: 0x59}
	transaction := []byte{0x71, 0x64, 0x61, 0x79}
	left := [32]byte{0: 0xaa, 31: 0xbb}
	commitment := merklePair(left, transactionLeaf(transaction))
	timestamp := int64(1_789_234_567)
	header := make([]byte, 80)
	copy(header[:32], parent[:])
	binary.LittleEndian.PutUint64(header[40:48], uint64(timestamp))
	copy(header[48:], commitment[:])
	block := make([]byte, 96)
	copy(block[:48], header[:48])
	return nodeapi.Template{
		Header:            hex.EncodeToString(header),
		Commitment:        hex.EncodeToString(commitment[:]),
		Transactions:      []nodeapi.TemplateTransaction{{Data: hex.EncodeToString(transaction), TxID: "marker"}},
		PreviousBlockHash: hex.EncodeToString(parent[:]),
		LongPollID:        "template-1",
		Target:            hex.EncodeToString(target[:]),
		Height:            42,
		Timestamp:         timestamp,
		Stratum: nodeapi.StratumTemplate{
			Block:        hex.EncodeToString(block),
			MerkleBranch: []string{hex.EncodeToString(left[:])},
		},
	}
}

func TestJobReconstructsSolvedBlock(t *testing.T) {
	var target [32]byte
	for i := range target {
		target[i] = 0xff
	}
	template := syntheticTemplate(t, target)
	j, err := newJob("a", template, time.Unix(template.Timestamp, 0))
	if err != nil {
		t.Fatal(err)
	}
	nonce := "8877665544332211"
	block, hash, err := j.solve("", "", littleEndianHex(j.timestamp), nonce)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(block[32:40], mustDecodeHex(t, nonce)) {
		t.Fatal("solved nonce was not inserted into the complete block")
	}
	header := make([]byte, 80)
	copy(header[:32], j.parent[:])
	copy(header[32:40], mustDecodeHex(t, nonce))
	copy(header[40:48], mustDecodeHex(t, littleEndianHex(j.timestamp)))
	root := transactionLeaf(j.coinbase1)
	for _, left := range j.merkleBranch {
		root = merklePair(left, root)
	}
	copy(header[48:], root[:])
	if hash != transactionHash(header) {
		t.Fatal("submitted block hash does not match the Sia Stratum work header")
	}
}

func TestJobRejectsModifiedWork(t *testing.T) {
	var target [32]byte
	for i := range target {
		target[i] = 0xff
	}
	template := syntheticTemplate(t, target)
	j, err := newJob("a", template, time.Unix(template.Timestamp, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := j.solve("", "01", littleEndianHex(j.timestamp), stringsOfZero(16)); err == nil {
		t.Fatal("accepted a nonempty extranonce")
	}
	if _, _, err := j.solve("", "", "00", stringsOfZero(16)); err == nil {
		t.Fatal("accepted an invalid timestamp")
	}
	bad := template
	bad.Commitment = stringsOfZero(64)
	if _, err := newJob("b", bad, time.Unix(template.Timestamp, 0)); err == nil {
		t.Fatal("accepted a Merkle branch that does not match the commitment")
	}
}

func TestDifficultyOne(t *testing.T) {
	var target [32]byte
	difficultyOneTarget.FillBytes(target[:])
	difficulty, err := targetDifficulty(target)
	if err != nil {
		t.Fatal(err)
	} else if math.Abs(difficulty-1) > 1e-12 {
		t.Fatalf("difficulty = %v, want 1", difficulty)
	}
	if got := compactTarget(target); got != "1d00ffff" {
		t.Fatalf("compact target = %s, want 1d00ffff", got)
	}
}

func TestAdvertisedDifficultyNeverEasesTarget(t *testing.T) {
	for _, encoded := range []string{
		"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		"00000fffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		"000000000000123456789abcdef0000000000000000000000000000000000000",
		"0000000000000000000000000000000000000000000000000000000000000001",
	} {
		targetValue := mustBigInt(encoded)
		var target [32]byte
		targetValue.FillBytes(target[:])
		difficulty, err := targetDifficulty(target)
		if err != nil {
			t.Fatal(err)
		}
		// Match gominer's difficulty-to-target calculation.
		workerTargetFloat := new(big.Float).SetInt(difficultyOneTarget)
		workerTargetFloat.Quo(workerTargetFloat, big.NewFloat(difficulty))
		workerTarget, _ := workerTargetFloat.Int(nil)
		if workerTarget.Cmp(targetValue) > 0 {
			t.Fatalf("advertised difficulty %v eases target %s to %x", difficulty, encoded, workerTarget)
		}
	}
}

func transactionHash(data []byte) [32]byte {
	// The mining hash and transaction/Merkle hashes are all BLAKE2b-256; this
	// helper keeps the assertion readable.
	return blakeSum(data)
}

func mustDecodeHex(t *testing.T, encoded string) []byte {
	t.Helper()
	b, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func stringsOfZero(n int) string { return string(bytes.Repeat([]byte{'0'}, n)) }
