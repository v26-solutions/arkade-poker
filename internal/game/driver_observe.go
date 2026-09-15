package game

import (
	"bytes"
	"context"
	"slices"
	"time"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	"arkade-poker/go/internal/wallet"
	"github.com/btcsuite/btcd/wire"
)

type sourceCheck struct {
	event   *Event
	unspent bool
}

func (d *Driver) acceptedEvent(ctx context.Context, a *wallet.AcceptedTransaction) (*Event, error) {
	if a == nil || a.Transaction == nil {
		return nil, ErrProtocol
	}
	g := d.journal.game
	e := g.NewEvent(SpendObserved)
	if g.hand == nil {
		e.Kind = DepositObserved
	}
	e.Accepted = &AcceptedTransaction{Transaction: a.Transaction, Checkpoints: a.Checkpoints}
	var err error
	e.ObservedAt, err = d.now()
	if err != nil {
		return nil, err
	}
	// The journal will repeat admission before recording this exact fact.
	candidate := *g
	if err := candidate.ApplyContext(ctx, e); err != nil {
		return nil, err
	}
	return &e, nil
}
func (d *Driver) inspectSource(ctx context.Context) (sourceCheck, error) {
	g := d.journal.game
	if g.hand == nil {
		return sourceCheck{}, ErrInput
	}
	source := wire.OutPoint{Hash: g.hand.accepted.Transaction.TxHash()}
	observed, err := wallet.InspectSpend(ctx, d.config.Indexer, source)
	if err != nil {
		return sourceCheck{}, err
	}
	if observed.State == wallet.SourceAccepted {
		e, err := d.acceptedEvent(ctx, observed.Accepted)
		return sourceCheck{event: e}, err
	}
	return sourceCheck{unspent: observed.State == wallet.SourceUnspent}, nil
}
func (d *Driver) inspectPrepared(ctx context.Context) (sourceCheck, error) {
	g := d.journal.game
	if g.prepared == nil {
		return sourceCheck{}, ErrInput
	}
	p := g.prepared
	id := p.built.Ark.UnsignedTx.TxHash()
	accepted, err := wallet.Accepted(ctx, d.config.Indexer, id)
	if err != nil {
		return sourceCheck{}, err
	}
	if accepted != nil {
		e, err := d.acceptedEvent(ctx, accepted)
		return sourceCheck{event: e}, err
	}
	result := sourceCheck{unspent: true}
	if p.saved.Source != nil {
		result, err = d.inspectSource(ctx)
		if err != nil || result.event != nil {
			return result, err
		}
	}
	for _, source := range p.saved.WalletSources {
		observed, err := wallet.InspectSpend(ctx, d.config.Indexer, source)
		if err != nil {
			return sourceCheck{}, err
		}
		if observed.State == wallet.SourceAccepted {
			if observed.Accepted == nil || observed.Accepted.Transaction == nil {
				return sourceCheck{}, ErrProtocol
			}
			if observed.Accepted.Transaction.TxHash() != id {
				return sourceCheck{}, protocolError("funding source already spent")
			}
			e, err := d.acceptedEvent(ctx, observed.Accepted)
			return sourceCheck{event: e}, err
		}
		result.unspent = result.unspent && observed.State == wallet.SourceUnspent
	}
	return result, nil
}
func (d *Driver) discoverDeposit(ctx context.Context) (*Event, error) {
	g := d.journal.game
	if g.stage != StageAwaitInitialDeposit {
		return nil, ErrInput
	}
	script, err := g.setup.contract.ScriptPubKey()
	if err != nil {
		return nil, err
	}
	accepted, err := wallet.AcceptedByScript(ctx, d.config.Indexer, script)
	if err != nil {
		return nil, err
	}
	var found *wallet.AcceptedTransaction
	var exact []byte
	for _, a := range accepted {
		if a == nil || a.Transaction == nil {
			return nil, ErrProtocol
		}
		if err := g.checkDepositPacket(a.Transaction); err != nil {
			continue
		}
		v := AcceptedTransaction{Transaction: a.Transaction, Checkpoints: a.Checkpoints}
		if err := g.checkEdges(v, nil); err != nil {
			return nil, err
		}
		w := new(encoder)
		encodeAccepted(w, v)
		b, err := w.finish()
		if err != nil {
			return nil, err
		}
		if found != nil && !bytes.Equal(exact, b) {
			return nil, protocolError("ambiguous initial deposits")
		}
		found = a
		exact = b
	}
	if found == nil {
		return nil, nil
	}
	return d.acceptedEvent(ctx, found)
}
func (d *Driver) waitForSource(ctx context.Context) (*Event, error) {
	if err := d.ensureWatch(ctx); err != nil {
		return nil, err
	}
	checked, err := d.inspectSource(ctx)
	if err != nil || checked.event != nil {
		return checked.event, err
	}
	state, err := covenant.ReadState(d.journal.game.hand.accepted.Transaction)
	if err != nil {
		return nil, err
	}
	at, err := d.now()
	if err != nil {
		return nil, err
	}
	if at >= state.Deadline {
		return nil, nil
	}
	hint, err := d.waitHint(ctx, 250*time.Millisecond)
	if err != nil {
		return nil, err
	}
	source := wire.OutPoint{Hash: d.journal.game.hand.accepted.Transaction.TxHash()}
	if hint != nil && slices.Contains(hint.SpentVtxos, source) {
		accepted, err := wallet.Accepted(ctx, d.config.Indexer, hint.TxID)
		if err != nil {
			return nil, err
		}
		if accepted != nil {
			script, err := d.journal.game.setup.contract.ScriptPubKey()
			if err != nil {
				return nil, err
			}
			if bytes.Equal(accepted.Transaction.TxOut[0].PkScript, script) && !slices.Contains(hint.NewVtxos, wire.OutPoint{Hash: hint.TxID}) {
				return nil, protocolError("script event continuation outpoint")
			}
			return d.acceptedEvent(ctx, accepted)
		}
	}
	checked, err = d.inspectSource(ctx)
	return checked.event, err
}
func (d *Driver) checkSigningTime(at covenant.UnixSeconds) error {
	g := d.journal.game
	if g.prepared == nil {
		return ErrInput
	}
	switch g.prepared.saved.Action.Kind {
	case ActionInitialDeposit, ActionPlayer1Funding:
		return deadlineWindow(at, g.setup.params.InitialDeadline)
	case ActionTimeout:
		state, err := covenant.ReadState(g.hand.accepted.Transaction)
		if err != nil {
			return err
		}
		if at < state.Deadline {
			return protocolError("timeout is not mature")
		}
	}
	return nil
}

// The reference checks a matching transaction hint after both broad discovery
// queries, before expiry. A notification still requires finalized indexer
// evidence and the same source/checkpoint/main poker admission.
func (d *Driver) hintedDeposit(ctx context.Context, hint *ports.ScriptEvent) (*Event, error) {
	if hint == nil || !slices.Contains(hint.NewVtxos, wire.OutPoint{Hash: hint.TxID}) {
		return nil, nil
	}
	accepted, err := wallet.Accepted(ctx, d.config.Indexer, hint.TxID)
	if err != nil || accepted == nil {
		return nil, err
	}
	if d.journal.game.checkDepositPacket(accepted.Transaction) != nil {
		return nil, nil
	}
	return d.acceptedEvent(ctx, accepted)
}
