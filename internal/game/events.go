package game

import (
	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	"arkade-poker/go/internal/wallet"
	"github.com/btcsuite/btcd/wire"
)

type EventKind uint8

const (
	Configured EventKind = iota + 1 // Wallet-bound journal start; no runtime settings.
	SessionPrepared
	MessagePrepared
	MessagePublished
	MessageReceived
	_ // reserved: agreements are derived, not external facts
	SpendPrepared
	SpendSigned
	SpendObserved
	SubmissionFailed
	SessionOpened
	SetupAborted
	DepositObserved
	OpeningObserved
	ShowdownEvaluated
	SubmissionAttempted
	PublicationPrepared
)

// SavedSpend retains exact serialized PSBTs. Recovery may submit this work once
// after an explicitly unspent result; it must never rebuild, reselect or resign.
// Signed may be absent before signing. Neither bundle establishes acceptance.
type SavedSpend struct {
	Action        PreparedAction
	Route         wallet.Route
	Source        *wire.OutPoint // Nil before the initial deposit; wallet inputs below.
	WalletSources []wire.OutPoint
	Prepared      ports.Bundle
	Signed        *ports.Bundle
}

// Event is a typed envelope for the private durable log. Its codec enforces one
// payload matching Kind and canonical bounds; Apply enforces chain ordering.
// ObservedAt is recorded Unix seconds; replay never reads the current clock.
// Public carriers remain opaque; their payload is bound to the pending message.
type Event struct {
	SessionID    SessionID
	Sequence     uint64
	Previous     [32]byte
	ObservedAt   covenant.UnixSeconds
	Kind         EventKind
	Role         covenant.Player
	Config       *Config // Legacy journals only; never overrides runtime settings.
	Invitation   *Invitation
	Secrets      *SessionSecrets
	Message      *Message
	Publication  *ports.PreparedMessage
	Spend        *SavedSpend
	FailureCode  string // Bounded local code; never raw request/service diagnostics.
	Accepted     *AcceptedTransaction
	PrivateHoles *[2]covenant.CardReveal
	Evaluation   *covenant.ShowdownWitness
	Winner       covenant.Player
	Receipt      *SubmissionReceipt
}

// Replay applies the intact log without signing, submission or transport effects.
// It rechecks shuffle/card proofs and bundle integrity under the same admission
// rules as live events, without adding signature verification to the FSM.
func Replay(config Config, events []Event) (*Game, error) {
	if len(events) == 0 || events[0].Kind != Configured {
		return nil, ErrOrder
	}
	if err := events[0].CheckWallet(config.Wallet.WalletPublicKey); err != nil {
		return nil, err
	}
	g, err := New(config)
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		if err := g.Apply(event); err != nil {
			g.Destroy()
			return nil, err
		}
	}
	return g, nil
}

func walletBinding(public [32]byte) [32]byte {
	return digest("arkade-poker/wallet-log\x00", public[:])
}

// CheckWallet matches a journal's first event to the imported public identity.
// Old Configured snapshots remain readable without selecting startup settings.
func (e Event) CheckWallet(public [32]byte) error {
	if e.Kind != Configured {
		return ErrOrder
	}
	if e.Config != nil {
		if e.Config.Wallet.WalletPublicKey == public {
			return nil
		}
	} else if e.Previous == walletBinding(public) {
		return nil
	}
	return ErrWalletIdentity
}
