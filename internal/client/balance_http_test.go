//go:build !js

package client

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adapter "arkade-poker/go/internal/adapters/http"
	"arkade-poker/go/internal/wallet"
)

func TestWalletBalanceRefetchesOnSSE(t *testing.T) {
	script := append([]byte{0x51, 0x20}, bytes.Repeat([]byte{3}, 32)...)
	encoded := hex.EncodeToString(script)
	id := strings.Repeat("01", 32)
	var amount atomic.Int64
	amount.Store(1000)
	var queries atomic.Int32
	events, closed := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/indexer/subscription":
			defer close(closed)
			if r.Header.Get("Accept") != "text/event-stream" || r.URL.Query().Get("filter.scripts.add") != encoded {
				t.Error("missing default receive script SSE subscription")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"subscriptionStarted\":{\"subscriptionId\":\"wallet\"}}\n\n")
			w.(http.Flusher).Flush()
			for {
				select {
				case <-events:
					fmt.Fprintf(w, "data: {\"event\":{\"txid\":%q,\"newVtxos\":[{\"outpoint\":{\"txid\":%q},\"script\":%q}]}}\n\n", id, id, encoded)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		case "/v1/indexer/vtxos":
			queries.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"vtxos":[{"outpoint":{"txid":%q},"script":%q,"amount":"%d","expiresAt":"2000"}],"page":{"current":1,"next":1,"total":1}}`, id, encoded, amount.Load())
		default:
			t.Error("unexpected request", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	indexer, err := adapter.NewIndexer(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer indexer.Close()
	updates := make(chan BalanceUpdate, 1)
	stop := startBalance(context.Background(), wallet.Services{Indexer: indexer, Now: func() time.Time { return time.Unix(1000, 0) }}, indexer, script, updates)
	defer stop()
	if u := awaitBalance(t, updates); u.Err != nil || u.Sats != 1000 {
		t.Fatal("initial HTTP balance", u)
	}
	amount.Store(1234567)
	select {
	case events <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("SSE event timeout")
	}
	if u := awaitBalance(t, updates); u.Err != nil || u.Sats != 1234567 || queries.Load() != 2 {
		t.Fatal("SSE did not refetch balance", u, queries.Load())
	}
	stop()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("wallet SSE request leaked")
	}
}
