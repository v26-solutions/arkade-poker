// Package game coordinates setup, poker play and deterministic private replay.
package game

import (
	"errors"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/wallet"
	"github.com/btcsuite/btcd/wire"
)

var (
	ErrInput      = errors.New("game: input is not permitted in this phase")
	ErrEncoding   = errors.New("game: invalid canonical encoding")
	ErrInvitation = errors.New("game: invalid invitation or service binding")
	ErrAmount     = errors.New("game: amount or output policy")
	ErrSecrets    = errors.New("game: missing, invalid or destroyed session secrets")
	ErrProtocol   = errors.New("game: invalid protocol evidence")
	ErrOrder      = errors.New("game: event or message order")
	ErrDeadline   = errors.New("game: initial deadline outside permitted window")
)

// Config is runtime public configuration, supplied again on restart. It must
// never contain the imported wallet key. New journals do not persist it.
type Config struct {
	Wallet                                         wallet.Config
	ArkdURL, EmulatorURL, DelegatorURL, IndexerURL string
}

type InputKind uint8

const (
	StartSession InputKind = iota + 1
	JoinSession
	Progress
	Bet
	Concede
	RevealShowdown
	ClaimTimeout
)

// Input is a user/driver command, not a durable fact. Decide must reject payloads
// inconsistent with Kind. Progress must never choose a player's bet or concession.
type Input struct {
	Kind       InputKind
	Terms      Terms
	RelayURL   string
	Invitation *Invitation
	Bet        covenant.BettingAction
}

type StepKind uint8

const (
	RecordEvent StepKind = iota + 1
	Waiting
	NeedsInput
	Finished
	RunEffect
)

type Choice struct {
	Allowed                []InputKind
	CanCheck, CanCall      bool
	MinRaiseTo, MaxRaiseTo int64
	CallAmount             int64
}

type OutcomeKind uint8

const (
	Won OutcomeKind = iota + 1
	Lost
	Tied
	Aborted
)

// A settlement requires an accepted terminal spend. A pre-lock setup abort is
// distinct; a submission error is neither outcome.
type Outcome struct {
	Kind        OutcomeKind
	Settlement  Settlement
	AbortReason string
	Transaction *wire.MsgTx
	Payouts     covenant.PerPlayer[wire.OutPoint]
}

type Step struct {
	Kind    StepKind
	Event   *Event
	Choice  *Choice
	Outcome *Outcome
	Effect  EffectKind
}

type KnownCard struct {
	Known bool
	Index byte
}

// Snapshot is a transient UI projection, never the authoritative replay state.
type Snapshot struct {
	Stage      Stage
	Role       covenant.Player
	Terms      Terms
	Invitation *Invitation
	State      *covenant.State
	Cards      covenant.DealtCards[KnownCard]
	Choice     *Choice
	Outcome    *Outcome
}
