package bridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/petoshi/qday-stratum/internal/nodeapi"
	"golang.org/x/crypto/blake2b"
)

var difficultyOneTarget = mustBigInt("00000000ffff0000000000000000000000000000000000000000000000000000")

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
	merkleBranch [][32]byte
	block        []byte
	difficulty   float64
	bits         string
	longPollID   string
	transactions int
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
	coinbase1, err := hex.DecodeString(template.Transactions[len(template.Transactions)-1].Data)
	if err != nil || len(coinbase1) == 0 {
		return nil, errors.New("rightmost QDAY template transaction is invalid")
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
	root := transactionLeaf(coinbase1)
	for _, left := range branches {
		root = merklePair(left, root)
	}
	if root != commitment {
		return nil, errors.New("QDAY node returned a Stratum branch that does not match its commitment")
	}
	block, err := hex.DecodeString(template.Stratum.Block)
	if err != nil || len(block) < 48 {
		return nil, errors.New("QDAY node returned an invalid complete block")
	}
	header, err := hex.DecodeString(template.Header)
	if err != nil || len(header) != 80 {
		return nil, errors.New("QDAY node returned an invalid 80-byte header")
	}
	if !bytes.Equal(header[:32], parent[:]) || !bytes.Equal(header[48:], commitment[:]) || binary.LittleEndian.Uint64(header[40:48]) != uint64(template.Timestamp) {
		return nil, errors.New("QDAY template fields do not describe the same header")
	}
	if !bytes.Equal(block[:48], header[:48]) {
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
		merkleBranch: branches,
		block:        block,
		difficulty:   difficulty,
		bits:         compactTarget(target),
		longPollID:   template.LongPollID,
		transactions: len(template.Transactions),
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
		return 0, errors.New("QDAY target is zero")
	}
	ratio := new(big.Float).SetPrec(256).Quo(
		new(big.Float).SetPrec(256).SetInt(difficultyOneTarget),
		new(big.Float).SetPrec(256).SetInt(t),
	)
	difficulty, accuracy := ratio.Float64()
	// Never advertise an easier target than QDAY. Sia miners reconstruct their
	// target from this float, so round upward when float64 rounded the exact
	// ratio downward.
	if accuracy == big.Below {
		difficulty = math.Nextafter(difficulty, math.Inf(1))
	}
	if difficulty <= 0 || math.IsInf(difficulty, 0) || math.IsNaN(difficulty) {
		return 0, errors.New("QDAY target cannot be represented as Stratum difficulty")
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
	return []any{
		j.id,
		hex.EncodeToString(j.parent[:]),
		hex.EncodeToString(j.coinbase1),
		"",
		branches,
		"",
		j.bits,
		littleEndianHex(j.timestamp),
		clean,
	}
}

func (j *job) solve(extraNonce1, extraNonce2, encodedTime, encodedNonce string) ([]byte, [32]byte, error) {
	var zero [32]byte
	if extraNonce1 != "" || extraNonce2 != "" {
		return nil, zero, errors.New("this solo bridge requires an empty extranonce")
	}
	timeBytes, err := hex.DecodeString(encodedTime)
	if err != nil || len(timeBytes) != 8 {
		return nil, zero, errors.New("timestamp must contain 8 bytes of hexadecimal data")
	}
	nonceBytes, err := hex.DecodeString(encodedNonce)
	if err != nil || len(nonceBytes) != 8 {
		return nil, zero, errors.New("nonce must contain 8 bytes of hexadecimal data")
	}
	root := transactionLeaf(j.coinbase1)
	for _, left := range j.merkleBranch {
		root = merklePair(left, root)
	}
	header := make([]byte, 80)
	copy(header[:32], j.parent[:])
	copy(header[32:40], nonceBytes)
	copy(header[40:48], timeBytes)
	copy(header[48:], root[:])
	hash := blake2b.Sum256(header)
	if bytes.Compare(hash[:], j.target[:]) > 0 {
		return nil, hash, errors.New("low difficulty share")
	}
	block := append([]byte(nil), j.block...)
	copy(block[32:40], nonceBytes)
	copy(block[40:48], timeBytes)
	return block, hash, nil
}
