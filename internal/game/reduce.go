package game

import (
	"bytes"
	"context"
	"math"

	"arkade-poker/go/internal/covenant"
)

type Stage uint8

const (
	StageInit Stage = iota
	StageSessionPrepared
	StagePlayer2KeysPrepared
	StageAwaitOpponentKeys
	StagePlayer1KeysPrepared
	StageInitialShuffle
	StageInitialShufflePrepared
	StageAwaitInitialShuffle
	StageFinalShuffle
	StageFinalShufflePrepared
	StageAwaitFinalShuffle
	StageInitialDeposit
	StageAwaitInitialDeposit
	StageAborted
	StagePlayer1Funding
	StageAwaitPlayer1Funding
	StagePlayer2Opening
	StageAwaitPlayer2Opening
	StageBettingLocal
	StageBettingOpponent
	StageBoardRevealLocal
	StageBoardRevealOpponent
	StageAllInLocal
	StageAllInOpponent
	StageAllInRevealLocal
	StageAllInRevealOpponent
	StageShowdownLocal
	StageShowdownOpponent
	StageEvaluateShowdown
	StageSettleShowdown
	StageTransactionPrepared
	StageTransactionSigned
	StageAwaitTransactionAcceptance
	StageFinished
)

type Game struct {
	config    Config
	stage     Stage
	setup     *setupState
	nextEvent uint64
	previous  [32]byte
	hand      *handState
	prepared  *preparedState
	evaluated *evaluatedState
	outcome   *Outcome
	completed *completedTable
}

// Only display data survives settlement. Keeping a handState here would leave
// a spent covenant available to the live action machinery.
type completedTable struct {
	state covenant.State
	cards covenant.DealtCards[KnownCard]
}

func New(config Config) (*Game, error) {
	b, err := encodeConfig(config)
	if err != nil {
		return nil, err
	}
	owned, err := decodeConfig(b)
	if err != nil {
		return nil, err
	}
	return &Game{config: owned, previous: walletBinding(owned.Wallet.WalletPublicKey)}, nil
}

// NewEvent supplies the current chain envelope. Callers still must obtain the
// actual fact and persist it before Apply; constructing it authorizes no effect.
func (g *Game) NewEvent(kind EventKind) Event {
	e := Event{Kind: kind, Sequence: g.nextEvent, Previous: g.previous}
	if g.setup != nil {
		e.SessionID = g.setup.invitation.SessionID
	}
	return e
}

