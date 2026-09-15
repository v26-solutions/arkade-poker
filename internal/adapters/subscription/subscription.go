// Package subscription owns the common bounded streaming lifetime. A failed
// stream reports a gap then its error; the game driver owns fresh attachment
// and reconciliation. There is no hidden reconnect/retry loop in an adapter.
package subscription

import (
	"context"
	"errors"
	"sync"
	"time"

	"arkade-poker/go/internal/adapters/indexdata"
	"arkade-poker/go/internal/ports"
)

const AttachTimeout = 30 * time.Second
const QueueSize = 64

var ErrOverflow = errors.New("subscription observation queue overflow")

// Receiver must unblock Recv when the Open context is cancelled. Close releases
// its body/connection; it is called exactly once by the stream owner.
type Receiver interface {
	Recv() (indexdata.SubscriptionFrame, error)
	Close() error
}
type Open func(context.Context, []string) (Receiver, error)

// Owner's zero value is ready to use. Shutdown includes pending attachments and
// waits for every receiver to release its resources. No Subscribe may follow it.
type Owner struct {
	mu     sync.Mutex
	closed bool
	live   map[*stream]struct{}
	wg     sync.WaitGroup
}

func (o *Owner) Subscribe(ctx context.Context, scripts [][]byte, open Open) (ports.ScriptSubscription, error) {
	return o.subscribe(ctx, scripts, open, AttachTimeout)
}

func (o *Owner) subscribe(ctx context.Context, scripts [][]byte, open Open, timeout time.Duration) (ports.ScriptSubscription, error) {
	filter, err := indexdata.WatchScripts(scripts)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	live, cancel := context.WithCancelCause(ctx)
	s := &stream{ctx: live, cancel: cancel, ready: make(chan struct{}), done: make(chan struct{}), events: make(chan ports.ScriptEvent, QueueSize)}
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		cancel(context.Canceled)
		return nil, context.Canceled
	}
	if o.live == nil {
		o.live = make(map[*stream]struct{})
	}
	o.live[s] = struct{}{}
	o.wg.Add(1)
	o.mu.Unlock()
	timer := time.AfterFunc(timeout, func() { cancel(context.DeadlineExceeded) })
	go func() {
		defer o.wg.Done()
		defer func() { o.mu.Lock(); delete(o.live, s); o.mu.Unlock() }()
		defer close(s.done)
		defer cancel(context.Canceled)
		defer timer.Stop()
		s.err = s.run(open, filter, timer)
	}()
	select {
	case <-s.ready:
		if cause := context.Cause(live); cause != nil {
			_ = s.Close()
			return nil, cause
		}
		return s, nil
	case <-s.done:
		return nil, s.err
	case <-live.Done():
		cause := context.Cause(live)
		_ = s.Close()
		return nil, cause
	}
}

func (o *Owner) Close() error {
	o.mu.Lock()
	o.closed = true
	for s := range o.live {
		s.cancel(context.Canceled)
	}
	o.mu.Unlock()
	o.wg.Wait()
	return nil
}

type stream struct {
	ctx         context.Context
	cancel      context.CancelCauseFunc
	ready, done chan struct{}
	events      chan ports.ScriptEvent
	err         error // written before done closes
	next        sync.Mutex
	gapSent     bool
}

func (s *stream) run(open Open, filter []string, timer *time.Timer) error {
	r, err := open(s.ctx, filter)
	if err != nil {
		return s.cause(err)
	}
	if r == nil {
		return errors.New("missing subscription receiver")
	}
	defer r.Close()
	attached := false
	for {
		frame, err := r.Recv()
		if err != nil {
			return s.cause(err)
		}
		events, err := frame.Events(filter)
		if err != nil {
			return err
		}
		if !attached {
			if !timer.Stop() {
				return context.DeadlineExceeded
			}
			if err := s.ctx.Err(); err != nil {
				return s.cause(err)
			}
			s.events <- ports.ScriptEvent{Kind: ports.ScriptAttached}
		}
		for _, event := range events {
			select {
			case s.events <- event:
			default:
				return ErrOverflow
			}
		}
		if !attached {
			attached = true
			close(s.ready)
		}
	}
}

func (s *stream) cause(err error) error {
	if cause := context.Cause(s.ctx); cause != nil {
		return cause
	}
	return err
}

func (s *stream) Next(ctx context.Context) (ports.ScriptEvent, error) {
	s.next.Lock()
	defer s.next.Unlock()
	if err := ctx.Err(); err != nil {
		return ports.ScriptEvent{}, err
	}
	// Failure takes priority over stale queued hints. A gap remains observable
	// even when the queue was full; the next read exposes the terminal error.
	terminated := func() (ports.ScriptEvent, error) {
		if !s.gapSent {
			s.gapSent = true
			return ports.ScriptEvent{Kind: ports.ScriptObservationGap}, nil
		}
		return ports.ScriptEvent{}, s.err
	}
	select {
	case <-s.done:
		return terminated()
	default:
	}
	select {
	case <-ctx.Done():
		return ports.ScriptEvent{}, ctx.Err()
	case <-s.done:
		return terminated()
	case event := <-s.events:
		select {
		case <-s.done:
			return terminated()
		default:
			return event, nil
		}
	}
}

func (s *stream) Close() error { s.cancel(context.Canceled); <-s.done; return nil }
