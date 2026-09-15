package game

import (
	"context"
	"errors"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

func (d *Driver) recover(ctx context.Context) error {
	for d.recovering {
		if err := ctx.Err(); err != nil {
			return err
		}
		g := d.journal.game
		if g.hand == nil && g.prepared == nil {
			d.recovering = false
			return nil
		}
		if err := d.ensureWatch(ctx); err != nil {
			return err
		}
		// The reference records private opening before observing later successors.
		if g.openingReady() {
			e, err := g.PrepareOpening(ctx, d.config.Entropy, g.hand.accepted, g.hand.observedAt)
			if err != nil {
				return err
			}
			if err := d.commit(ctx, e); err != nil {
				return err
			}
			continue
		}
		var checked sourceCheck
		var err error
		if g.prepared != nil {
			checked, err = d.inspectPrepared(ctx)
		} else {
			checked, err = d.inspectSource(ctx)
		}
		if err != nil {
			return err
		}
		if checked.event != nil {
			if err := d.commit(ctx, *checked.event); err != nil {
				return err
			}
			continue
		}
		if !checked.unspent {
			return nil
		} // Missing/pending/unavailable are never retry authority.
		if g.prepared == nil {
			d.recovering = false
			return nil
		}
		if g.prepared.saved.Signed == nil {
			// Persist-before-submit proves this unsigned work cannot have been sent.
			// Resume its first signing step; never replace an existing saved signature.
			d.recovering = false
			return nil
		}
		if d.retried {
			return nil
		}
		at, err := d.now()
		if err != nil {
			return err
		}
		if err := d.checkSigningTime(at); err != nil {
			return err
		}
		saved, err := g.PendingSpend()
		if err != nil {
			return err
		}
		d.retried = true // Set before entering the boundary; no loop can repeat it.
		result, submitErr := d.config.Wallet.RetrySaved(ctx, saved.Route, *saved.Signed)
		if submitErr != nil {
			e := g.NewEvent(SubmissionFailed)
			e.FailureCode = "retry_failed"
			if err := d.commit(ctx, e); err != nil {
				return errors.Join(submitErr, err)
			}
			return submitErr
		}
		e, err := d.submissionEvent(result, true, at)
		if err != nil {
			return err
		}
		if err := d.commit(ctx, e); err != nil {
			return err
		}
		// Query accepted evidence again, including every opponent response. If the
		// service acknowledgement precedes indexing, keep watching without resubmit.
	}
	return nil
}
func (d *Driver) submissionEvent(result ports.Submitted, recovery bool, at covenant.UnixSeconds) (Event, error) {
	g := d.journal.game
	id, err := chainhash.NewHashFromStr(result.TxID)
	if err != nil || result.TxID != id.String() || *id != g.prepared.built.Ark.UnsignedTx.TxHash() {
		return Event{}, protocolError("submission receipt identity")
	}
	e := g.NewEvent(SubmissionAttempted)
	e.ObservedAt = at
	e.Receipt = &SubmissionReceipt{Recovery: recovery, TxID: id}
	return e, nil
}
