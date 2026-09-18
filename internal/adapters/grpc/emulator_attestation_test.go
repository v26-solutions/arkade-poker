//go:build !js

package grpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	web "arkade-poker/go/internal/adapters/http"
	"arkade-poker/go/internal/ports"
)

// Exercise the real enclave client through both adapters. An invalid attestation
// must prevent discovery and signing, with no fallback to an unverified client.
func TestEmulatorRequiresAttestation(t *testing.T) {
	constructors := map[string]func(context.Context, string, string) (ports.Emulator, error){
		"grpc": func(ctx context.Context, endpoint, pcr0 string) (ports.Emulator, error) {
			return NewEmulator(ctx, endpoint, pcr0)
		},
		"https": func(_ context.Context, endpoint, pcr0 string) (ports.Emulator, error) {
			return web.NewEmulator(endpoint, pcr0)
		},
	}
	for name, newEmulator := range constructors {
		t.Run(name, func(t *testing.T) {
			var attestations, requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/enclave/attestation" {
					attestations.Add(1)
					if r.URL.Query().Get("nonce") == "" {
						t.Error("enclave client did not request a nonce")
					}
					_, _ = w.Write([]byte(`{"document":"Zm9yZ2Vk"}`))
					return
				}
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			ctx := context.Background()
			pcr0 := strings.Repeat("ab", 48)
			for _, endpoint := range []string{"http://localhost:7073", "localhost:7073"} {
				if _, err := newEmulator(ctx, endpoint, pcr0); err == nil {
					t.Fatalf("accepted non-HTTPS endpoint %s", endpoint)
				}
			}
			if _, err := newEmulator(ctx, server.URL, ""); err == nil {
				t.Fatal("missing PCR0 did not reach enclave client validation")
			}
			emu, err := newEmulator(ctx, server.URL, pcr0)
			if err == nil {
				defer emu.Close()
				if _, err := emu.Info(ctx); err == nil {
					t.Fatal("discovery accepted invalid attestation")
				}
				if _, err := emu.Sign(ctx, ports.Bundle{Ark: "must-not-be-sent"}); err == nil {
					t.Fatal("signing accepted invalid attestation")
				}
			}
			if attestations.Load() == 0 || requests.Load() != 0 {
				t.Fatalf("attestation requests = %d, application requests = %d", attestations.Load(), requests.Load())
			}
		})
	}
}
