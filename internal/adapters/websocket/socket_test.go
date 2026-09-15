//go:build !js

package websocket

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ws "github.com/coder/websocket"
)

func TestLimitsAndCloseOwnership(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dial" {
			<-r.Context().Done()
			return
		}
		c, err := ws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(MaxMessage)
		switch r.URL.Path {
		case "/binary":
			c.Write(r.Context(), ws.MessageBinary, []byte{1})
		case "/large":
			c.Write(r.Context(), ws.MessageText, bytes.Repeat([]byte{'x'}, MaxMessage+1))
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
	}))
	defer server.Close()
	base := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dial, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	if c, err := Dial(dial, base+"/dial"); err == nil {
		c.Close()
		t.Fatal("dial ignored deadline")
	}
	stop()
	for _, path := range []string{"/binary", "/large"} {
		c, err := Dial(ctx, base+path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Read(ctx); err == nil {
			t.Fatal("accepted invalid message", path)
		}
		c.Close()
	}
	c, err := Dial(ctx, base+"/echo")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if err := c.Write(cancelled, []byte("cancelled")); err == nil {
		t.Fatal("write ignored cancellation")
	}
	for _, data := range [][]byte{{0xff}, bytes.Repeat([]byte{'x'}, MaxMessage+1)} {
		if err := c.Write(ctx, data); err == nil {
			t.Fatal("invalid write accepted")
		}
	}
	if err := c.Write(ctx, []byte("only-message")); err != nil {
		t.Fatal(err)
	}
	if data, err := c.Read(ctx); err != nil || string(data) != "only-message" {
		t.Fatal("cancelled or invalid message escaped", err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.Read(ctx); done <- err }()
	for range 2 {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Close did not interrupt read")
		}
	case <-ctx.Done():
		t.Fatal("Close blocked reader")
	}
}

func TestTextAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := ws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		kind, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		c.Write(r.Context(), kind, data)
		c.Read(r.Context())
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Write(ctx, []byte(`["REQ","session"]`)); err != nil {
		t.Fatal(err)
	}
	data, err := c.Read(ctx)
	if err != nil || string(data) != `["REQ","session"]` {
		t.Fatal("text changed", err)
	}
	readCtx, stop := context.WithCancel(ctx)
	stop()
	if _, err := c.Read(readCtx); err == nil {
		t.Fatal("read ignored cancellation")
	}
}
