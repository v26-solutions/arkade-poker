package merkel

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/bits"
	"sync"
)

const (
	RealLeafCount = 133784560
	TreeDepth     = 27
	LowLength     = 13
	HighLength    = TreeDepth - LowLength
	blockLeaves   = 1 << LowLength
	cacheNodes    = (1 << (HighLength + 1)) - 1
	RootHex       = "b5ade7f72592e46f3436407059d8db058ab2aab4ffef1d3eda28288f6e41b157"
)

var ErrProof = errors.New("invalid hand-rank proof")
var ErrCache = errors.New("invalid committed hand-rank cache")

//go:embed tree_cache.bin
var cacheBytes []byte

var leafTag = sha256.Sum256([]byte("arkade/poker/hand-rank/leaf/v1"))
var branchTag = sha256.Sum256([]byte("arkade/poker/hand-rank/branch/v1"))
var paddingTag = sha256.Sum256([]byte("arkade/poker/hand-rank/padding/v1"))

func Root() [32]byte { data, _ := hex.DecodeString(RootHex); return [32]byte(data) }

func tagged(tag [32]byte, data []byte) [32]byte {
	h := sha256.New()
	h.Write(tag[:])
	h.Write(tag[:])
	h.Write(data)
	return [32]byte(h.Sum(nil))
}

func parent(a, b [32]byte) [32]byte {
	if bytes.Compare(a[:], b[:]) > 0 {
		a, b = b, a
	}
	var data [64]byte
	copy(data[:32], a[:])
	copy(data[32:], b[:])
	return tagged(branchTag, data[:])
}

// Nine-byte payload: U56_LE(card bitmask) || U16_LE(rank).
func payload(cards [7]byte, rank uint16) [9]byte {
	var mask uint64
	for _, c := range cards {
		mask |= uint64(1) << c
	}
	var data [9]byte
	binary.LittleEndian.PutUint64(data[:8], mask)
	binary.LittleEndian.PutUint16(data[7:], rank)
	return data
}

// Proof segments contain leaf-to-root siblings with sorted-child branch hashes.
// There are exactly 13 lower and 14 upper hashes; no directions or index bytes.
type Proof struct {
	Low  [LowLength][32]byte
	High [HighLength][32]byte
}
type HandProof struct {
	Rank  uint16
	Proof Proof
}

func (p Proof) MarshalBinary() ([]byte, error) {
	data := make([]byte, 0, TreeDepth*32)
	for _, h := range p.Low {
		data = append(data, h[:]...)
	}
	for _, h := range p.High {
		data = append(data, h[:]...)
	}
	return data, nil
}

func DecodeProof(data []byte) (Proof, error) {
	if len(data) != TreeDepth*32 {
		return Proof{}, ErrProof
	}
	var p Proof
	for i := range p.Low {
		copy(p.Low[i][:], data[i*32:(i+1)*32])
	}
	for i := range p.High {
		copy(p.High[i][:], data[(i+LowLength)*32:(i+LowLength+1)*32])
	}
	return p, nil
}

func (p Proof) VerifyPayload(data [9]byte, root [32]byte) bool {
	// These checks keep semantically invalid payloads out of the application,
	// even when a caller supplies an unrelated root.
	var maskBytes [8]byte
	copy(maskBytes[:7], data[:7])
	mask := binary.LittleEndian.Uint64(maskBytes[:])
	rank := binary.LittleEndian.Uint16(data[7:])
	if mask>>52 != 0 || bits.OnesCount64(mask) != 7 || rank < 1 || rank > NumRanks {
		return false
	}
	node := tagged(leafTag, data[:])
	for _, s := range p.Low {
		node = parent(node, s)
	}
	for _, s := range p.High {
		node = parent(node, s)
	}
	return node == root
}

func (h HandProof) Verify(cards [7]byte) bool {
	if validate(cards[:]) != nil {
		return false
	}
	return h.Proof.VerifyPayload(payload(cards, h.Rank), Root())
}

