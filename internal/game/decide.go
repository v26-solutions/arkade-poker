package game

import (
	"context"
	"io"
	"math"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/shuffle"
)

type EffectKind uint8

const (
	PrepareSessionEffect EffectKind = iota + 1
	OpenSessionEffect
	PublishMessageEffect
	ReceiveMessageEffect
	PrepareShuffleEffect
	// Final publication must attach the agreed covenant script watch first.
	WatchAndPublishFinalEffect
)

func validateInput(input Input) error {
	if input.Kind < StartSession || input.Kind > ClaimTimeout {
		return ErrInput
	}
	if input.Kind != StartSession && (input.Terms != (Terms{}) || input.RelayURL != "") {
		return ErrInput
	}
	if (input.Invitation != nil) != (input.Kind == JoinSession) {
		return ErrInput
	}
	if input.Kind != Bet && input.Bet != (covenant.BettingAction{}) {
		return ErrInput
	}
	if input.Kind == Bet && (input.Bet.Kind < covenant.Check || input.Bet.Kind > covenant.RaiseTo || input.Bet.Kind != covenant.RaiseTo && input.Bet.Amount != 0) {
		return ErrInput
	}
	return nil
}

// Decide is pure. A RunEffect result describes the next driver operation; it
// neither runs that operation nor advances the event chain.
func (g *Game) Decide(input Input) (Step, error) {
	if g == nil {
		return Step{}, ErrInput
	}
	if err := validateInput(input); err != nil {
		return Step{}, err
	}
	if g.nextEvent == 0 {
		e := g.NewEvent(Configured)
		return Step{Kind: RecordEvent, Event: &e}, nil
	}
	if g.stage == StageInit {
		switch input.Kind {
		case Progress:
			return Step{Kind: NeedsInput, Choice: &Choice{Allowed: []InputKind{StartSession, JoinSession}}}, nil
		case StartSession:
			if err := input.Terms.Validate(); err != nil {
				return Step{}, err
			}
			if err := validateRelay(input.RelayURL); err != nil {
				return Step{}, err
			}
		case JoinSession:
			if err := checkInvitation(g.config, *input.Invitation); err != nil {
				return Step{}, err
			}
		default:
			return Step{}, ErrInput
		}
		return Step{Kind: RunEffect, Effect: PrepareSessionEffect}, nil
	}
	if g.stage >= StageInitialDeposit && g.stage != StageAborted {
		return g.decideHandEffect(input)
	}
	if input.Kind != Progress {
		return Step{}, ErrInput
	}
	switch g.stage {
	case StageSessionPrepared:
		return Step{Kind: RunEffect, Effect: OpenSessionEffect}, nil
	case StagePlayer2KeysPrepared, StagePlayer1KeysPrepared, StageInitialShufflePrepared:
		return Step{Kind: RunEffect, Effect: PublishMessageEffect}, nil
	case StageFinalShufflePrepared:
		return Step{Kind: RunEffect, Effect: WatchAndPublishFinalEffect}, nil
	case StageAwaitOpponentKeys, StageAwaitInitialShuffle, StageAwaitFinalShuffle:
		return Step{Kind: RunEffect, Effect: ReceiveMessageEffect}, nil
	case StageInitialShuffle, StageFinalShuffle:
		return Step{Kind: RunEffect, Effect: PrepareShuffleEffect}, nil
	case StageAborted:
		o := *g.outcome
		return Step{Kind: Finished, Outcome: &o}, nil
	default:
		return Step{}, ErrInput
	}
}

// PrepareSession performs only entropy-backed local work. Its returned event
// must be saved before opening transport or sharing the invitation.
func (g *Game) PrepareSession(ctx context.Context, entropy io.Reader, input Input) (Event, error) {
	step, err := g.Decide(input)
	if err != nil {
		return Event{}, err
	}
	if step.Kind != RunEffect || step.Effect != PrepareSessionEffect {
		return Event{}, ErrInput
	}
	var inv Invitation
	var secrets *SessionSecrets
	role := covenant.Player1
	if input.Kind == StartSession {
		inv, secrets, err = CreateInvitation(ctx, entropy, g.config, input.Terms, input.RelayURL)
	} else {
		inv = *input.Invitation
		role = covenant.Player2
		secrets, err = AcceptInvitation(ctx, entropy, g.config, inv)
	}
	if err != nil {
		return Event{}, err
	}
	keys, err := secrets.KeyMessage(g.config, inv, role)
	if err != nil {
		_ = secrets.Destroy()
		return Event{}, err
	}
	e := g.NewEvent(SessionPrepared)
	e.SessionID = inv.SessionID
	e.Invitation = &inv
	e.Secrets = secrets
	e.Role = role
	e.Message = &keys
	return e, nil
}

// PrepareShuffle reads the clock only after final proof generation, preserving
// P1's full funding window. Replay uses the resulting recorded deadline.
func (g *Game) PrepareShuffle(ctx context.Context, entropy io.Reader, now func() (covenant.UnixSeconds, error)) (Event, error) {
	if g == nil || g.setup == nil || (g.stage != StageInitialShuffle && g.stage != StageFinalShuffle) {
		return Event{}, ErrInput
	}
	s := g.setup
	final := g.stage == StageFinalShuffle
	if final && now == nil {
		return Event{}, ErrInput
	}
	binding, err := s.shuffleContext(final)
	if err != nil {
		return Event{}, err
	}
	protocol, err := shuffle.New()
	if err != nil {
		return Event{}, err
	}
	var deck shuffle.Deck
	var proof shuffle.ShuffleProof
	if final {
		deck, proof, err = protocol.Shuffle(ctx, entropy, s.aggregate, s.verifiedInitial, binding)
	} else {
		deck, proof, err = protocol.ShuffleInitial(ctx, entropy, s.aggregate, binding)
	}
	if err != nil {
		return Event{}, err
	}
	m := Message{SessionID: s.invitation.SessionID, Sequence: s.nextSend, Sender: s.role, Identity: s.local.Identity, Kind: InitialShuffle, Deck: &deck, ShuffleProof: &proof}
	if final {
		at, err := now()
		if err != nil {
			return Event{}, err
		}
		if uint64(at) > math.MaxUint64-covenant.DeadlineInterval {
			return Event{}, ErrDeadline
		}
		m.Kind = FinalShuffle
		m.InitialDeadline = at + covenant.DeadlineInterval
	}
	e := g.NewEvent(MessagePrepared)
	e.Message = &m
	return e, nil
}

// PendingMessage returns an owned public envelope for exact publication retry.
func (g *Game) PendingMessage() (Message, error) {
	if g == nil || g.setup == nil {
		return Message{}, ErrInput
	}
	var m *Message
	switch g.stage {
	case StagePlayer2KeysPrepared, StagePlayer1KeysPrepared:
		m = &g.setup.local
	case StageInitialShufflePrepared, StageFinalShufflePrepared:
		m = g.setup.pending
	default:
		return Message{}, ErrInput
	}
	b, err := EncodeMessage(*m)
	if err != nil {
		return Message{}, err
	}
	return DecodeMessage(b)
}

// AgreedContract is derived locally from the two verified shuffles. Its public
// methods return owned values and it has no exported mutable fields.
func (g *Game) AgreedContract() (*covenant.Contract, error) {
	if g == nil || g.setup == nil || g.setup.contract == nil {
		return nil, ErrInput
	}
	return g.setup.contract, nil
}
