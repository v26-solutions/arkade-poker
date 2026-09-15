package nostr

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
)

type socketFrame struct {
	data []byte
	err  error
}
type memorySocket struct {
	incoming   chan socketFrame
	writes     chan []byte
	closed     chan struct{}
	once       sync.Once
	closeCalls atomic.Int32
}

func newMemorySocket() *memorySocket {
	return &memorySocket{incoming: make(chan socketFrame, 32), writes: make(chan []byte, 32), closed: make(chan struct{})}
}
func (s *memorySocket) Read(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closed:
		return nil, io.EOF
	case frame := <-s.incoming:
		return frame.data, frame.err
	}
}
func (s *memorySocket) Write(ctx context.Context, b []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closed:
		return io.EOF
	case s.writes <- bytes.Clone(b):
		return nil
	}
}
func (s *memorySocket) Close() error {
	s.closeCalls.Add(1)
	s.once.Do(func() { close(s.closed) })
	return nil
}
func (s *memorySocket) send(b []byte) { s.incoming <- socketFrame{data: b} }
func rawEvent(e Event) json.RawMessage {
	b, _ := json.Marshal(eventJSON{hex.EncodeToString(e.ID[:]), hex.EncodeToString(e.PublicKey[:]), e.CreatedAt, e.Kind, e.Tags, e.Content, hex.EncodeToString(e.Signature[:])})
	return b
}
func relayEvent(session [32]byte, e Event) []byte {
	b, _ := json.Marshal([]any{"EVENT", hex.EncodeToString(session[:]), rawEvent(e)})
	return b
}
func ack(id [32]byte, accepted bool) []byte {
	b, _ := json.Marshal([]any{"OK", hex.EncodeToString(id[:]), accepted, "fixture"})
	return b
}
func expectWrite(t testing.TB, s *memorySocket) []byte {
	t.Helper()
	select {
	case b := <-s.writes:
		return b
	case <-time.After(time.Second):
		t.Fatal("missing socket write")
		return nil
	}
}