func offset(level int) int {
	return (1 << (TreeDepth + 1 - LowLength)) - (1 << (TreeDepth + 1 - level))
}
func cacheNode(level, index int) [32]byte {
	return [32]byte(cacheBytes[(offset(level)+index)*32:][:32])
}

var cacheOnce sync.Once
var cacheError error

func validateCache() error {
	cacheOnce.Do(func() {
		if len(cacheBytes) != cacheNodes*32 {
			cacheError = ErrCache
			return
		}
		for level := LowLength + 1; level <= TreeDepth; level++ {
			for i := 0; i < 1<<(TreeDepth-level); i++ {
				if parent(cacheNode(level-1, 2*i), cacheNode(level-1, 2*i+1)) != cacheNode(level, i) {
					cacheError = ErrCache
					return
				}
				if i%256 == 0 {
					cooperate()
				}
			}
		}
		if cacheNode(TreeDepth, 0) != Root() {
			cacheError = ErrCache
		}
	})
	return cacheError
}

func Generate(ctx context.Context, cards [7]byte) (HandProof, error) {
	if err := validate(cards[:]); err != nil {
		return HandProof{}, err
	}
	if err := ctx.Err(); err != nil {
		return HandProof{}, err
	}
	if err := validateCache(); err != nil {
		return HandProof{}, err
	}
	table := rankTable()
	index := combinationIndex(cards)
	block := index / blockLeaves
	nodes := make([][32]byte, blockLeaves)
	for i := range nodes {
		if i%64 == 0 {
			if err := ctx.Err(); err != nil {
				return HandProof{}, err
			}
			cooperate()
		}
		leafIndex := block*blockLeaves + uint64(i)
		if leafIndex < RealLeafCount {
			leafCards := combinationAt(leafIndex)
			data := payload(leafCards, rank7(leafCards, table))
			nodes[i] = tagged(leafTag, data[:])
		} else {
			var data [4]byte
			binary.BigEndian.PutUint32(data[:], uint32(leafIndex-RealLeafCount))
			nodes[i] = tagged(paddingTag, data[:])
		}
	}
	p := Proof{}
	pos := int(index % blockLeaves)
	for level := range p.Low {
		p.Low[level] = nodes[pos^1]
		for i := 0; i < len(nodes)/2; i++ {
			nodes[i] = parent(nodes[2*i], nodes[2*i+1])
		}
		nodes = nodes[:len(nodes)/2]
		pos /= 2
	}
	if nodes[0] != cacheNode(LowLength, int(block)) {
		return HandProof{}, ErrCache
	}
	for i := range p.High {
		level := i + LowLength
		p.High[i] = cacheNode(level, int(index>>level)^1)
	}
	h := HandProof{Rank: rank7(cards, table), Proof: p}
	if !h.Verify(cards) {
		return HandProof{}, ErrProof
	}
	return h, nil
}

type Evaluation struct {
	Player1, Player2 HandProof
	Winner           int
}

// Evaluate uses protocol deal order: P1 holes, P2 holes, flop, turn, river.
// Winner is 0 for a tie, 1 for player 1, and 2 for player 2.
func Evaluate(ctx context.Context, deal [9]byte) (Evaluation, error) {
	if err := validate(deal[:]); err != nil {
		return Evaluation{}, err
	}
	var e Evaluation
	var err error
	e.Player1, err = Generate(ctx, [7]byte{deal[0], deal[1], deal[4], deal[5], deal[6], deal[7], deal[8]})
	if err != nil {
		return Evaluation{}, err
	}
	e.Player2, err = Generate(ctx, [7]byte{deal[2], deal[3], deal[4], deal[5], deal[6], deal[7], deal[8]})
	if err != nil {
		return Evaluation{}, err
	}
	if e.Player1.Rank > e.Player2.Rank {
		e.Winner = 1
	} else if e.Player2.Rank > e.Player1.Rank {
		e.Winner = 2
	}
	return e, nil
}
