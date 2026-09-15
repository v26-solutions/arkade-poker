//go:build !js

// Local, public-data-only browser transport qualification fixture.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	adapter "arkade-poker/go/internal/adapters/websocket"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/coder/websocket"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	directory := "build/transport-qualification"
	var active atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"active":%d}`, active.Load()) })
	mux.HandleFunc("/fragment/v1/indexer/subscription", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" || len(r.URL.Query()["filter.scripts.add"]) != 1 {
			http.Error(w, "filter", 400)
			return
		}
		active.Add(1)
		defer active.Add(-1)
		w.Header().Set("Content-Type", "text/event-stream")
		flush := w.(http.Flusher)
		// Deliberate fragmented lines, UTF-8 BOM and CRLF boundaries.
		for _, part := range []string{"\xef\xbb\xbf: fixture\r", "\n\r\ndata: {\"subscription_", "started\":{\"subscription_id\":\"browser-1\"}}\r", "\n\r\n"} {
			if _, err := io.WriteString(w, part); err != nil {
				return
			}
			flush.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		after := time.NewTimer(32 * time.Second)
		defer after.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := io.WriteString(w, "data: {\"heartbeat\":{}}\n\n"); err != nil {
					return
				}
				flush.Flush()
			case <-after.C:
				event := map[string]any{"event": map[string]any{"txid": chainhash.Hash{8}.String(), "newVtxos": []any{map[string]any{"outpoint": map[string]any{"txid": chainhash.Hash{9}.String(), "vout": 2}, "script": r.URL.Query().Get("filter.scripts.add")}}}}
				data, _ := json.Marshal(event)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flush.Flush()
			}
		}
	})
	mux.HandleFunc("/ws/", func(w http.ResponseWriter, r *http.Request) {
		active.Add(1)
		defer active.Add(-1)
		if r.URL.Path == "/ws/dial" {
			select {
			case <-r.Context().Done():
			case <-time.After(500 * time.Millisecond):
				http.Error(w, "delayed upgrade fixture", 408)
			}
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"127.0.0.1:5176"}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(adapter.MaxMessage)
		switch r.URL.Path {
		case "/ws/slow-close":
			// Echo once, then leave close frames unread while the client starts
			// another session. Local teardown must not wait for this peer.
			kind, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if err := c.Write(r.Context(), kind, data); err != nil {
				return
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		case "/ws/binary":
			c.Write(r.Context(), websocket.MessageBinary, []byte{1, 2, 3})
		case "/ws/large":
			c.Write(r.Context(), websocket.MessageText, []byte(strings.Repeat("x", adapter.MaxMessage+1)))
		default:
			for {
				kind, data, err := c.Read(r.Context())
				if err != nil {
					return
				}
				if err := c.Write(r.Context(), kind, data); err != nil {
					return
				}
			}
		}
		c.Read(r.Context())
	})
	fixture := &http.Server{Addr: "127.0.0.1:5177", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == "http://127.0.0.1:5176" {
			w.Header().Set("Access-Control-Allow-Origin", "http://127.0.0.1:5176")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		mux.ServeHTTP(w, r)
	})}
	pageMux := http.NewServeMux()
	pageMux.HandleFunc("/qualification-result", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, 65537))
		if err != nil || len(data) > 65536 || !json.Valid(data) {
			w.WriteHeader(400)
			return
		}
		if err := os.WriteFile(filepath.Join(directory, "brave-result.json"), data, 0600); err != nil {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	})
	pageMux.Handle("/", http.FileServer(http.Dir(directory)))
	page := &http.Server{Addr: "127.0.0.1:5176", Handler: pageMux}
	go func() {
		if err := fixture.ListenAndServe(); err != http.ErrServerClosed {
			log.Print(err)
			stop()
		}
	}()
	go func() {
		if err := page.ListenAndServe(); err != http.ErrServerClosed {
			log.Print(err)
			stop()
		}
	}()
	fmt.Println("Transport qualification: http://127.0.0.1:5176")
	<-ctx.Done()
	page.Close()
	fixture.Close()
}
