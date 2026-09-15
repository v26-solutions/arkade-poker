package covenant

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"

	"arkade-poker/go/internal/shuffle"
	"github.com/arkade-os/emulator/pkg/arkade"
)

var revealPackets = [...]struct {
	kind   byte
	length int
}{{typeOpening, 256}, {typePlayer1, 448}, {typePlayer2, 448}}

func u64LE(n uint64) []byte { return binary.LittleEndian.AppendUint64(nil, n) }
func scriptUnsigned(b []byte) []byte {
	n := len(b)
	for n > 0 && b[n-1] == 0 {
		n--
	}
	out := append([]byte(nil), b[:n]...)
	if n > 0 && b[n-1]&0x80 != 0 {
		out = append(out, 0)
	}
	return out
}
func curveConstants() (order, gx, gy []byte) {
	// secp256k1 order and standard generator; bytecode uses little-endian numbers.
	order, _ = hex.DecodeString("fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141")
	gx, _ = hex.DecodeString("79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
	gy, _ = hex.DecodeString("483ada7726a3c4655da4fbfc0e1108a8fd17b448a68554199c47d08ffb10d4b8")
	slices.Reverse(order)
	slices.Reverse(gx)
	slices.Reverse(gy)
	return
}
func (p *programBuilder) pointNumbers() *programBuilder {
	return p.op(arkade.OP_DUP).num(0).num(32).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM, arkade.OP_SWAP).num(32).num(32).op(arkade.OP_SUBSTR).data([]byte{0}).op(arkade.OP_CAT, arkade.OP_BIN2NUM)
}
func emitInitialReveals() ([]byte, error) {
	p := newProgram()
	zeros := make([]byte, 448)
	for _, v := range revealPackets {
		p.num(int64(v.kind)).num(0).op(arkade.OP_INSPECTINPUTPACKET, arkade.OP_VERIFY).data(zeros[:v.length]).op(arkade.OP_EQUALVERIFY)
	}
	return p.bytes()
}

// All actual publication batches are contiguous within each affected packet.
// Unchanged ranges are preserved byte-for-byte; initial funding establishes
// zero unpublished ranges and accepted history carries the invariant forward.
func emitRevealPreservation(changed []byte) ([]byte, error) {
	for _, slot := range changed {
		if int(slot) >= len(revealLocations) {
			return nil, fmt.Errorf("covenant: invalid reveal slot")
		}
	}
	p := newProgram()
	for _, v := range revealPackets {
		p.num(int64(v.kind)).num(0).op(arkade.OP_INSPECTINPUTPACKET, arkade.OP_VERIFY, arkade.OP_SIZE).num(int64(v.length)).op(arkade.OP_NUMEQUALVERIFY)
		p.num(int64(v.kind)).op(arkade.OP_INSPECTPACKET, arkade.OP_VERIFY, arkade.OP_SIZE).num(int64(v.length)).op(arkade.OP_NUMEQUALVERIFY)
		var offsets []int
		for _, slot := range changed {
			loc := revealLocations[slot]
			if typeState+byte(loc.packet) == v.kind {
				offsets = append(offsets, loc.offset)
			}
		}
		if len(offsets) == 0 {
			p.op(arkade.OP_EQUALVERIFY)
			continue
		}
		for i := 1; i < len(offsets); i++ {
			if offsets[i] != offsets[i-1]+64 {
				return nil, fmt.Errorf("covenant: noncontiguous reveal batch")
			}
		}
		start, end := offsets[0], offsets[len(offsets)-1]+64
		for _, span := range [][2]int{{0, start}, {end, v.length - end}} {
			offset, count := span[0], span[1]
			if count == 0 {
				continue
			}
			p.op(arkade.OP_OVER).num(int64(offset)).num(int64(count)).op(arkade.OP_SUBSTR, arkade.OP_OVER).num(int64(offset)).num(int64(count)).op(arkade.OP_SUBSTR, arkade.OP_EQUALVERIFY)
		}
		p.op(arkade.OP_2DROP)
	}
	return p.bytes()
}

type revealCardSlot struct {
	slot byte
	card shuffle.MaskedCard
}

func remainingRevealCards(params Params, street Street, actor Player) []revealCardSlot {
	player := byte(actor - Player1)
	deal := params.EncryptedDeal
	var cards []revealCardSlot
	if street == PreFlop {
		for i, card := range deal.Flop {
			cards = append(cards, revealCardSlot{4 + 3*player + byte(i), card})
		}
	}
	if street == PreFlop || street == Flop {
		cards = append(cards, revealCardSlot{10 + player, deal.Turn})
	}
	if street != River {
		cards = append(cards, revealCardSlot{12 + player, deal.River})
	}
	holes := deal.HoleCards.Player1
	if actor == Player2 {
		holes = deal.HoleCards.Player2
	}
	for i, card := range holes {
		cards = append(cards, revealCardSlot{14 + 2*player + byte(i), card})
	}
	return cards
}
func (p *programBuilder) remainingShares(params Params, id ContractID, actor Player, cards []revealCardSlot) *programBuilder {
	slots := make([]byte, len(cards))
	for i, card := range cards {
		slots[i] = card.slot
	}
	p.include(emitRevealPreservation(slots))
	for _, card := range cards {
		p.include(emitRevealProof(params, id, actor, card.slot, card.card))
	}
	return p
}
func dealtOrder[T any](d DealtCards[T]) [9]T {
	return [9]T{d.HoleCards.Player1[0], d.HoleCards.Player1[1], d.HoleCards.Player2[0], d.HoleCards.Player2[1], d.Flop[0], d.Flop[1], d.Flop[2], d.Turn, d.River}
}
