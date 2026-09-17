package bridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/petoshi/qday-stratum/internal/nodeapi"
	"go.sia.tech/core/types"
	"golang.org/x/crypto/blake2b"
)

var (
	difficultyOneTarget = mustBigInt("00000000ffff0000000000000000000000000000000000000000000000000000")
	twoTo256            = new(big.Int).Lsh(big.NewInt(1), 256)
)

const qdayV1ActivationHeight uint32 = 9100

func mustBigInt(encoded string) *big.Int {
	n, ok := new(big.Int).SetString(encoded, 16)
	if !ok {
		panic("invalid integer constant")
	}
	return n
}

type workTemplate struct {
	height       uint32
	parent       [32]byte
	target       [32]byte
	timestamp    int64
	coinbase1    []byte
	coinbase2    []byte
	extra1Size   uint8
	extra2Size   uint8
	merkleBranch [][32]byte
	block        []byte
	difficulty   float64
	bits         string
	longPollID   string
	transactions int
	workNonce    uint64
}

type job struct {
	*workTemplate
	id        string
	sequence  uint64
	timestamp uint64
}

func decode32(name, encoded string) ([32]byte, error) {
	var value [32]byte
	b, err := hex.DecodeString(encoded)
	if err != nil || len(b) != len(value) {
		return value, fmt.Errorf("%s must be 32 bytes of hexadecimal data", name)
	}
	copy(value[:], b)
	return value, nil
}

func mempoolTransactionCount(template nodeapi.Template) (int, error) {
	const protocolTransactions = 2
	if len(template.Transactions) < protocolTransactions {
		return 0, errors.New("QDAY template is missing a protocol marker")
	}
	return len(template.Transactions) - protocolTransactions, nil
}

func newWorkTemplate(template nodeapi.Template) (*workTemplate, error) {
	if template.LongPollID == "" || template.Height == 0 || template.Timestamp <= 0 {
		return nil, errors.New("QDAY node returned an incomplete mining template")
	}
	if template.Stratum.Block == "" {
		return nil, errors.New("QDAY node has no Sia Stratum template data; update QDAY")
	}
	if len(template.Transactions) == 0 {
		return nil, errors.New("QDAY template has no miner marker transaction")
	}
	parent, err := decode32("previous block hash", template.PreviousBlockHash)
	if err != nil {
		return nil, err
	}
	target, err := decode32("target", template.Target)
	if err != nil {
		return nil, err
	}
	commitment, err := decode32("commitment", template.Commitment)
	if err != nil {
		return nil, err
	}
	coinbase1, err := hex.DecodeString(template.Stratum.Coinbase1)
	if err != nil || len(coinbase1) == 0 {
		return nil, errors.New("QDAY Stratum coinbase1 is invalid")
	}
	coinbase2, err := hex.DecodeString(template.Stratum.Coinbase2)
	if err != nil {
		return nil, errors.New("QDAY Stratum coinbase2 is invalid")
	}
	if template.Height < qdayV1ActivationHeight || template.Stratum.ExtraNonce1Size != 4 || template.Stratum.ExtraNonce2Size != 4 {
		return nil, errors.New("QDAY v1 mining starts at block 9,100 and requires a 4+4 byte extranonce split")
	}
	mempoolTransactions, err := mempoolTransactionCount(template)
	if err != nil {
		return nil, err
	}
	initialCoinbase := append([]byte(nil), coinbase1...)
	var nonce [8]byte
	binary.LittleEndian.PutUint64(nonce[:], template.WorkNonce)
	initialCoinbase = append(initialCoinbase, nonce[:]...)
	initialCoinbase = append(initialCoinbase, coinbase2...)
	if !strings.EqualFold(hex.EncodeToString(initialCoinbase), template.Transactions[len(template.Transactions)-1].Data) {
		return nil, errors.New("QDAY Stratum coinbase parts do not reconstruct the template transaction")
	}
	branches := make([][32]byte, len(template.Stratum.MerkleBranch))
	if len(branches) > 64 {
		return nil, errors.New("QDAY node returned too many Merkle branches")
	}
	for i, encoded := range template.Stratum.MerkleBranch {
		branches[i], err = decode32("Merkle branch", encoded)
		if err != nil {
			return nil, err
		}
	}
	root := transactionLeaf(initialCoinbase)
	for _, left := range branches {
		root = merklePair(left, root)
	}
	if root != commitment {
		return nil, errors.New("QDAY Stratum branch does not match its commitment")
	}
	block, err := hex.DecodeString(template.Stratum.Block)
	if err != nil || len(block) < 80 {
		return nil, errors.New("QDAY node returned an invalid complete block")
	}
	header, err := hex.DecodeString(template.Header)
	if err != nil || len(header) != 80 {
		return nil, errors.New("QDAY node returned an invalid 80-byte header")
	}
	if !bytes.Equal(header[:32], parent[:]) || !bytes.Equal(header[48:], commitment[:]) || binary.LittleEndian.Uint64(header[40:48]) != uint64(template.Timestamp) {
		return nil, errors.New("QDAY template fields do not describe the same header")
	}
	decoder := types.NewBufDecoder(block)
	var encodedBlock types.V2Block
	encodedBlock.DecodeFrom(decoder)
	if decoder.Err() != nil {
		return nil, errors.New("QDAY node returned an invalid complete block")
	}
	var canonical bytes.Buffer
	encoder := types.NewEncoder(&canonical)
	encodedBlock.EncodeTo(encoder)
	if encoder.Flush() != nil || !bytes.Equal(canonical.Bytes(), block) {
		return nil, errors.New("QDAY node returned a non-canonical complete block")
	}
	decodedBlock := encodedBlock.Cast()
	blockHeader := decodedBlock.Header()
	if blockHeader.ParentID != types.BlockID(parent) || blockHeader.Nonce != binary.LittleEndian.Uint64(header[32:40]) || blockHeader.Timestamp.Unix() != template.Timestamp || blockHeader.Commitment != types.Hash256(commitment) {
		return nil, errors.New("QDAY complete block does not match its work header")
	}
	difficulty, err := targetDifficulty(target)
	if err != nil {
		return nil, err
	}
	return &workTemplate{
		height:       template.Height,
		parent:       parent,
		target:       target,
		timestamp:    template.Timestamp,
		coinbase1:    coinbase1,
		coinbase2:    coinbase2,
		extra1Size:   template.Stratum.ExtraNonce1Size,
		extra2Size:   template.Stratum.ExtraNonce2Size,
		merkleBranch: branches,
		block:        block,
		difficulty:   difficulty,
		bits:         template.Bits,
		longPollID:   template.LongPollID,
		transactions: mempoolTransactions,
		workNonce:    template.WorkNonce,
	}, nil
}