func transportFixture(t testing.TB, pinned bool) (*Transport, *memorySocket, ports.PeerSession, chan *memorySocket) {
	t.Helper()
	connections := make(chan *memorySocket, 16)
	tr, err := New(Config{Dial: func(ctx context.Context, _ string) (ports.Socket, error) {
		s := newMemorySocket()
		connections <- s
		return s, nil
	}, ReceiveTimeout: 20 * time.Millisecond, OperationTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	session := ports.PeerSession{RelayURL: "ws://relay.invalid", SessionID: [32]byte{9}, Role: uint8(covenant.Player1)}
	session.TransportSecret[31] = 1
	if pinned {
		peer := testPublic(t, testKey(t, 2))
		session.PeerPublicKey = &peer
	}
	if err := tr.Open(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	s := <-connections
	request := expectWrite(t, s)
	var fields []json.RawMessage
	if json.Unmarshal(request, &fields) != nil || len(fields) != 3 || stringValue(fields[0]) != "REQ" {
		t.Fatal("subscription", string(request))
	}
	var filter map[string]json.RawMessage
	if json.Unmarshal(fields[2], &filter) != nil {
		t.Fatal("filter")
	}
	for _, forbidden := range []string{"since", "until", "limit"} {
		if filter[forbidden] != nil {
			t.Fatal("stored history constrained")
		}
	}
	return tr, s, session, connections
}
func requestFor(session ports.PeerSession, sequence uint64) ports.PeerRequest {
	return ports.PeerRequest{SessionID: session.SessionID, Role: uint8(covenant.Player2), Sequence: sequence, PeerPublicKey: session.PeerPublicKey}
}

func TestStoredMessagesOrderingDedupAndPeerPin(t *testing.T) {
	tr, s, session, connections := transportFixture(t, true)
	peer := testKey(t, 2)
	local := testPublic(t, testKey(t, 1))
	first := testEvent(t, peer, local, session.SessionID, uint8(covenant.Player2), 0)
	second := testEvent(t, peer, local, session.SessionID, uint8(covenant.Player2), 1)
	// Canonical public messages exercise transport ordering. Only the game decides
	// whether each message kind/proof is legal at that protocol step.
	s.send(relayEvent(session.SessionID, second))
	s.send(relayEvent(session.SessionID, first))
	got, err := tr.Receive(context.Background(), requestFor(session, 0))
	if err != nil || got == nil {
		t.Fatal(err)
	}
	original := bytes.Clone(got.Payload)
	got.Payload[0] ^= 1
	again, err := tr.Receive(context.Background(), requestFor(session, 0))
	if err != nil || !bytes.Equal(again.Payload, original) {
		t.Fatal("delivery not retained/owned", err)
	}
	next, err := tr.Receive(context.Background(), requestFor(session, 1))
	if err != nil || next == nil {
		t.Fatal(err)
	}
	if tr.received[0].payload != nil || tr.received[0].digest == ([32]byte{}) {
		t.Fatal("consumed digest not retained")
	}
	if err := tr.Open(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if len(connections) != 0 {
		t.Fatal("idempotent open dialed again")
	}
	changed := session
	changed.TransportSecret[31] = 3
	if err := tr.Open(context.Background(), changed); !errors.Is(err, ErrSession) {
		t.Fatal("changed restored identity", err)
	}
	if _, err := tr.Receive(context.Background(), requestFor(session, 0)); !errors.Is(err, ErrSession) {
		t.Fatal("cursor went backwards", err)
	}
	// Same application bytes with a different valid event timestamp are retries,
	// not equivocation. A consumed digest remains checked while reading later work.
	envelope, err := Authenticate(first)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := peer.Sign(envelope, 1001)
	if err != nil {
		t.Fatal(err)
	}
	s.send(relayEvent(session.SessionID, retry))
	if value, err := tr.Receive(context.Background(), requestFor(session, 2)); err != nil || value != nil {
		t.Fatal("retry equivocated", err)
	}
	changedEvent := testEvent(t, peer, local, session.SessionID, uint8(covenant.Player2), 0)
	s.send(relayEvent(session.SessionID, changedEvent))
	if _, err := tr.Receive(context.Background(), requestFor(session, 2)); !errors.Is(err, ErrEquivocation) {
		t.Fatal("consumed equivocation missed", err)
	}
	if err := tr.Open(context.Background(), session); !errors.Is(err, ErrEquivocation) {
		t.Fatal("reopen cleared equivocation", err)
	}
}

func TestFirstCanonicalJoinRemainsPinned(t *testing.T) {
	tr, s, session, _ := transportFixture(t, false)
	local := testPublic(t, testKey(t, 1))
	peer := testKey(t, 2)
	first := testEvent(t, peer, local, session.SessionID, uint8(covenant.Player2), 0)
	// Transport pinning follows canonical authenticated key-offer bytes. Shuffle
	// or wallet ownership rejection by the game must not select a different peer.
	s.send(relayEvent(session.SessionID, first))
	got, err := tr.Receive(context.Background(), requestFor(session, 0))
	if err != nil || got.Identity != testPublic(t, peer) {
		t.Fatal(err)
	}
	competing := testEvent(t, testKey(t, 3), local, session.SessionID, uint8(covenant.Player2), 1)
	s.send(relayEvent(session.SessionID, competing))
	if got, err := tr.Receive(context.Background(), requestFor(session, 1)); err != nil || got != nil {
		t.Fatal("other author delivered", err)
	}
	if *tr.peer != testPublic(t, peer) {
		t.Fatal("peer rotated")
	}
	wrong := session
	other := testPublic(t, testKey(t, 3))
	wrong.PeerPublicKey = &other
	if err := tr.Open(context.Background(), wrong); !errors.Is(err, ErrSession) {
		t.Fatal("recorded peer replaced pin", err)
	}
}

func TestUnpinnedJoinAuthenticationAndContext(t *testing.T) {
	for _, kind := range []string{"signature", "wrong sequence", "wrong role", "wrong session"} {
		t.Run(kind, func(t *testing.T) {
			tr, s, session, _ := transportFixture(t, false)
			event := testEvent(t, testKey(t, 2), testPublic(t, testKey(t, 1)), session.SessionID, uint8(covenant.Player2), 0)
			switch kind {
			case "signature":
				event.Signature[0] ^= 1
			case "wrong sequence":
				event = testEvent(t, testKey(t, 2), testPublic(t, testKey(t, 1)), session.SessionID, uint8(covenant.Player2), 1)
			case "wrong role":
				event = testEvent(t, testKey(t, 2), testPublic(t, testKey(t, 1)), session.SessionID, uint8(covenant.Player1), 0)
			case "wrong session":
				event.Tags = tags([32]byte{77}, testPublic(t, testKey(t, 1)))
			}
			s.send(relayEvent(session.SessionID, event))
			got, err := tr.Receive(context.Background(), requestFor(session, 0))
			if kind == "wrong session" {
				if err != nil || got != nil {
					t.Fatal("unrelated filter event", err)
				}
			} else if err == nil {
				t.Fatal("invalid join delivered")
			}
			if tr.peer != nil {
				t.Fatal("invalid join pinned")
			}
		})
	}
}

func TestPublicationExactACKAndIncomingDelivery(t *testing.T) {
	tr, s, session, _ := transportFixture(t, true)
	localKey, peerKey := testKey(t, 1), testKey(t, 2)
	payload := testPayload(t, localKey, session.SessionID, session.Role, 0, 1)
	request := ports.PeerRequest{SessionID: session.SessionID, Role: session.Role, Sequence: 0, PeerPublicKey: session.PeerPublicKey}
	prepared, err := tr.Prepare(context.Background(), request, payload, 1000)
	if err != nil {
		t.Fatal(err)
	}
	saved := bytes.Clone(prepared.Carrier)
	payload[0] ^= 1
	if bytes.Equal(payload, prepared.Payload) {
		t.Fatal("caller payload aliases saved bytes")
	}
	fields, err := relayFields(saved)
	if err != nil {
		t.Fatal(err)
	}
	event, err := DecodeEvent(fields[1])
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- tr.Publish(context.Background(), saved) }()
	if sent := expectWrite(t, s); !bytes.Equal(sent, saved) {
		t.Fatal("saved carrier re-encoded")
	}
	incoming := testEvent(t, peerKey, testPublic(t, localKey), session.SessionID, uint8(covenant.Player2), 0)
	s.send(relayEvent(session.SessionID, incoming))
	s.send(ack([32]byte{99}, true))
	select {
	case err := <-finished:
		t.Fatal("wrong acknowledgement completed publish", err)
	case <-time.After(5 * time.Millisecond):
	}
	s.send(ack(event.ID, true))
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	got, err := tr.Receive(context.Background(), requestFor(session, 0))
	if err != nil || got == nil || got.Identity != testPublic(t, peerKey) {
		t.Fatal("incoming during publish lost", err)
	}
}

func TestQuietWaitReopenAndCancellation(t *testing.T) {
	tr, s, session, connections := transportFixture(t, true)
	for range 2 {
		if got, err := tr.Receive(context.Background(), requestFor(session, 0)); err != nil || got != nil {
			t.Fatal(err)
		}
	}
	if s.closeCalls.Load() != 0 {
		t.Fatal("quiet wait closed socket")
	}
	s.incoming <- socketFrame{err: io.EOF}
	if _, err := tr.Receive(context.Background(), requestFor(session, 0)); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if s.closeCalls.Load() != 1 {
		t.Fatal("reader not joined/closed once")
	}
	if err := tr.Open(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	s2 := <-connections
	expectWrite(t, s2)
	if *tr.peer != *session.PeerPublicKey {
		t.Fatal("reopen lost recorded pin")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := tr.Receive(ctx, requestFor(session, 0)); done <- err }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	tr.Close()
	tr.Close()
	if _, err := tr.Prepare(context.Background(), ports.PeerRequest{}, nil, 0); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := tr.key.PublicKey(); !errors.Is(err, ErrKey) {
		t.Fatal("close retained secret")
	}
}

func TestPublishFailureRequiresExplicitReopen(t *testing.T) {
	for _, mode := range []string{"rejected", "timeout", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			tr, s, session, connections := transportFixture(t, true)
			payload := testPayload(t, testKey(t, 1), session.SessionID, session.Role, 0, 1)
			prepared, err := tr.Prepare(context.Background(), ports.PeerRequest{SessionID: session.SessionID, Role: session.Role, PeerPublicKey: session.PeerPublicKey}, payload, 1000)
			if err != nil {
				t.Fatal(err)
			}
			fields, _ := relayFields(prepared.Carrier)
			event, _ := DecodeEvent(fields[1])
			done := make(chan error, 1)
			go func() { done <- tr.Publish(context.Background(), prepared.Carrier) }()
			expectWrite(t, s)
			switch mode {
			case "rejected":
				s.send(ack(event.ID, false))
			case "malformed":
				s.send([]byte(`["OK","bad",null,""]`))
			}
			err = <-done
			if err == nil {
				t.Fatal("failed publish accepted")
			}
			if mode == "rejected" && !errors.Is(err, ErrRejected) {
				t.Fatal(err)
			}
			if mode == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if len(connections) != 0 || len(s.writes) != 0 {
				t.Fatal("hidden reconnect/retry")
			}
			if s.closeCalls.Load() != 1 {
				t.Fatal("failed publication reader leaked")
			}
			if err := tr.Open(context.Background(), session); err != nil {
				t.Fatal(err)
			}
			next := <-connections
			expectWrite(t, next)
			go func() { done <- tr.Publish(context.Background(), prepared.Carrier) }()
			if !bytes.Equal(expectWrite(t, next), prepared.Carrier) {
				t.Fatal("retry carrier changed")
			}
			next.send(ack(event.ID, true))
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTransportLimitsAndClosePendingDial(t *testing.T) {
	t.Run("reader overflow", func(t *testing.T) {
		tr, s, session, _ := transportFixture(t, true)
		for range 10 {
			s.send([]byte(`["NOTICE","flood"]`))
		}
		select {
		case <-tr.stream.done:
		case <-time.After(time.Second):
			t.Fatal("reader did not stop on overflow")
		}
		if _, err := tr.Receive(context.Background(), requestFor(session, 0)); !errors.Is(err, ErrLimit) {
			t.Fatal(err)
		}
	})
	t.Run("oversized frame", func(t *testing.T) {
		tr, s, session, _ := transportFixture(t, true)
		s.send(make([]byte, MaxCarrierBytes+1))
		if _, err := tr.Receive(context.Background(), requestFor(session, 0)); !errors.Is(err, ErrLimit) {
			t.Fatal(err)
		}
	})
	t.Run("pending count", func(t *testing.T) {
		tr, s, session, _ := transportFixture(t, true)
		tr.config.MaxPendingMessages = 1
		local := testPublic(t, testKey(t, 1))
		peer := testKey(t, 2)
		s.send(relayEvent(session.SessionID, testEvent(t, peer, local, session.SessionID, uint8(covenant.Player2), 1)))
		s.send(relayEvent(session.SessionID, testEvent(t, peer, local, session.SessionID, uint8(covenant.Player2), 0)))
		if _, err := tr.Receive(context.Background(), requestFor(session, 0)); !errors.Is(err, ErrLimit) {
			t.Fatal(err)
		}
	})
	t.Run("pending dial", func(t *testing.T) {
		started := make(chan struct{})
		tr, err := New(Config{Dial: func(ctx context.Context, _ string) (ports.Socket, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}})
		if err != nil {
			t.Fatal(err)
		}
		session := ports.PeerSession{RelayURL: "ws://relay.invalid", SessionID: [32]byte{1}, Role: uint8(covenant.Player1)}
		session.TransportSecret[31] = 1
		done := make(chan error, 1)
		go func() { done <- tr.Open(context.Background(), session) }()
		<-started
		if err := tr.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if _, err := tr.key.PublicKey(); !errors.Is(err, ErrKey) {
			t.Fatal("pending dial secret retained")
		}
	})
}

func TestRelayFraming(t *testing.T) {
	tr, s, session, _ := transportFixture(t, true)
	// Events for another subscription are ignored without decoding their body.
	s.send([]byte(`["EVENT","elsewhere",{"id":"bad","id":"duplicate"}]`))
	unrelated := testEvent(t, testKey(t, 3), testPublic(t, testKey(t, 1)), session.SessionID, uint8(covenant.Player2), 0)
	unrelatedJSON := bytes.Replace(rawEvent(unrelated), []byte(`"sig":"`), []byte(`"sig":"not-hex`), 1)
	frame, _ := json.Marshal([]any{"EVENT", hex.EncodeToString(session.SessionID[:]), json.RawMessage(unrelatedJSON)})
	s.send(frame)
	s.send([]byte(`["EOSE","elsewhere"]`))
	s.send([]byte(`["CLOSED","elsewhere","done"]`))
	if got, err := tr.Receive(context.Background(), requestFor(session, 0)); err != nil || got != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{[]byte(`null`), []byte(`["EOSE"]`), []byte(`["NOTICE",null]`), []byte(`["OK","00",true]`), []byte(`["AUTH","challenge"]`)} {
		t.Run(string(raw), func(t *testing.T) {
			tr, s, session, _ := transportFixture(t, true)
			s.send(raw)
			if _, err := tr.Receive(context.Background(), requestFor(session, 0)); err == nil {
				t.Fatal("malformed relay frame accepted")
			}
		})
	}
	if !reflect.DeepEqual(tr.peer, session.PeerPublicKey) {
		t.Fatal("unrelated frames changed pin")
	}
}
