package game

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math"
	"time"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
)

func eventStep(e Event) Step { return Step{Kind: RecordEvent, Event: &e} }
func (d *Driver) abort(reason string) Step {
	e := d.journal.game.NewEvent(SetupAborted)
	e.FailureCode = reason
	return eventStep(e)
}

func (d *Driver) step(ctx context.Context, input Input) (Step, error) {
	g := d.journal.game
	step, err := g.Decide(input)
	if err != nil || step.Kind != RunEffect {
		return step, err
	}
	// Record the start of blocking work, without flooding logs on wait polls.
	if d.lastLoggedEffect != step.Effect {
		slog.Info("Game operation", "operation", step.Effect.diagnosticName(), "stage", g.stage.diagnosticName())
		d.lastLoggedEffect = step.Effect
	}
	switch step.Effect {
	case PrepareSessionEffect:
		if err := d.emit(ctx, nil, true, nil); err != nil {
			return Step{}, err
		}
		e, err := g.PrepareSession(ctx, d.config.Entropy, input)
		if err != nil {
			return Step{}, err
		}
		digest, err := WalletOwnershipDigest(*e.Invitation, *e.Message)
		if err == nil {
			e.Message.WalletOwnership, err = d.config.Wallet.ProveOwnership(ctx, digest)
		}
		if err != nil {
			_ = e.Secrets.Destroy()
			return Step{}, err
		}
		return eventStep(e), nil
	case OpenSessionEffect:
		if err := d.openTransport(ctx); err != nil {
			return Step{}, err
		}
		return eventStep(g.NewEvent(SessionOpened)), nil
	case PrepareShuffleEffect:
		if err := d.emit(ctx, nil, true, nil); err != nil {
			return Step{}, err
		}
		e, err := g.PrepareShuffle(ctx, d.config.Entropy, d.now)
		return eventStep(e), err
	case PublishMessageEffect, WatchAndPublishFinalEffect:
		if step.Effect == WatchAndPublishFinalEffect {
			if err := d.admitAgreement(); err != nil {
				return Step{}, err
			}
			if err := d.ensureWatch(ctx); err != nil {
				return Step{}, err
			}
		}
		if err := d.openTransport(ctx); err != nil {
			return Step{}, err
		}
		message, err := g.PendingMessage()
		if err != nil {
			return Step{}, err
		}
		publication, err := g.PendingPublication()
		if errors.Is(err, ErrInput) {
			payload, err := EncodeMessage(message)
			if err != nil {
				return Step{}, err
			}
			at, err := d.now()
			if err != nil || uint64(at) > math.MaxInt64 {
				return Step{}, ErrDeadline
			}
			publication, err = d.config.Transport.Prepare(ctx, d.peerRequest(g.setup.role, message.Sequence), payload, int64(at))
			if err != nil {
				return Step{}, err
			}
			if !bytes.Equal(publication.Payload, payload) {
				return Step{}, protocolError("carrier payload changed")
			}
			e := g.NewEvent(PublicationPrepared)
			e.Publication = &publication
			return eventStep(e), nil
		}
		if err != nil {
			return Step{}, err
		}
		// No timestamp, serializer or signer is called when retrying saved publication.
		if err := d.config.Transport.Publish(ctx, publication.Carrier); err != nil {
			return Step{}, err
		}
		e := g.NewEvent(MessagePublished)
		e.Message = &message
		return eventStep(e), nil
	case ReceiveMessageEffect:
		return d.receiveMessage(ctx)
	case PrepareDepositEffect:
		if err := d.admitAgreement(); err != nil {
			return Step{}, err
		}
		if err := d.ensureWatch(ctx); err != nil {
			return Step{}, err
		}
		at, err := d.now()
		if err != nil {
			return Step{}, err
		}
		if deadlineWindow(at, g.setup.params.InitialDeadline) != nil {
			return d.abort("initial_deadline_expired"), nil
		}
		return d.prepareAction(ctx, input, at)
	case DiscoverDepositEffect:
		if err := d.ensureWatch(ctx); err != nil {
			return Step{}, err
		}
		e, err := d.discoverDeposit(ctx)
		if err != nil {
			return Step{}, err
		}
		if e != nil {
			return eventStep(*e), nil
		}
		hint, err := d.waitHint(ctx, 250*time.Millisecond)
		if err != nil {
			return Step{}, err
		}
		e, err = d.discoverDeposit(ctx)
		if err != nil {
			return Step{}, err
		}
		if e != nil {
			return eventStep(*e), nil
		}
		e, err = d.hintedDeposit(ctx, hint)
		if err != nil {
			return Step{}, err
		}
		if e != nil {
			return eventStep(*e), nil
		}
		at, err := d.now()
		if err != nil {
			return Step{}, err
		}
		if deadlineWindow(at, g.setup.params.InitialDeadline) != nil {
			return d.abort("initial_deadline_expired"), nil
		}
		return Step{Kind: Waiting}, nil
	case PrepareOpeningEffect:
		if err := d.emit(ctx, nil, true, nil); err != nil {
			return Step{}, err
		}
		e, err := g.PrepareOpening(ctx, d.config.Entropy, g.hand.accepted, g.hand.observedAt)
		return eventStep(e), err
	case ObserveLocalActionEffect:
		checked, err := d.inspectSource(ctx)
		if err != nil {
			return Step{}, err
		}
		if checked.event != nil {
			return eventStep(*checked.event), nil
		}
		at, err := d.now()
		if err != nil {
			return Step{}, err
		}
		if g.stage == StagePlayer1Funding && deadlineWindow(at, g.setup.params.InitialDeadline) != nil {
			return d.abort("initial_deadline_expired"), nil
		}
		choice, err := g.handChoice()
		if err != nil {
			return Step{}, err
		}
		if input.Kind == Progress && choice != nil {
			return Step{Kind: NeedsInput, Choice: choice}, nil
		}
		return d.prepareAction(ctx, input, at)
	case WaitOpponentEffect:
		e, err := d.waitForSource(ctx)
		if err != nil {
			return Step{}, err
		}
		if e != nil {
			return eventStep(*e), nil
		}
		at, err := d.now()
		if err != nil {
			return Step{}, err
		}
		state, err := covenant.ReadState(g.hand.accepted.Transaction)
		if err != nil {
			return Step{}, err
		}
		if at >= state.Deadline {
			if input.Kind == ClaimTimeout {
				return d.prepareAction(ctx, input, at)
			}
			// Project the already permitted timeout input after source observation.
			// Progress still never chooses or submits the claim.
			return Step{Kind: Waiting, Choice: &Choice{Allowed: []InputKind{ClaimTimeout}}}, nil
		}
		return Step{Kind: Waiting}, nil
	case EvaluateShowdownEffect:
		if err := d.emit(ctx, nil, true, nil); err != nil {
			return Step{}, err
		}
		e, err := g.PrepareEvaluation(ctx)
		return eventStep(e), err
	case WaitSettlementEffect:
		e, err := d.waitForSource(ctx)
		if err != nil {
			return Step{}, err
		}
		if e != nil {
			return eventStep(*e), nil
		}
		at, err := d.now()
		if err != nil {
			return Step{}, err
		}
		eligible, err := g.settlementEligible(g.evaluated.winner, at)
		if err != nil {
			return Step{}, err
		}
		if !eligible {
			return Step{Kind: Waiting}, nil
		}
		return d.prepareAction(ctx, input, at)
	case SignPreparedEffect, SubmitSignedEffect, ReconcileSubmissionEffect:
		if err := d.admitAgreement(); err != nil {
			return Step{}, err
		}
		if err := d.ensureWatch(ctx); err != nil {
			return Step{}, err
		}
		checked, err := d.inspectPrepared(ctx)
		if err != nil {
			return Step{}, err
		}
		if checked.event != nil {
			return eventStep(*checked.event), nil
		}
		at, err := d.now()
		if err != nil {
			return Step{}, err
		}
		if step.Effect == ReconcileSubmissionEffect {
			if uint64(g.prepared.lastAttempt) > math.MaxUint64-5 {
				return Step{}, protocolError("retry time overflow")
			}
			if at < g.prepared.lastAttempt+5 {
				return Step{Kind: Waiting}, nil
			}
			checked, err = d.inspectPrepared(ctx)
			if err != nil {
				return Step{}, err
			}
			if checked.event != nil {
				return eventStep(*checked.event), nil
			}
		}
		if err := d.checkSigningTime(at); err != nil {
			return Step{}, err
		}
		saved, err := g.PendingSpend()
		if err != nil {
			return Step{}, err
		}
		if step.Effect == SignPreparedEffect {
			signingInput := ports.Bundle{Ark: saved.Prepared.Ark, Checkpoints: append([]string(nil), saved.Prepared.Checkpoints...)}
			signed, err := d.config.Wallet.Sign(ctx, signingInput)
			if err != nil {
				return Step{}, err
			}
			saved.Signed = &signed
			e := g.NewEvent(SpendSigned)
			e.Spend = &saved
			return eventStep(e), nil
		}
		if saved.Signed == nil {
			return Step{}, ErrProtocol
		}
		result, err := d.config.Wallet.Submit(ctx, saved.Route, *saved.Signed)
		if err != nil {
			e := g.NewEvent(SubmissionAttempted)
			e.ObservedAt = at
			e.Receipt = &SubmissionReceipt{}
			return eventStep(e), nil // Persist uncertainty before bounded reconciliation.
		}
		e, err := d.submissionEvent(result, false, at)
		return eventStep(e), err
	}
	return Step{}, ErrInput
}
func (d *Driver) prepareAction(ctx context.Context, input Input, at covenant.UnixSeconds) (Step, error) {
	g := d.journal.game
	required, err := g.RequiredFunding(input)
	if err != nil {
		return Step{}, err
	}
	var funding covenant.Funding
	if required > 0 {
		if err := d.admitAgreement(); err != nil {
			return Step{}, err
		}
		funding, err = d.config.Wallet.SelectFunding(ctx, required)
		if err != nil {
			return Step{}, err
		}
	}
	if err := d.emit(ctx, nil, true, nil); err != nil {
		return Step{}, err
	}
	e, err := g.PrepareAction(ctx, d.config.Entropy, input, funding, at)
	return eventStep(e), err
}