func (t *workTemplate) newJob(id string, sequence uint64, timestamp time.Time) *job {
	when := uint64(timestamp.Unix())
	if when < uint64(t.timestamp) {
		when = uint64(t.timestamp)
	}
	return &job{workTemplate: t, id: id, sequence: sequence, timestamp: when}
}

func newJob(id string, template nodeapi.Template, timestamp time.Time) (*job, error) {
	work, err := newWorkTemplate(template)
	if err != nil {
		return nil, err
	}
	return work.newJob(id, 1, timestamp), nil
}

func transactionLeaf(transaction []byte) [32]byte {
	data := make([]byte, 1+len(transaction))
	copy(data[1:], transaction)
	return blake2b.Sum256(data)
}

func merklePair(left, right [32]byte) [32]byte {
	var data [65]byte
	data[0] = 1
	copy(data[1:33], left[:])
	copy(data[33:], right[:])
	return blake2b.Sum256(data[:])
}

func targetDifficulty(target [32]byte) (float64, error) {
	t := new(big.Int).SetBytes(target[:])
	if t.Sign() == 0 {
		return 0, errors.New("target is zero")
	}
	ratio := new(big.Float).SetPrec(256).Quo(new(big.Float).SetInt(difficultyOneTarget), new(big.Float).SetInt(t))
	difficulty, accuracy := ratio.Float64()
	if accuracy == big.Below {
		difficulty = math.Nextafter(difficulty, math.Inf(1))
	}
	if difficulty <= 0 || math.IsInf(difficulty, 0) || math.IsNaN(difficulty) {
		return 0, errors.New("target difficulty cannot be represented")
	}
	return difficulty, nil
}

func compactTarget(target [32]byte) string {
	n := new(big.Int).SetBytes(target[:])
	if n.Sign() == 0 {
		return "00000000"
	}
	exponent := uint(len(n.Bytes()))
	var mantissa uint32
	if exponent <= 3 {
		mantissa = uint32(n.Uint64()) << (8 * (3 - exponent))
	} else {
		mantissa = uint32(new(big.Int).Rsh(new(big.Int).Set(n), 8*(exponent-3)).Uint64())
	}
	if mantissa&0x00800000 != 0 {
		mantissa >>= 8
		exponent++
	}
	return fmt.Sprintf("%08x", uint32(exponent<<24)|mantissa)
}

func targetForDifficulty(difficulty float64) ([32]byte, error) {
	var target [32]byte
	if difficulty <= 0 || math.IsInf(difficulty, 0) || math.IsNaN(difficulty) {
		return target, errors.New("difficulty must be finite and positive")
	}
	d := new(big.Float).SetPrec(256).SetFloat64(difficulty)
	value, _ := new(big.Float).SetPrec(256).Quo(new(big.Float).SetInt(difficultyOneTarget), d).Int(nil)
	if value.Sign() <= 0 {
		value.SetInt64(1)
	}
	max := new(big.Int).Sub(twoTo256, big.NewInt(1))
	if value.Cmp(max) > 0 {
		value.Set(max)
	}
	b := value.Bytes()
	copy(target[len(target)-len(b):], b)
	return target, nil
}

