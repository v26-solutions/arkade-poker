package http

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"arkade-poker/go/internal/adapters/indexdata"
	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

type fragmentedReader struct {
	io.Reader
	size int
}

func (r fragmentedReader) Read(b []byte) (int, error) { return r.Reader.Read(b[:min(len(b), r.size)]) }

func TestSSEFragmentationAndProtoAliases(t *testing.T) {
	script := append([]byte{0x51, 0x20}, bytes.Repeat([]byte{3}, 32)...)
	filter, _ := indexdata.WatchScripts([][]byte{script})
	id := chainhash.Hash{3}.String()
	for _, ending := range []string{"\n", "\r\n", "\r"} {
		for _, size := range []int{1, 2, 7, 4096} {
			input := "\xef\xbb\xbf: comment\n\nevent: message\ndata: {\ndata: \"subscription_started\":{\"subscription_id\":\"live-1\"}}\n\n" +
				"data: {\"heartbeat\":{}}\n\ndata: {\"event\":{\"txid\":\"" + id + "\",\"new_vtxos\":[{\"outpoint\":{\"txid\":\"" + id + "\",\"vout\":2},\"script\":\"" + hex.EncodeToString(script) + "\"}]}}\n\n"
			s := newSSE(io.NopCloser(fragmentedReader{strings.NewReader(strings.ReplaceAll(input, "\n", ending)), size}), 4096)
			for i := range 3 {
				f, err := s.Recv()
				if err != nil {
					t.Fatal(ending, size, i, err)
				}
				events, err := f.Events(filter)
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 && f.Started != "live-1" || i == 1 && !f.Heartbeat || i == 2 && (len(events) != 1 || len(events[0].NewVtxos) != 1 || events[0].NewVtxos[0].Index != 2) {
					t.Fatal(f, events)
				}
			}
			if _, err := s.Recv(); !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
		}
	}
}

func TestSSERejectsMalformedAndBoundedFrames(t *testing.T) {
	for _, input := range []string{
		"data: {}\n\n", "data: null\n\n", "data: {\"heartbeat\":null}\n\n",
		"data: {\"heartbeat\":{},\"event\":{}}\n\n",
		"data: {\"subscriptionStarted\":{},\"subscription_started\":{}}\n\n",
		"data: {\"event\":{\"newVtxos\":[],\"new_vtxos\":[]}}\n\n",
		"data: {\"event\":{\"new_vtxos\":[{\"outpoint\":{\"txid\":\"a\",\"TXID\":\"b\"}}]}}\n\n",
		"data: {\"heartbeat\":{},\"error\":{}}\n\n",
		"event: error\ndata: {\"heartbeat\":{}}\n\n",
		"data: {\"heartbeat\":{}}\n", "data: {\"heartbeat\":{}}",
		"data: {\"heartbeat\":{}} {}\n\n", "data: {\"heartbeat\":{},\"x\":\"\xff\"}\n\n",
		strings.Repeat(": comment\n", 40) + "\n", ":" + strings.Repeat("a", 257) + "\n\n",
	} {
		s := newSSE(io.NopCloser(fragmentedReader{strings.NewReader(input), 1}), 256)
		if _, err := s.Recv(); err == nil {
			t.Fatalf("malformed/oversized stream accepted: %q", input)
		}
	}
}

type streamRoundTrip func(*http.Request) (*http.Response, error)

func (f streamRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestStreamLifetimeIsSeparateFromUnaryTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, err := NewIndexer("http://example.test")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.client.Timeout = time.Millisecond
	body, writer := io.Pipe()
	requestDone := make(chan struct{})
	a.client.Transport = streamRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Accept") != "text/event-stream" || r.URL.Path != "/v1/indexer/subscription" || len(r.URL.Query()["filter.scripts.add"]) != 1 {
			t.Error("incorrect stream request")
		}
		go func() { <-r.Context().Done(); close(requestDone) }()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream; charset=utf-8"}}, Body: body}, nil
	})
	go func() { writer.Write([]byte("data: {\"heartbeat\":{}}\n\n")) }()
	script := append([]byte{0x51, 0x20}, bytes.Repeat([]byte{3}, 32)...)
	s, err := a.Subscribe(ctx, [][]byte{script})
	if err != nil {
		t.Fatal(err)
	}
	if e, err := s.Next(ctx); err != nil || e.Kind != ports.ScriptAttached {
		t.Fatal(e, err)
	}
	// Waiting for a caller deadline beyond the unary timeout proves a healthy
	// attached stream has not inherited that whole-request lifetime.
	read, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if _, err := s.Next(read); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestDone:
	case <-ctx.Done():
		t.Fatal("request leaked")
	}
	writer.Close()
}
