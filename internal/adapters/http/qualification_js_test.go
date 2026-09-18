//go:build js && wasm && qualification

package http

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	ws "arkade-poker/go/internal/adapters/websocket"
	"arkade-poker/go/internal/ports"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
)

type observedBody struct {
	io.ReadCloser
	mu   sync.Mutex
	data []byte
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	if len(b.data)+n <= 1<<20 {
		b.data = append(b.data, p[:n]...)
	}
	b.mu.Unlock()
	return n, err
}

func TestBrowserTransport(t *testing.T) {
	watchedScript, _ := hex.DecodeString("512079be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
	t.Run("live_services", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		a, _ := NewArkd("http://localhost:7070")
		defer a.Close()
		info, err := a.Info(ctx)
		if err != nil || info.Network != "regtest" {
			t.Fatal(info, err)
		}
		t.Log("Arkd", info.Version)
		e, _ := NewUnverifiedEmulator("http://localhost:7073")
		defer e.Close()
		emu, err := e.Info(ctx)
		if err != nil || emu.Signer == "" {
			t.Fatal(emu, err)
		}
		t.Log("Emulator", emu.Version)
		fixture, _ := New("http://127.0.0.1:5176")
		defer fixture.Close()
		var data struct {
			Bundle   ports.Bundle
			SkipKeys []string
		}
		if err := fixture.request(ctx, "GET", "/emulator-fixture.json", nil, &data); err != nil {
			t.Fatal(err)
		}
		var skipped []*btcec.PublicKey
		for _, encoded := range data.SkipKeys {
			raw, err := hex.DecodeString(encoded)
			if err != nil {
				t.Fatal(err)
			}
			key, err := schnorr.ParsePubKey(raw)
			if err != nil {
				t.Fatal(err)
			}
			skipped = append(skipped, key)
		}
		signed, err := e.Sign(ctx, data.Bundle)
		if err != nil {
			t.Fatal("browser emulator sign", err)
		}
		if len(signed.Checkpoints) != len(data.Bundle.Checkpoints) {
			t.Fatal("checkpoint count")
		}
		for _, encoded := range append([]string{signed.Ark}, signed.Checkpoints...) {
			packet, err := psbt.NewFromRawBytes(strings.NewReader(encoded), true)
			if err != nil {
				t.Fatal(err)
			}
			fetcher := txscript.NewMultiPrevOutFetcher(nil)
			for i, in := range packet.Inputs {
				fetcher.AddPrevOut(packet.UnsignedTx.TxIn[i].PreviousOutPoint, in.WitnessUtxo)
			}
			inputs, err := script.VerifyTapscriptSigs(packet, fetcher, script.WithSkipPublicKeys(skipped...))
			if err != nil || len(inputs) != 1 {
				t.Fatal("browser emulator signature", inputs, err)
			}
		}
		t.Log("browser emulator POST/CORS: main + checkpoint signatures verified on synthetic non-finalizing fixture")
	})
	for name, endpoint := range map[string]string{"live_subscription": "http://localhost:7070", "fragmented_subscription": "http://127.0.0.1:5177/fragment"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
			defer cancel()
			a, _ := NewIndexer(endpoint)
			defer a.Close()
			var body *observedBody
			a.client.Transport = streamRoundTrip(func(r *http.Request) (*http.Response, error) {
				resp, err := http.DefaultTransport.RoundTrip(r)
				if err == nil {
					body = &observedBody{ReadCloser: resp.Body}
					resp.Body = body
				}
				return resp, err
			})
			start := time.Now()
			sub, err := a.Subscribe(ctx, [][]byte{watchedScript})
			if err != nil {
				t.Fatal(err)
			}
			if event, err := sub.Next(ctx); err != nil || event.Kind != ports.ScriptAttached {
				t.Fatal(event, err)
			}
			if name == "fragmented_subscription" {
				event, err := sub.Next(ctx)
				if err != nil || event.Kind != ports.ScriptChanged || event.TxID != chainhash.Hash([32]byte{8}) || len(event.NewVtxos) != 1 || event.NewVtxos[0].Hash != chainhash.Hash([32]byte{9}) {
					t.Fatal(event, err)
				}
				if time.Since(start) < 31*time.Second {
					t.Fatal("late transaction delivered too soon")
				}
			} else {
				// Regtest's default heartbeat interval is 60 seconds.
				read, stop := context.WithTimeout(ctx, 65*time.Second)
				defer stop()
				for {
					_, err := sub.Next(read)
					if errors.Is(err, context.DeadlineExceeded) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := sub.Close(); err != nil {
				t.Fatal(err)
			}
			body.mu.Lock()
			heartbeats := bytes.Count(body.data, []byte("heartbeat"))
			size := len(body.data)
			body.mu.Unlock()
			if heartbeats == 0 {
				t.Fatal("no actual heartbeat frames observed")
			}
			t.Logf("healthy %.2fs, %d heartbeat frames, %d bytes; Close completed", time.Since(start).Seconds(), heartbeats, size)
			cancelled, stop := context.WithCancel(ctx)
			stop()
			if _, err := sub.Next(cancelled); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
	t.Run("websocket", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cancelled, stop := context.WithCancel(ctx)
		stop()
		if _, err := ws.Dial(cancelled, "ws://127.0.0.1:5177/ws/echo"); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		dial, stop := context.WithTimeout(ctx, 100*time.Millisecond)
		defer stop()
		began := time.Now()
		if c, err := ws.Dial(dial, "ws://127.0.0.1:5177/ws/dial"); err == nil {
			c.Close()
			t.Fatal("dial ignored deadline")
		}
		if time.Since(began) > time.Second {
			t.Fatal("dial teardown blocked caller deadline")
		}
		c, err := ws.Dial(ctx, "ws://127.0.0.1:5177/ws/echo")
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte(`["REQ","transport-qualification"]`)
		if err := c.Write(cancelled, []byte("must-not-send")); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if err := c.Write(ctx, bytes.Repeat([]byte{'x'}, ws.MaxMessage+1)); err == nil {
			t.Fatal("write limit ignored")
		}
		if err := c.Write(ctx, []byte{0xff}); err == nil {
			t.Fatal("invalid text admitted")
		}
		if err := c.Write(ctx, payload); err != nil {
			t.Fatal(err)
		}
		data, err := c.Read(ctx)
		if err != nil || !bytes.Equal(data, payload) {
			t.Fatal("text changed or cancelled write escaped", err)
		}
		pending := make(chan error, 1)
		go func() { _, err := c.Read(ctx); pending <- err }()
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-pending; err == nil {
			t.Fatal("Close failed to interrupt read")
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"binary", "large", "echo"} {
			c, err := ws.Dial(ctx, "ws://127.0.0.1:5177/ws/"+mode)
			if err != nil {
				t.Fatal(err)
			}
			read, stop := context.WithTimeout(ctx, 100*time.Millisecond)
			if mode != "echo" {
				stop()
				read, stop = context.WithTimeout(ctx, 5*time.Second)
			}
			if _, err := c.Read(read); err == nil {
				t.Fatal("read accepted " + mode)
			}
			stop()
			c.Close()
		}
		for range 3 {
			old, err := ws.Dial(ctx, "ws://127.0.0.1:5177/ws/slow-close")
			if err != nil {
				t.Fatal(err)
			}
			if err := old.Write(ctx, payload); err != nil {
				t.Fatal(err)
			}
			if data, err := old.Read(ctx); err != nil || !bytes.Equal(data, payload) {
				t.Fatal("slow-close fixture did not become ready", err)
			}
			pending := make(chan error, 1)
			go func() { _, err := old.Read(ctx); pending <- err }()
			// Let the browser reader enter its wait before closing the adapter.
			time.Sleep(20 * time.Millisecond)
			began := time.Now()
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-pending:
				if err == nil {
					t.Fatal("closed session kept reading")
				}
			case <-time.After(time.Second):
				t.Fatal("local reader waited for peer close")
			}
			if err := old.Write(ctx, payload); err == nil {
				t.Fatal("closed session accepted a write")
			}
			if err := old.Close(); err != nil {
				t.Fatal("repeated close", err)
			}
			fresh, err := ws.Dial(ctx, "ws://127.0.0.1:5177/ws/echo")
			if err != nil {
				t.Fatal(err)
			}
			if err := fresh.Write(ctx, payload); err != nil {
				t.Fatal(err)
			}
			if data, err := fresh.Read(ctx); err != nil || !bytes.Equal(data, payload) {
				t.Fatal("new session failed", err)
			}
			fresh.Close()
			if time.Since(began) > time.Second {
				t.Fatal("new session waited for old peer close")
			}
		}
		t.Log("text, UTF-8, input/output limits, dial/read/write cancellation, repeated Close and three fresh sessions before delayed peer closure passed")
	})
}
