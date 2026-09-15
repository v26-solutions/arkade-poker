//go:build !js

package game

import (
	"context"
	"net"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	native "arkade-poker/go/internal/adapters/grpc"
	web "arkade-poker/go/internal/adapters/http"
	"arkade-poker/go/internal/ports"
	arkv1 "github.com/arkade-os/arkd/api-spec/protobuf/gen/ark/v1"
	"github.com/meshapi/grpc-api-gateway/gateway"
	rpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type driverStreamServer struct {
	arkv1.UnimplementedIndexerServiceServer
	connected chan chan bool
}

func (s *driverStreamServer) GetSubscription(_ *arkv1.GetSubscriptionRequest, stream arkv1.IndexerService_GetSubscriptionServer) error {
	control := make(chan bool)
	select {
	case s.connected <- control:
	case <-stream.Context().Done():
		return stream.Context().Err()
	}
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case attach := <-control:
			if !attach {
				return status.Error(codes.Unavailable, "disconnect fixture")
			}
			if err := stream.Send(&arkv1.GetSubscriptionResponse{Data: &arkv1.GetSubscriptionResponse_SubscriptionStarted{SubscriptionStarted: &arkv1.SubscriptionStartedEvent{SubscriptionId: "driver-watch"}}}); err != nil {
				return err
			}
		}
	}
}

// The marker is used only by the existing wallet fixture's watch-before-effect
// assertions. All attachment, reads, cancellation and gaps use the real adapter.
type trackedAdapter struct {
	ports.ScriptSubscriber
	marker fixtureSubscriber
}
type trackedStream struct {
	ports.ScriptSubscription
	marker ports.ScriptSubscription
}

func (a trackedAdapter) Subscribe(ctx context.Context, scripts [][]byte) (ports.ScriptSubscription, error) {
	s, err := a.ScriptSubscriber.Subscribe(ctx, scripts)
	if err != nil {
		return nil, err
	}
	m, err := a.marker.Subscribe(ctx, scripts)
	if err != nil {
		s.Close()
		return nil, err
	}
	return trackedStream{s, m}, nil
}
func (s trackedStream) Close() error {
	err := s.ScriptSubscription.Close()
	s.marker.Close()
	return err
}

func TestDriverActualAdaptersReconcileBeforeEffects(t *testing.T) {
	server := &driverStreamServer{connected: make(chan chan bool, 4)}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpcServer := rpc.NewServer()
	arkv1.RegisterIndexerServiceServer(rpcServer, server)
	go rpcServer.Serve(listener)
	defer rpcServer.Stop()
	conn, err := rpc.NewClient(listener.Addr().String(), rpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	mux := gateway.NewServeMux()
	arkv1.RegisterIndexerServiceHandler(context.Background(), mux, conn)
	h := httptest.NewServer(mux)
	defer h.Close()
	n, err := native.NewIndexer("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	w, err := web.NewIndexer(h.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for name, adapter := range map[string]ports.ScriptSubscriber{"native": n, "http": w} {
		t.Run(name, func(t *testing.T) {
			f := newDriverFixture(t)
			f.h.start(0)
			f.savePending(t, 0, Input{Kind: Concede}, false)
			f.importHistory(t)
			d, wallet, _, _ := f.newDriver(t, 0, f.h.logs[0])
			d.config.Subscriptions = trackedAdapter{adapter, fixtureSubscriber{f, 0}}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			nextConnection := func() chan bool {
				select {
				case c := <-server.connected:
					return c
				case <-ctx.Done():
					t.Fatal("no fresh attachment", ctx.Err())
					return nil
				}
			}
			send := func(c chan bool, value bool) {
				select {
				case c <- value:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			done := make(chan error, 1)
			go func() { done <- d.Restore(ctx) }()
			control := nextConnection()
			select {
			case err := <-done:
				t.Fatal("restore bypassed confirmed attachment", err)
			default:
			}
			send(control, true)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			old := d.watch
			send(control, false)
			for !old.gap.Load() {
				select {
				case <-old.changed:
				case <-ctx.Done():
					t.Fatal("missing sticky gap")
				}
			}
			f.mu.Lock()
			f.trace = nil
			f.mu.Unlock()
			var step Step
			go func() { var err error; step, err = d.step(ctx, Input{Kind: Progress}); done <- err }()
			control = nextConnection()
			select {
			case err := <-done:
				t.Fatal("signing bypassed reattachment", err)
			default:
			}
			f.mu.Lock()
			calls := wallet.signCalls
			f.mu.Unlock()
			if calls != 0 {
				t.Fatal("signed during attachment")
			}
			send(control, true)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if step.Event == nil || step.Event.Kind != SpendSigned || d.watch == old || d.watch.gap.Load() {
				t.Fatal("fresh stream did not resume signing")
			}
			f.mu.Lock()
			trace := slices.Clone(f.trace)
			f.mu.Unlock()
			attach, query, sign := slices.Index(trace, "attach:0"), slices.Index(trace, "query"), slices.Index(trace, "sign:0")
			if attach < 0 || query <= attach || sign <= query {
				t.Fatal("missing attach/query/sign order", trace)
			}
			if err := d.commit(ctx, *step.Event); err != nil {
				t.Fatal(err)
			}
			current := d.watch
			send(control, false)
			for !current.gap.Load() {
				select {
				case <-current.changed:
				case <-ctx.Done():
					t.Fatal("missing second gap")
				}
			}
			go func() { _, err := d.step(ctx, Input{Kind: Progress}); done <- err }()
			control = nextConnection()
			send(control, false)
			if err := <-done; err == nil || wallet.submitCalls != 0 {
				t.Fatal("submitted after failed attachment", err)
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
