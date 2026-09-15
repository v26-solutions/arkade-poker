//go:build !js && regtest

package grpc

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	gateway "arkade-poker/go/internal/adapters/http"
	"arkade-poker/go/internal/ports"
)

// This opt-in test reads the live regtest service. It neither signs nor spends.
func TestRegtestArkdGatewayParity(t *testing.T) {
	endpoint := os.Getenv("POKER_ARKD_URL")
	if endpoint == "" {
		endpoint = "http://localhost:7070"
	}
	native, err := NewArkd(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	web, err := gateway.NewArkd(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer web.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n, err := native.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w, err := web.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(n, w) {
		t.Fatalf("transport info differs: %+v / %+v", n, w)
	}
	if n.Network != "regtest" {
		t.Fatal("qualification must use regtest")
	}
	t.Logf("Arkd %s: native/gateway discovery agrees", n.Version)
}

func TestRegtestSubscriptionAttachment(t *testing.T) {
	endpoint := os.Getenv("POKER_ARKD_URL")
	if endpoint == "" {
		endpoint = "http://localhost:7070"
	}
	script, _ := hex.DecodeString("512079be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
	n, err := NewIndexer(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	w, err := gateway.NewIndexer(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for name, a := range map[string]ports.ScriptSubscriber{"native": n, "http": w} {
		t.Run(name, func(t *testing.T) {
			for range 2 {
				s, err := a.Subscribe(ctx, [][]byte{script})
				if err != nil {
					t.Fatal(err)
				}
				if event, err := s.Next(ctx); err != nil || event.Kind != ports.ScriptAttached {
					t.Fatal(event, err)
				}
				read, stop := context.WithTimeout(ctx, 20*time.Millisecond)
				_, err = s.Next(read)
				stop()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("unexpected public fixture activity", err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRegtestIndexerGatewayParity(t *testing.T) {
	endpoint := os.Getenv("POKER_ARKD_URL")
	if endpoint == "" {
		endpoint = "http://localhost:7070"
	}
	scriptHex := os.Getenv("POKER_INDEXER_TEST_SCRIPT")
	if scriptHex == "" {
		// Public generator x coordinate; read only, no key or spend operation.
		scriptHex = "512079be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	}
	script, err := hex.DecodeString(scriptHex)
	if err != nil {
		t.Fatal(err)
	}
	native, err := NewIndexer(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	web, err := gateway.NewIndexer(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer web.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	n, err := native.Vtxos(ctx, ports.VtxoQuery{Script: script})
	if err != nil {
		t.Fatal(err)
	}
	w, err := web.Vtxos(ctx, ports.VtxoQuery{Script: script})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(n, w) {
		t.Fatal("live indexer records differ")
	}
	for _, v := range n {
		ntx, err := native.Transaction(ctx, v.Outpoint.Hash)
		if err != nil {
			t.Fatal(err)
		}
		wtx, err := web.Transaction(ctx, v.Outpoint.Hash)
		if err != nil {
			t.Fatal(err)
		}
		if ntx == nil || wtx == nil || !reflect.DeepEqual(ntx, wtx) {
			t.Fatal("live indexed transaction differs or is missing")
		}
	}
	t.Logf("live native/gateway indexer agreement: %d records (no spends)", len(n))
}