func (d *Driver) peerRequest(role covenant.Player, sequence uint64) ports.PeerRequest {
	s := d.journal.game.setup
	request := ports.PeerRequest{SessionID: [32]byte(s.invitation.SessionID), Role: uint8(role), Sequence: sequence}
	if s.peer != nil {
		identity := s.peer.Identity
		request.PeerPublicKey = &identity
	}
	if s.peer == nil && s.role == covenant.Player2 {
		identity := s.invitation.CreatorTransportKey
		request.PeerPublicKey = &identity
	}
	return request
}
func (d *Driver) openTransport(ctx context.Context) error {
	s := d.journal.game.setup
	if s == nil || s.secrets == nil || s.secrets.state == nil {
		return ErrSecrets
	}
	p := ports.PeerSession{SessionID: [32]byte(s.invitation.SessionID), Role: uint8(s.role), RelayURL: s.invitation.RelayURL, PeerPublicKey: d.peerRequest(s.role, 0).PeerPublicKey}
	secrets := s.secrets.state
	secrets.mu.Lock()
	if secrets.destroyed {
		secrets.mu.Unlock()
		return ErrSecrets
	}
	p.TransportSecret = secrets.transportSecret
	secrets.mu.Unlock()
	defer clear(p.TransportSecret[:])
	// Open is idempotent for the same saved session; supplying the newly admitted
	// peer pins its identity without replacing the local key or subscription.
	return d.config.Transport.Open(ctx, p)
}
func (d *Driver) receiveMessage(ctx context.Context) (Step, error) {
	if err := d.openTransport(ctx); err != nil {
		return Step{}, err
	}
	g := d.journal.game
	s := g.setup
	wait, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	delivery, err := d.config.Transport.Receive(wait, d.peerRequest(other(s.role), s.nextReceive))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return Step{Kind: Waiting}, nil
		}
		return Step{}, err
	}
	if delivery == nil {
		return Step{Kind: Waiting}, nil
	}
	m, err := DecodeMessage(delivery.Payload)
	if err != nil {
		return d.abort("invalid_peer_evidence"), nil
	}
	if delivery.Identity != m.Identity {
		return Step{}, protocolError("authenticated peer identity")
	}
	var peer *[32]byte
	if s.peer != nil {
		peer = &s.peer.Identity
	}
	if err := checkMessageContext(s.invitation, m, other(s.role), peer); err != nil {
		return Step{}, err
	}
	if m.Kind == KeyOffer || m.Kind == KeyReply {
		digest, err := WalletOwnershipDigest(s.invitation, m)
		if err != nil {
			return d.abort("invalid_peer_evidence"), nil
		}
		if err := d.config.Wallet.AuthenticateOwnership(ctx, m.Participant.SigningKey, digest, m.WalletOwnership); err != nil {
			return Step{}, err
		}
	}
	e := g.NewEvent(MessageReceived)
	e.Message = &m
	if m.Kind == FinalShuffle {
		e.ObservedAt, err = d.now()
		if err != nil {
			return Step{}, err
		}
	}
	candidate := *g
	if err := candidate.ApplyContext(ctx, e); err != nil {
		if ctx.Err() != nil {
			return Step{}, ctx.Err()
		}
		if errors.Is(err, ErrDeadline) {
			return d.abort("initial_deadline_expired"), nil
		}
		return d.abort("invalid_peer_evidence"), nil
	}
	return eventStep(e), nil
}

func (d *Driver) admitAgreement() error {
	g := d.journal.game
	if g.setup == nil || g.setup.params == nil {
		return ErrInput
	}
	params := *g.setup.params
	params.Players.Player1.PayoutScript = bytes.Clone(params.Players.Player1.PayoutScript)
	params.Players.Player2.PayoutScript = bytes.Clone(params.Players.Player2.PayoutScript)
	return d.config.Wallet.AdmitAgreement(params)
}
