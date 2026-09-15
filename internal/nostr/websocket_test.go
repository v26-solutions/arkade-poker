//go:build !js

package nostr

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adapter "arkade-poker/go/internal/adapters/websocket"
	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	"github.com/coder/websocket"
)

func TestActualWebSocketStoredDeliveryAndSavedPublication(t *testing.T) {
	local, peer := testKey(t, 1), testKey(t, 2)
	session := ports.PeerSession{SessionID: [32]byte{71}, Role: uint8(covenant.Player1)}
	session.TransportSecret[31] = 1
	identity := testPublic(t, peer)
	session.PeerPublicKey = &identity
	first := testEvent(t, peer, testPublic(t, local), session.SessionID, uint8(covenant.Player2), 0)
	second := testEvent(t, peer, testPublic(t, local), session.SessionID, uint8(covenant.Player2), 1)
	var active atomic.Int32
	publications := make(chan []byte, 4)
	errors := make(chan error, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			errors <- err
			return
		}
		active.Add(1)
		defer active.Add(-1)
		defer conn.CloseNow()
		conn.SetReadLimit(MaxCarrierBytes)
		ctx := r.Context()
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}
			fields, err := relayFields(raw)
			if err != nil {
				errors <- err
				return
			}
			switch stringValue(fields[0]) {
			case "REQ":
				for _, frame := range [][]byte{relayEvent(session.SessionID, second), relayEvent(session.SessionID, first), []byte(`["EOSE","fixture"]`)} {
					if err := conn.Write(ctx, websocket.MessageText, frame); err != nil {
						errors <- err
						return
					}
				}
			case "EVENT":
				event, err := DecodeEvent(fields[1])
				if err != nil {
					errors <- err
					return
				}
				if _, err := Authenticate(event); err != nil {
					errors <- err
					return
				}
				publications <- bytes.Clone(raw)
				if err := conn.Write(ctx, websocket.MessageText, ack(event.ID, true)); err != nil {
					errors <- err
					return
				}
			default:
				t.Errorf("unexpected relay request")
			}
		}
	}))
	defer server.Close()
	session.RelayURL = "ws" + strings.TrimPrefix(server.URL, "http")
	config := Config{Dial: adapter.Dial, ReceiveTimeout: 20 * time.Millisecond, OperationTimeout: time.Second}
	tr, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.Open(ctx, session); err != nil {
		t.Fatal(err)
	}
	for _, seq := range []uint64{0, 1} {
		delivery, err := tr.Receive(ctx, requestFor(session, seq))
		if err != nil || delivery == nil || delivery.Identity != identity {
			t.Fatal("actual stored delivery", seq, err)
		}
	}
	payload := testPayload(t, local, session.SessionID, session.Role, 0, 1)
	prepared, err := tr.Prepare(ctx, ports.PeerRequest{SessionID: session.SessionID, Role: session.Role, PeerPublicKey: session.PeerPublicKey}, payload, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Publish(ctx, prepared.Carrier); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(<-publications, prepared.Carrier) {
		t.Fatal("wire publication changed")
	}
	for range 3 {
		if delivery, err := tr.Receive(ctx, requestFor(session, 2)); err != nil || delivery != nil {
			t.Fatal("quiet actual socket", err)
		}
	}
	if active.Load() != 1 {
		t.Fatal("quiet wait lost connection")
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	// New process equivalent: same saved session, cursor and exact carrier.
	restored, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.Open(ctx, session); err != nil {
		t.Fatal(err)
	}
	delivery, err := restored.Receive(ctx, requestFor(session, 1))
	if err != nil || delivery == nil {
		t.Fatal("restored history", err)
	}
	if err := restored.Publish(ctx, prepared.Carrier); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(<-publications, prepared.Carrier) {
		t.Fatal("saved carrier changed after restore")
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errors:
		t.Fatal(err)
	default:
	}
	var eventObject map[string]json.RawMessage
	fields, _ := relayFields(prepared.Carrier)
	if json.Unmarshal(fields[1], &eventObject) != nil || len(eventObject) != 7 {
		t.Fatal("unexpected event fields")
	}
}
