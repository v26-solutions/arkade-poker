package covenant

import (
	"encoding/binary"
	"fmt"

	"arkade-poker/go/internal/merkel"
	"github.com/btcsuite/btcd/wire"
)

func (c *Contract) Concession(previous *wire.MsgTx) (*Unsigned, error) {
	phase, value, err := c.exitPredecessor(previous)
	if err != nil {
		return nil, err
	}
	switch phase.Kind {
	case Betting, AllIn, ShowdownReveal, AllInReveal:
	default:
		return nil, fmt.Errorf("covenant: concession unavailable in this phase")
	}
	bond := c.params.Bond
	if value < bond {
		return nil, fmt.Errorf("covenant: covenant value below bond")
	}
	p1, p2 := bond, value-bond
	if phase.Actor == Player2 {
		p1, p2 = p2, p1
	}
	return c.buildTerminal(previous, SpendingIdentity{Kind: SpendConcession, Actor: phase.Actor}, []*wire.TxOut{c.payout(Player1, p1), c.payout(Player2, p2)}, nil)
}

// Timeout selects the opponent of the obligated actor without consulting the
// local clock. Only emulator execution checks maturity; evaluation has no timeout.
func (c *Contract) Timeout(previous *wire.MsgTx) (*Unsigned, error) {
	phase, value, err := c.exitPredecessor(previous)
	if err != nil {
		return nil, err
	}
	actor, err := phase.RequiredActor()
	if err != nil {
		return nil, err
	}
	if actor == 0 {
		return nil, fmt.Errorf("covenant: timeout unavailable in showdown evaluation")
	}
	beneficiary := otherPlayer(actor)
	return c.buildTerminal(previous, SpendingIdentity{Kind: SpendTimeout, Actor: beneficiary}, []*wire.TxOut{c.payout(beneficiary, value)}, nil)
}

// Showdown constructs the payouts implied by the claimed ranks; the emulator
// proves the decryption, distinct cards and both Merkle ranks before acceptance.
func (c *Contract) Showdown(previous *wire.MsgTx, submitter Player, witness ShowdownWitness) (*Unsigned, error) {
	state, err := c.livePredecessor(previous)
	if err != nil {
		return nil, err
	}
	if state.Phase != (Phase{Kind: ShowdownEvaluation}) {
		return nil, fmt.Errorf("covenant: showdown requires evaluation")
	}
	if err := startEqual(state); err != nil {
		return nil, err
	}
	encoded, err := encodeShowdownWitness(witness)
	if err != nil {
		return nil, err
	}
	total, _ := c.liveValue(state)
	p1, p2 := total/2, total/2
	if witness.RankProofs.Player1.Rank > witness.RankProofs.Player2.Rank {
		p1, p2 = total-c.params.Bond, c.params.Bond
	}
	if witness.RankProofs.Player1.Rank < witness.RankProofs.Player2.Rank {
		p1, p2 = c.params.Bond, total-c.params.Bond
	}
	return c.buildTerminal(previous, SpendingIdentity{Kind: SpendShowdown, Actor: submitter}, []*wire.TxOut{c.payout(Player1, p1), c.payout(Player2, p2)}, encoded)
}
func encodeShowdownWitness(witness ShowdownWitness) (wire.TxWitness, error) {
	cards := dealtOrder(witness.Cards)
	var seen [52]bool
	for _, card := range cards {
		if card >= 52 || seen[card] {
			return nil, fmt.Errorf("covenant: showdown cards must be distinct and below 52")
		}
		seen[card] = true
	}
	encoded := make(wire.TxWitness, 0, 7)
	for _, proof := range []merkel.HandProof{witness.RankProofs.Player1, witness.RankProofs.Player2} {
		if proof.Rank < 1 || proof.Rank > 7462 {
			return nil, fmt.Errorf("covenant: hand rank must be in 1..7462")
		}
		siblings, err := proof.Proof.MarshalBinary()
		if err != nil {
			return nil, err
		}
		encoded = append(encoded, binary.LittleEndian.AppendUint16(nil, proof.Rank), append([]byte(nil), siblings[:merkel.LowLength*32]...), append([]byte(nil), siblings[merkel.LowLength*32:]...))
	}
	return append(encoded, append([]byte(nil), cards[:]...)), nil
}
