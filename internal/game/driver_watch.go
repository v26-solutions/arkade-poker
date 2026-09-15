package game

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"time"

	"arkade-poker/go/internal/ports"
)

type watchFault struct{ err error }
type driverWatch struct {
	sub     ports.ScriptSubscription
	cancel  context.CancelFunc
	done    chan struct{}
	changed chan struct{}
	hints   chan ports.ScriptEvent
	gap     atomic.Bool
	fault   atomic.Pointer[watchFault]
}

func (d *Driver) ensureWatch(ctx context.Context) error {
	if d.watch != nil {
		if fault := d.watch.fault.Load(); fault != nil && errors.Is(fault.err, ErrProtocol) {
			return fault.err
		}
	}
	if d.watch != nil && !d.watch.gap.Load() && d.watch.fault.Load() == nil {
		return nil
	}
	_ = d.closeWatch()
	c, err := d.journal.game.AgreedContract()
	if err != nil {
		return err
	}
	script, err := c.ScriptPubKey()
	if err != nil {
		return err
	}
	live, cancel := context.WithCancel(d.lifetime)
	stop := context.AfterFunc(ctx, cancel)
	sub, err := d.config.Subscriptions.Subscribe(live, [][]byte{bytes.Clone(script)})
	stopped := stop()
	if err != nil || !stopped || ctx.Err() != nil {
		cancel()
		if sub != nil {
			_ = sub.Close()
		}
		if err != nil {
			return err
		}
		return ctx.Err()
	}
	if sub == nil {
		cancel()
		return ErrProtocol
	}
	w := &driverWatch{sub: sub, cancel: cancel, done: make(chan struct{}), changed: make(chan struct{}, 1), hints: make(chan ports.ScriptEvent, 64)}
	d.watch = w
	go func() {
		defer close(w.done)
		for {
			event, err := sub.Next(live)
			if err == nil && len(event.Script) > 0 && !bytes.Equal(event.Script, script) {
				err = protocolError("unrelated subscription script")
			}
			if err == nil {
				switch event.Kind {
				case ports.ScriptObservationGap:
					w.gap.Store(true)
				case ports.ScriptAttached:
				case ports.ScriptChanged:
					event.Script = bytes.Clone(event.Script)
					event.NewVtxos = slices.Clone(event.NewVtxos)
					event.SpentVtxos = slices.Clone(event.SpentVtxos)
					select {
					case w.hints <- event:
					default:
						// A bounded queue cannot silently lose source/deposit hints.
						// Reattach and reconcile instead of trusting an incomplete stream.
						w.gap.Store(true)
					}
				default:
					err = protocolError("subscription event kind")
				}
			}
			if err != nil {
				w.fault.Store(&watchFault{err})
			}
			select {
			case w.changed <- struct{}{}:
			default:
			}
			if err != nil {
				return
			}
		}
	}()
	return nil
}
func (d *Driver) closeWatch() error {
	if d.watch == nil {
		return nil
	}
	w := d.watch
	d.watch = nil
	w.cancel()
	err := w.sub.Close()
	<-w.done
	return err
}
func (d *Driver) watchSignal() <-chan struct{} {
	if d.watch == nil {
		return nil
	}
	return d.watch.changed
}

// waitHint consumes at most one transaction hint. The separate wakeup channel
// lets Run notice activity without discarding the transaction details.
func (d *Driver) waitHint(ctx context.Context, duration time.Duration) (*ports.ScriptEvent, error) {
	if err := d.ensureWatch(ctx); err != nil {
		return nil, err
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	var hint *ports.ScriptEvent
	select {
	case e := <-d.watch.hints:
		hint = &e
	default:
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case e := <-d.watch.hints:
			hint = &e
		case <-d.watch.changed:
		case <-timer.C:
		}
	}
	if hint == nil {
		select {
		case e := <-d.watch.hints:
			hint = &e
		default:
		}
	}
	// A sticky gap is cleared only by a fresh confirmed subscription. Callers
	// reconcile even when the old stream's hint must be discarded.
	if d.watch.gap.Load() || d.watch.fault.Load() != nil {
		return nil, d.ensureWatch(ctx)
	}
	return hint, nil
}