// TargetWork returns the expected hashes represented by a target.
func TargetWork(target [32]byte) *big.Int {
	denominator := new(big.Int).Add(new(big.Int).SetBytes(target[:]), big.NewInt(1))
	return new(big.Int).Quo(new(big.Int).Set(twoTo256), denominator)
}

func littleEndianHex(value uint64) string {
	var encoded [8]byte
	binary.LittleEndian.PutUint64(encoded[:], value)
	return hex.EncodeToString(encoded[:])
}

func (j *job) notifyParams(clean bool) []any {
	branches := make([]string, len(j.merkleBranch))
	for i := range j.merkleBranch {
		branches[i] = hex.EncodeToString(j.merkleBranch[i][:])
	}
	return []any{j.id, hex.EncodeToString(j.parent[:]), hex.EncodeToString(j.coinbase1), hex.EncodeToString(j.coinbase2), branches, "", j.bits, littleEndianHex(j.timestamp), clean}
}

func (j *job) solve(extraNonce1, extraNonce2, encodedTime, encodedNonce string) (block []byte, hash [32]byte, err error) {
	extra1, err := hex.DecodeString(extraNonce1)
	if err != nil || len(extra1) != int(j.extra1Size) {
		return nil, hash, fmt.Errorf("extranonce1 must contain %d bytes of hexadecimal data", j.extra1Size)
	}
	extra2, err := hex.DecodeString(extraNonce2)
	if err != nil || len(extra2) != int(j.extra2Size) {
		return nil, hash, fmt.Errorf("extranonce2 must contain %d bytes of hexadecimal data", j.extra2Size)
	}
	timeBytes, err := hex.DecodeString(encodedTime)
	if err != nil || len(timeBytes) != 8 {
		return nil, hash, errors.New("timestamp must contain 8 bytes of hexadecimal data")
	}
	if binary.LittleEndian.Uint64(timeBytes) != j.timestamp {
		return nil, hash, errors.New("timestamp does not match the assigned job")
	}
	nonceBytes, err := hex.DecodeString(encodedNonce)
	if err != nil || len(nonceBytes) != 8 {
		return nil, hash, errors.New("nonce must contain 8 bytes of hexadecimal data")
	}
	coinbase := make([]byte, 0, len(j.coinbase1)+len(extra1)+len(extra2)+len(j.coinbase2))
	coinbase = append(coinbase, j.coinbase1...)
	coinbase = append(coinbase, extra1...)
	coinbase = append(coinbase, extra2...)
	coinbase = append(coinbase, j.coinbase2...)
	root := transactionLeaf(coinbase)
	for _, left := range j.merkleBranch {
		root = merklePair(left, root)
	}
	header := make([]byte, 80)
	copy(header[:32], j.parent[:])
	copy(header[32:40], nonceBytes)
	copy(header[40:48], timeBytes)
	copy(header[48:], root[:])
	hash = blake2b.Sum256(header)
	if bytes.Compare(hash[:], j.target[:]) > 0 {
		return nil, hash, errors.New("low difficulty share")
	}
	if j.extra1Size != 0 || j.extra2Size != 0 {
		decoder := types.NewBufDecoder(j.block)
		var encodedBlock types.V2Block
		encodedBlock.DecodeFrom(decoder)
		if decoder.Err() != nil {
			return nil, hash, errors.New("stored QDAY block is invalid")
		}
		decodedBlock := encodedBlock.Cast()
		if decodedBlock.V2 == nil || len(decodedBlock.V2.Transactions) == 0 {
			return nil, hash, errors.New("stored QDAY block has no mining transaction")
		}
		coinbaseDecoder := types.NewBufDecoder(coinbase)
		var work types.V2Transaction
		work.DecodeFrom(coinbaseDecoder)
		if coinbaseDecoder.Err() != nil {
			return nil, hash, errors.New("reconstructed QDAY mining transaction is invalid")
		}
		decodedBlock.V2.Transactions[len(decodedBlock.V2.Transactions)-1] = work
		decodedBlock.V2.Commitment = types.Hash256(root)
		decodedBlock.Nonce = binary.LittleEndian.Uint64(nonceBytes)
		decodedBlock.Timestamp = time.Unix(int64(binary.LittleEndian.Uint64(timeBytes)), 0)
		var encoded bytes.Buffer
		encoder := types.NewEncoder(&encoded)
		types.V2Block(decodedBlock).EncodeTo(encoder)
		if encoder.Flush() != nil {
			return nil, hash, errors.New("could not encode solved QDAY block")
		}
		block = encoded.Bytes()
	} else {
		block = append([]byte(nil), j.block...)
		copy(block[32:40], nonceBytes)
		copy(block[40:48], timeBytes)
	}
	return block, hash, nil
}