// Apply is atomic, owns all admitted input buffers, and uses the same proof
// admission during live execution and replay. It never verifies signatures.
func (g *Game) Apply(event Event) error { return g.ApplyContext(context.Background(), event) }
func (g *Game) ApplyContext(ctx context.Context, event Event) error {
	if g == nil || ctx == nil {
		return ErrInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	previous := g.previous
	if g.nextEvent == 0 && event.Kind == Configured && event.Config != nil {
		// Legacy chains started with a full configuration hash. Verify their
		// original bytes while continuing to use the supplied runtime config.
		if err := event.CheckWallet(g.config.Wallet.WalletPublicKey); err != nil {
			return err
		}
		b, err := encodeConfig(*event.Config)
		if err != nil {
			return err
		}
		previous = digest("", b)
	}
	if event.Sequence != g.nextEvent || event.Previous != previous || g.nextEvent == math.MaxUint64 {
		return ErrOrder
	}
	if g.setup != nil && event.SessionID != g.setup.invitation.SessionID {
		return ErrOrder
	}
	encoded, err := EncodeEvent(event)
	if err != nil {
		return err
	}
	defer clear(encoded)
	owned, err := DecodeEvent(encoded)
	if err != nil {
		return err
	}
	admitted := false
	defer func() {
		if !admitted && owned.Secrets != nil {
			_ = owned.Secrets.Destroy()
		}
	}()
	candidate := *g
	if g.setup != nil {
		s := *g.setup
		candidate.setup = &s
	}
	if err := candidate.reduceSetup(ctx, owned); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	candidate.nextEvent++
	candidate.previous = digest("", encoded)
	*g = candidate
	admitted = true
	return nil
}
func (g *Game) reduceSetup(ctx context.Context, e Event) error {
	if g.nextEvent == 0 {
		if e.Kind != Configured || e.SessionID != (SessionID{}) {
			return ErrOrder
		}
		return e.CheckWallet(g.config.Wallet.WalletPublicKey)
	}
	if e.Kind == Configured {
		return ErrOrder
	}
	if e.Kind == SetupAborted {
		if !g.mayAbort(e.FailureCode) {
			return ErrInput
		}
		g.stage = StageAborted
		g.hand = nil
		g.prepared = nil
		g.evaluated = nil
		g.outcome = &Outcome{Kind: Aborted, AbortReason: e.FailureCode}
		return nil
	}
	if e.Kind == PublicationPrepared {
		m, err := g.PendingMessage()
		if err != nil || g.setup.publication != nil {
			return ErrOrder
		}
		payload, err := EncodeMessage(m)
		if err != nil || !bytes.Equal(payload, e.Publication.Payload) {
			return ErrProtocol
		}
		g.setup.publication = e.Publication
		return nil
	}
	switch g.stage {
	case StageInit:
		if e.Kind != SessionPrepared || e.SessionID != e.Invitation.SessionID {
			return ErrInput
		}
		if err := checkInvitation(g.config, *e.Invitation); err != nil {
			return err
		}
		local, err := e.Secrets.KeyMessage(g.config, *e.Invitation, e.Role)
		if err != nil {
			return err
		}
		// Wallet ownership is authenticated at the boundary, not by the FSM.
		local.WalletOwnership = bytes.Clone(e.Message.WalletOwnership)
		if !sameMessage(local, *e.Message) {
			return ErrProtocol
		}
		key, err := validateKeys(g.config, *e.Invitation, local, e.Role, &local.Identity)
		if err != nil {
			return err
		}
		s := &setupState{invitation: *e.Invitation, role: e.Role, secrets: e.Secrets, local: local}
		s.verifiedKeys[int(e.Role)-1] = key
		g.setup = s
		g.stage = StageSessionPrepared
	case StageSessionPrepared:
		if e.Kind != SessionOpened {
			return ErrInput
		}
		if g.setup.role == covenant.Player1 {
			g.stage = StageAwaitOpponentKeys
		} else {
			g.stage = StagePlayer2KeysPrepared
		}
	case StagePlayer2KeysPrepared, StagePlayer1KeysPrepared, StageInitialShufflePrepared, StageFinalShufflePrepared:
		if e.Kind != MessagePublished || g.setup.publication == nil {
			return ErrInput
		}
		s := g.setup
		expected := &s.local
		if s.pending != nil {
			expected = s.pending
		}
		if !sameMessage(*expected, *e.Message) || e.Message.Sequence != s.nextSend || s.nextSend == math.MaxUint64 {
			return ErrOrder
		}
		s.nextSend++
		s.pending = nil
		s.publication = nil
		switch g.stage {
		case StagePlayer2KeysPrepared:
			g.stage = StageAwaitOpponentKeys
		case StagePlayer1KeysPrepared:
			g.stage = StageAwaitInitialShuffle
		case StageInitialShufflePrepared:
			g.stage = StageAwaitFinalShuffle
		case StageFinalShufflePrepared:
			g.stage = StageAwaitInitialDeposit
		}
	case StageAwaitOpponentKeys:
		if e.Kind != MessageReceived {
			return ErrInput
		}
		if err := g.setup.admitPeer(g.config, *e.Message); err != nil {
			return err
		}
		if g.setup.role == covenant.Player1 {
			g.stage = StagePlayer1KeysPrepared
		} else {
			g.stage = StageInitialShuffle
		}
	case StageInitialShuffle, StageFinalShuffle:
		if e.Kind != MessagePrepared {
			return ErrInput
		}
		s := g.setup
		m := *e.Message
		if err := checkMessageContext(s.invitation, m, s.role, &s.local.Identity); err != nil {
			return err
		}
		if m.Sequence != s.nextSend {
			return ErrOrder
		}
		final := g.stage == StageFinalShuffle
		if err := s.admitShuffle(ctx, g.config, m, final); err != nil {
			return err
		}
		s.pending = &m
		if final {
			g.stage = StageFinalShufflePrepared
		} else {
			g.stage = StageInitialShufflePrepared
		}
	case StageAwaitInitialShuffle, StageAwaitFinalShuffle:
		if e.Kind != MessageReceived {
			return ErrInput
		}
		s := g.setup
		m := *e.Message
		if err := checkMessageContext(s.invitation, m, other(s.role), &s.peer.Identity); err != nil {
			return err
		}
		if m.Sequence != s.nextReceive || s.nextReceive == math.MaxUint64 {
			return ErrOrder
		}
		final := g.stage == StageAwaitFinalShuffle
		if final {
			if err := deadlineWindow(e.ObservedAt, m.InitialDeadline); err != nil {
				return err
			}
		}
		if err := s.admitShuffle(ctx, g.config, m, final); err != nil {
			return err
		}
		s.nextReceive++
		if final {
			g.stage = StageInitialDeposit
		} else {
			g.stage = StageFinalShuffle
		}
	case StageInitialDeposit, StageAwaitInitialDeposit:
		return g.reduceHand(e)
	default:
		return g.reduceHand(e)
	}
	return nil
}
func (g *Game) mayAbort(reason string) bool {
	// The reference reducer allows every listed reason throughout setup; live
	// handlers choose the appropriate reason. No local money is locked yet.
	return validAbortReason(reason) && (g.stage >= StageSessionPrepared && g.stage <= StageAwaitInitialDeposit || g.stage == StagePlayer1Funding)
}
func (g *Game) Snapshot() (Snapshot, error) {
	if g == nil {
		return Snapshot{}, ErrInput
	}
	s := Snapshot{Stage: g.stage}
	if g.setup != nil {
		s.Role, s.Terms = g.setup.role, g.setup.invitation.Terms
	}
	if g.setup != nil && g.stage != StageAborted && g.stage != StageFinished {
		inv := g.setup.invitation
		s.Invitation = &inv
	}
	if g.outcome != nil {
		o := *g.outcome
		if o.Transaction != nil {
			o.Transaction = o.Transaction.Copy()
		}
		s.Outcome = &o
	}
	if g.completed != nil {
		state := g.completed.state
		s.State, s.Cards = &state, g.completed.cards
	}
	if g.stage == StageInit && g.nextEvent != 0 {
		s.Choice = &Choice{Allowed: []InputKind{StartSession, JoinSession}}
	}
	if g.hand != nil {
		state, err := covenant.ReadState(g.hand.accepted.Transaction)
		if err != nil {
			return Snapshot{}, err
		}
		s.State = state
		cards, err := g.knownCards(g.hand)
		if err != nil {
			return Snapshot{}, err
		}
		s.Cards = cards
		choice, err := g.handChoice()
		if err != nil {
			return Snapshot{}, err
		}
		s.Choice = choice
	}
	return s, nil
}

// Destroy releases private replay material owned by a standalone Game. Drivers
// do this in Close. The caller must not use the Game after destruction.
func (g *Game) Destroy() {
	if g != nil {
		discardGame(g, nil)
		*g = Game{}
	}
}
