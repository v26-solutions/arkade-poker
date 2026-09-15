package subscription

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"arkade-poker/go/internal/adapters/indexdata"
	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

type testReceiver struct {
	ctx    context.Context
	frames chan indexdata.SubscriptionFrame
	closed atomic.Int32
}

func (r *testReceiver) Recv() (indexdata.SubscriptionFrame, error) {
	select {
	case <-r.ctx.Done():
		return indexdata.SubscriptionFrame{}, r.ctx.Err()
	case f, ok := <-r.frames:
		if !ok {
			return f, io.EOF
		}
		return f, nil
	}
}
func (r *testReceiver) Close() error { r.closed.Add(1); return nil }
func testScript() [][]byte {
	return [][]byte{append([]byte{0x51, 0x20}, bytes.Repeat([]byte{3}, 32)...)}
}

func TestAttachmentRetainsEarlyEventAndOwnsFilter(t *testing.T) {
	var owner Owner
	defer owner.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := &testReceiver{frames: make(chan indexdata.SubscriptionFrame)}
	opened := make(chan struct{})
	result := make(chan ports.ScriptSubscription, 1)
	scripts := testScript()
	go func() {
		s, err := owner.Subscribe(ctx, scripts, func(ctx context.Context, filter []string) (Receiver, error) {
			r.ctx = ctx
			close(opened)
			return r, nil
		})
		if err != nil {
			t.Error(err)
		}
		result <- s
	}()
	<-opened
	scripts[0][2] = 99
	select {
	case <-result:
		t.Fatal("quiet stream established attachment")
	default:
	}
	id := chainhash.Hash{7}
	r.frames <- indexdata.SubscriptionFrame{Event: &indexdata.TransactionEvent{TxID: id.String()}}
	s := <-result
	if s == nil {
		t.Fatal("missing subscription")
	}
	for _, kind := range []ports.ScriptEventKind{ports.ScriptAttached, ports.ScriptChanged} {
		event, err := s.Next(ctx)
		if err != nil || event.Kind != kind {
			t.Fatal(event, err)
		}
		if kind == ports.ScriptChanged && (event.TxID != id || !bytes.Equal(event.Script, testScript()[0])) {
			t.Fatal("early event/filter changed", event)
		}
	}
	readCtx, stop := context.WithCancel(ctx)
	stop()
	if _, err := s.Next(readCtx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// A cancelled individual read does not stop the live stream.
	r.frames <- indexdata.SubscriptionFrame{Heartbeat: true}
	for range 2 {
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if r.closed.Load() != 1 {
		t.Fatal("receiver close count", r.closed.Load())
	}
}

func TestAttachmentTimeoutAndOwnerShutdown(t *testing.T) {
	for _, mode := range []string{"timeout", "shutdown", "cancel", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			var owner Owner
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := &testReceiver{frames: make(chan indexdata.SubscriptionFrame, 1)}
			opened := make(chan struct{})
			result := make(chan error, 1)
			duration := time.Second
			if mode == "timeout" {
				duration = 10 * time.Millisecond
			}
			go func() {
				s, err := owner.subscribe(ctx, testScript(), func(ctx context.Context, _ []string) (Receiver, error) { r.ctx = ctx; close(opened); return r, nil }, duration)
				if s != nil {
					t.Error("failed attachment returned a stream")
				}
				result <- err
			}()
			<-opened
			switch mode {
			case "shutdown":
				owner.Close()
			case "cancel":
				cancel()
			case "invalid":
				r.frames <- indexdata.SubscriptionFrame{}
			}
			err := <-result
			if err == nil {
				t.Fatal("missing attachment failure")
			}
			if mode == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			owner.Close()
			if r.closed.Load() != 1 {
				t.Fatal("leaked receiver")
			}
			if _, err := owner.Subscribe(ctx, testScript(), nil); err == nil {
				t.Fatal("reopened closed owner")
			}
		})
	}
}

func TestLossExposesGapAheadOfBufferedEvents(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		var owner Owner
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		r := &testReceiver{frames: make(chan indexdata.SubscriptionFrame, QueueSize+2)}
		r.frames <- indexdata.SubscriptionFrame{Heartbeat: true}
		sub, err := owner.Subscribe(ctx, testScript(), func(ctx context.Context, _ []string) (Receiver, error) { r.ctx = ctx; return r, nil })
		if err != nil {
			t.Fatal(err)
		}
		s := sub.(*stream)
		if overflow {
			for range QueueSize + 1 {
				r.frames <- indexdata.SubscriptionFrame{Event: &indexdata.TransactionEvent{TxID: chainhash.Hash{1}.String()}}
			}
		} else {
			close(r.frames)
		}
		select {
		case <-s.done:
		case <-ctx.Done():
			t.Fatal("stream failed to terminate")
		}
		event, err := s.Next(ctx)
		if err != nil || event.Kind != ports.ScriptObservationGap {
			t.Fatal("loss hidden by buffered event", event, err)
		}
		_, err = s.Next(ctx)
		want := io.EOF
		if overflow {
			want = ErrOverflow
		}
		if !errors.Is(err, want) {
			t.Fatal(err)
		}
		owner.Close()
		cancel()
	}
}
