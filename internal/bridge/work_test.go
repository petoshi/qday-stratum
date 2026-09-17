package bridge

import (
	"bytes"
	"encoding/hex"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/petoshi/qday-stratum/internal/nodeapi"
	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
)

func syntheticTemplate(t *testing.T, target [32]byte) nodeapi.Template {
	t.Helper()
	parent := [32]byte{0: 0x51, 31: 0x59}
	const workNonce = uint64(0x0807060504030201)
	txn := types.V2Transaction{ArbitraryData: (consensus.QdayEnvelope{Kind: consensus.QdayMiningWork, Nonce: workNonce}).Encode()}
	var transaction bytes.Buffer
	encoder := types.NewEncoder(&transaction)
	txn.EncodeTo(encoder)
	if err := encoder.Flush(); err != nil {
		t.Fatal(err)
	}
	left := [32]byte{0: 0xaa, 31: 0xbb}
	commitment := merklePair(left, transactionLeaf(transaction.Bytes()))
	timestamp := int64(1_789_234_567)
	block := types.Block{ParentID: types.BlockID(parent), Timestamp: time.Unix(timestamp, 0), V2: &types.V2BlockData{
		Height: uint64(qdayV1ActivationHeight), Commitment: types.Hash256(commitment), Transactions: []types.V2Transaction{txn},
	}}
	var header bytes.Buffer
	headerEncoder := types.NewEncoder(&header)
	block.Header().EncodeTo(headerEncoder)
	if err := headerEncoder.Flush(); err != nil {
		t.Fatal(err)
	}
	var encodedBlock bytes.Buffer
	blockEncoder := types.NewEncoder(&encodedBlock)
	types.V2Block(block).EncodeTo(blockEncoder)
	if err := blockEncoder.Flush(); err != nil {
		t.Fatal(err)
	}
	return nodeapi.Template{
		Header:     hex.EncodeToString(header.Bytes()),
		Commitment: hex.EncodeToString(commitment[:]),
		Transactions: []nodeapi.TemplateTransaction{
			{Data: "00", TxID: "coinbase"},
			{Data: hex.EncodeToString(transaction.Bytes()), TxID: "work"},
		},
		PreviousBlockHash:   hex.EncodeToString(parent[:]),
		LongPollID:          "template-1",
		Target:              hex.EncodeToString(target[:]),
		Height:              qdayV1ActivationHeight,
		Timestamp:           timestamp,
		Bits:                "207fffff",
		WorkNonce:           workNonce,
		MempoolTransactions: 0,
		Stratum: nodeapi.StratumTemplate{
			Block:           hex.EncodeToString(encodedBlock.Bytes()),
			Coinbase1:       hex.EncodeToString(transaction.Bytes()[:23]),
			Coinbase2:       hex.EncodeToString(transaction.Bytes()[31:]),
			ExtraNonce1Size: 4,
			ExtraNonce2Size: 4,
			MerkleBranch:    []string{hex.EncodeToString(left[:])},
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
	block, hash, err := j.solve("01020304", "05060708", littleEndianHex(j.timestamp), nonce)
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
	coinbase := append([]byte(nil), j.coinbase1...)
	coinbase = append(coinbase, 1, 2, 3, 4, 5, 6, 7, 8)
	coinbase = append(coinbase, j.coinbase2...)
	root := transactionLeaf(coinbase)
	for _, left := range j.merkleBranch {
		root = merklePair(left, root)
	}
	copy(header[48:], root[:])
	if hash != transactionHash(header) {
		t.Fatal("submitted block hash does not match the Sia Stratum work header")
	}
}

func TestBridgeWaitsForV1Activation(t *testing.T) {
	var target [32]byte
	for i := range target {
		target[i] = 0xff
	}
	template := syntheticTemplate(t, target)
	template.Height = qdayV1ActivationHeight - 1
	if _, err := newWorkTemplate(template); err == nil {
		t.Fatal("accepted mining work before block 9,100")
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
	if _, _, err := j.solve("", "01020304", littleEndianHex(j.timestamp), stringsOfZero(16)); err == nil {
		t.Fatal("accepted a short extranonce1")
	}
	if _, _, err := j.solve("01020304", "05060708", "00", stringsOfZero(16)); err == nil {
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

func TestObeliskSC1HeaderVector(t *testing.T) {
	// This vector was generated by Obelisk's stock Sia siastratum_gen_work
	// implementation. It proves that the compact 23+4+4+2 transaction split,
	// right-edge Merkle fold and 80-byte header layout agree byte for byte.
	coinbase1 := mustDecodeHex(t, "0200010000000000001000000000000000514441590204")
	extraNonce1 := mustDecodeHex(t, "01020304")
	extraNonce2 := mustDecodeHex(t, "05060708")
	coinbase2 := mustDecodeHex(t, "0000")
	transaction := append(append(append(append([]byte{}, coinbase1...), extraNonce1...), extraNonce2...), coinbase2...)
	if got := hex.EncodeToString(transaction); got != "020001000000000000100000000000000051444159020401020304050607080000" {
		t.Fatal("compact transaction changed: ", got)
	}
	left := [32]byte{0: 0xaa, 31: 0xbb}
	root := merklePair(left, transactionLeaf(transaction))
	parent := [32]byte{0: 0x51, 31: 0x59}
	header := make([]byte, 80)
	copy(header[:32], parent[:])
	copy(header[40:48], mustDecodeHex(t, "8877665544332211"))
	copy(header[48:], root[:])
	const expected = "510000000000000000000000000000000000000000000000000000000000005900000000000000008877665544332211309f36bacf3562bb7950f00159822c3489ebb89d26544d42664ce21144376dc5"
	if got := hex.EncodeToString(header); got != expected {
		t.Fatalf("Obelisk header mismatch\n got %s\nwant %s", got, expected)
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
