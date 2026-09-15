//go:build !js

package grpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"net"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	web "arkade-poker/go/internal/adapters/http"
	"arkade-poker/go/internal/adapters/indexdata"
	"arkade-poker/go/internal/ports"
	arkv1 "github.com/arkade-os/arkd/api-spec/protobuf/gen/ark/v1"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/meshapi/grpc-api-gateway/gateway"
	rpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type streamFixture struct {
	arkv1.UnimplementedIndexerServiceServer
	connections chan *fixtureConnection
}
type fixtureConnection struct {
	request *arkv1.GetSubscriptionRequest
	frames  chan *arkv1.GetSubscriptionResponse
	done    chan struct{}
}

func (s *streamFixture) GetSubscription(q *arkv1.GetSubscriptionRequest, stream arkv1.IndexerService_GetSubscriptionServer) error {
	c := &fixtureConnection{request: q, frames: make(chan *arkv1.GetSubscriptionResponse), done: make(chan struct{})}
	defer close(c.done)
	select {
	case s.connections <- c:
	case <-stream.Context().Done():
		return stream.Context().Err()
	}
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case f := <-c.frames:
			if f == nil {
				return status.Error(codes.Unavailable, "fixture disconnected")
			}
			if err := stream.Send(f); err != nil {
				return err
			}
		}
	}
}

type subscriberCloser interface {
	ports.ScriptSubscriber
	Close() error
}

