package game

import (
	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

// AcceptedTransaction is evidence supplied by the indexer boundary. Constructing
// it is not proof of acceptance. Replay rechecks the logical edges and poker
// effects from these exact transactions without querying services or signatures.
type AcceptedTransaction struct {
	Transaction *wire.MsgTx
	Checkpoints []*wire.MsgTx
}

type ActionKind uint8

const (
	ActionInitialDeposit ActionKind = iota + 1
	ActionPlayer1Funding
	ActionPlayer2Opening
	ActionBetting
	ActionBoardReveal
	ActionShowdownReveal
	ActionAllInCall
	ActionAllInReveal
	ActionConcession
	ActionTimeout
	ActionShowdown
)

// PreparedAction retains exact public builder inputs, including entropy-backed
// reveal proofs. Its codec requires precisely the fields selected by Kind.
type PreparedAction struct {
	Kind       ActionKind
	Bet        covenant.BettingAction
	Funding    *covenant.Funding
	Reveals    *covenant.RevealWitness
	Showdown   *covenant.ShowdownWitness
	ObservedAt covenant.UnixSeconds // timeout eligibility or settlement priority
}

type SubmissionReceipt struct {
	Recovery bool            // approved immediate, bounded exact-work reconciliation retry
	TxID     *chainhash.Hash // nil means uncertain; neither case establishes acceptance
}

type SettlementKind uint8

const (
	SettlementConcession SettlementKind = iota + 1
	SettlementTimeout
	SettlementShowdown
)

type Settlement struct {
	Kind   SettlementKind
	Winner covenant.Player
} // zero winner is a showdown tie

type handState struct {
	accepted     AcceptedTransaction
	observedAt   covenant.UnixSeconds
	reveals      [18]*covenant.CardReveal
	privateHoles *[2]covenant.CardReveal
}
type preparedState struct {
	saved       SavedSpend
	built       *covenant.Unsigned // rebuilt, compared and retained; never decoded as trusted
	lastAttempt covenant.UnixSeconds
}
type evaluatedState struct {
	witness covenant.ShowdownWitness
	winner  covenant.Player
}

func acceptedBuild(b *covenant.Unsigned) AcceptedTransaction {
	a := AcceptedTransaction{Transaction: b.Ark.UnsignedTx.Copy()}
	for _, p := range b.Checkpoints {
		a.Checkpoints = append(a.Checkpoints, p.UnsignedTx.Copy())
	}
	return a
}
func serializeBuild(b *covenant.Unsigned) (ports.Bundle, error) {
	if b == nil || b.Ark == nil || len(b.Checkpoints) > 256 {
		return ports.Bundle{}, ErrEncoding
	}
	var out ports.Bundle
	var err error
	out.Ark, err = b.Ark.B64Encode()
	if err != nil {
		return ports.Bundle{}, err
	}
	for _, p := range b.Checkpoints {
		if p == nil {
			return ports.Bundle{}, ErrEncoding
		}
		s, err := p.B64Encode()
		if err != nil {
			return ports.Bundle{}, err
		}
		out.Checkpoints = append(out.Checkpoints, s)
	}
	return out, nil
}