func subscriptionFixtures(t *testing.T) (*streamFixture, map[string]subscriberCloser) {
	t.Helper()
	service := &streamFixture{connections: make(chan *fixtureConnection, 10)}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := rpc.NewServer()
	arkv1.RegisterIndexerServiceServer(server, service)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := rpc.NewClient(listener.Addr().String(), rpc.WithTransportCredentials(insecure.NewCredentials()), rpc.WithDefaultCallOptions(rpc.MaxCallRecvMsgSize(indexdata.MaxBytes+1024)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	mux := gateway.NewServeMux()
	arkv1.RegisterIndexerServiceHandler(context.Background(), mux, conn)
	h := httptest.NewServer(mux)
	t.Cleanup(h.Close)
	n, err := NewIndexer("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	w, err := web.NewIndexer(h.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return service, map[string]subscriberCloser{"native": n, "http": w}
}
func startedFrame() *arkv1.GetSubscriptionResponse {
	return &arkv1.GetSubscriptionResponse{Data: &arkv1.GetSubscriptionResponse_SubscriptionStarted{SubscriptionStarted: &arkv1.SubscriptionStartedEvent{SubscriptionId: "fixture-1"}}}
}
func changedFrame(script []byte) *arkv1.GetSubscriptionResponse {
	id := chainhash.Hash{3}.String()
	v := &arkv1.IndexerVtxo{Outpoint: &arkv1.IndexerOutpoint{Txid: chainhash.Hash{4}.String(), Vout: 2}, Script: hex.EncodeToString(script)}
	return &arkv1.GetSubscriptionResponse{Data: &arkv1.GetSubscriptionResponse_Event{Event: &arkv1.IndexerSubscriptionEvent{Txid: id, NewVtxos: []*arkv1.IndexerVtxo{v, v}, SpentVtxos: []*arkv1.IndexerVtxo{v}}}}
}

type subscribeResult struct {
	sub ports.ScriptSubscription
	err error
}

func beginSubscription(ctx context.Context, a ports.ScriptSubscriber, scripts [][]byte) <-chan subscribeResult {
	result := make(chan subscribeResult, 1)
	go func() { s, err := a.Subscribe(ctx, scripts); result <- subscribeResult{s, err} }()
	return result
}
func connection(t *testing.T, ctx context.Context, s *streamFixture) *fixtureConnection {
	t.Helper()
	select {
	case c := <-s.connections:
		return c
	case <-ctx.Done():
		t.Fatal("no connection", ctx.Err())
		return nil
	}
}
func sendFrame(t *testing.T, ctx context.Context, c *fixtureConnection, f *arkv1.GetSubscriptionResponse) {
	t.Helper()
	select {
	case c.frames <- f:
	case <-ctx.Done():
		t.Fatal("frame blocked", ctx.Err())
	case <-c.done:
		t.Fatal("stream closed before frame")
	}
}

func TestSubscriptionTransportParityAndReattachment(t *testing.T) {
	service, adapters := subscriptionFixtures(t)
	script := append([]byte{0x51, 0x20}, bytes.Repeat([]byte{3}, 32)...)
	var previous []ports.ScriptEvent
	for name, a := range adapters {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			pending := beginSubscription(ctx, a, [][]byte{script})
			c := connection(t, ctx, service)
			if c.request.SubscriptionId != "" || c.request.Filter == nil || c.request.Filter.Scripts == nil || !reflect.DeepEqual(c.request.Filter.Scripts.Add, []string{hex.EncodeToString(script)}) {
				t.Fatal("initial filter lost", c.request)
			}
			// Longer than the upstream retry helper's two-second synthetic READY.
			select {
			case r := <-pending:
				t.Fatal("unconfirmed attachment", r.err)
			case <-time.After(2100 * time.Millisecond):
			}
			sendFrame(t, ctx, c, changedFrame(script))
			r := <-pending
			if r.err != nil {
				t.Fatal(r.err)
			}
			var events []ports.ScriptEvent
			for range 2 {
				e, err := r.sub.Next(ctx)
				if err != nil {
					t.Fatal(err)
				}
				events = append(events, e)
			}
			if events[0].Kind != ports.ScriptAttached || events[1].Kind != ports.ScriptChanged || len(events[1].NewVtxos) != 1 || events[1].NewVtxos[0].Hash != chainhash.Hash([32]byte{4}) {
				t.Fatal(events)
			}
			if previous != nil && !reflect.DeepEqual(previous, events) {
				t.Fatal("transport hints differ")
			}
			previous = events
			sendFrame(t, ctx, c, nil)
			if e, err := r.sub.Next(ctx); err != nil || e.Kind != ports.ScriptObservationGap {
				t.Fatal("disconnect did not report gap", e, err)
			}
			if _, err := r.sub.Next(ctx); err == nil {
				t.Fatal("missing termination error")
			}
			for range 2 {
				if err := r.sub.Close(); err != nil {
					t.Fatal(err)
				}
			}
			pending = beginSubscription(ctx, a, [][]byte{script})
			c = connection(t, ctx, service)
			select {
			case <-pending:
				t.Fatal("reattached without a frame")
			default:
			}
			sendFrame(t, ctx, c, startedFrame())
			r = <-pending
			if r.err != nil {
				t.Fatal(r.err)
			}
			if e, err := r.sub.Next(ctx); err != nil || e.Kind != ports.ScriptAttached {
				t.Fatal(e, err)
			}
			for range 2 {
				if err := a.Close(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-c.done:
			case <-ctx.Done():
				t.Fatal("adapter shutdown leaked listener")
			}
			if _, err := a.Subscribe(ctx, [][]byte{script}); err == nil {
				t.Fatal("subscribe after shutdown")
			}
		})
	}
}

func TestSubscriptionTransportMalformedAndOversized(t *testing.T) {
	service, adapters := subscriptionFixtures(t)
	script := append([]byte{0x51, 0x20}, bytes.Repeat([]byte{3}, 32)...)
	for name, a := range adapters {
		for _, fault := range []string{"empty", "id", "point", "oversize", "cancel"} {
			t.Run(name+"/"+fault, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				pending := beginSubscription(ctx, a, [][]byte{script})
				c := connection(t, ctx, service)
				f := startedFrame()
				switch fault {
				case "empty":
					f = &arkv1.GetSubscriptionResponse{}
				case "id":
					f.GetSubscriptionStarted().SubscriptionId = "bad/id"
				case "point":
					f = changedFrame(script)
					f.GetEvent().NewVtxos[0].Outpoint = nil
				case "oversize":
					f = changedFrame(script)
					f.GetEvent().Tx = strings.Repeat("x", indexdata.MaxBytes+1)
				case "cancel":
					cancel()
				}
				if fault != "cancel" {
					sendFrame(t, ctx, c, f)
				}
				r := <-pending
				if r.err == nil || r.sub != nil {
					t.Fatal("failed attachment admitted", r.err)
				}
				select {
				case <-c.done:
				case <-time.After(time.Second):
					t.Fatal("failed attachment leaked listener")
				}
			})
		}
	}
}

func TestSubscriptionTransportQueuePressure(t *testing.T) {
	service, adapters := subscriptionFixtures(t)
	script := append([]byte{0x51, 0x20}, bytes.Repeat([]byte{3}, 32)...)
	for name, a := range adapters {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			pending := beginSubscription(ctx, a, [][]byte{script})
			c := connection(t, ctx, service)
			sendFrame(t, ctx, c, startedFrame())
			r := <-pending
			if r.err != nil {
				t.Fatal(r.err)
			}
			defer r.sub.Close()
			// Do not consume any hints. The receiver must terminate and cancel the
			// server even though no room remains in its event queue for a gap.
			for {
				select {
				case <-c.done:
					goto closed
				case c.frames <- changedFrame(script):
				case <-ctx.Done():
					t.Fatal("queue pressure did not cancel the stream")
				}
			}
		closed:
			if event, err := r.sub.Next(ctx); err != nil || event.Kind != ports.ScriptObservationGap {
				t.Fatal("overflow hidden", event, err)
			}
			if _, err := r.sub.Next(ctx); err == nil {
				t.Fatal("missing overflow error")
			}
		})
	}
}
