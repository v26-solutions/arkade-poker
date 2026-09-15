package nostr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"sync"
	"time"
	"unicode/utf8"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

var (
	ErrClosed       = errors.New("nostr: transport closed; reopen the session")
	ErrBusy         = errors.New("nostr: concurrent operation")
	ErrEquivocation = errors.New("nostr: peer equivocated")
	ErrRejected     = errors.New("nostr: publication rejected")
)

type Config struct {
	Dial               ports.DialSocket
	OperationTimeout   time.Duration // default 10s, covering dial/subscribe or publish/ACK.
	ReceiveTimeout     time.Duration // default 250ms, bounded quiet wait without closing the socket.
	MaxPendingMessages int           // default 8, including consumed digests.
	MaxPendingBytes    int           // default four maximum game messages.
}

type retained struct {
	payload  []byte
	digest   [32]byte
	identity [32]byte
}

// One driver owns operations sequentially. Close may run concurrently and joins
// the reader and pending operation before destroying the copied session key.
// Reopen retains the peer pin/cache and performs no automatic publication retry.
type Transport struct {
	config        Config
	op            sync.Mutex
	life          context.Context
	cancel        context.CancelFunc
	closeOnce     sync.Once
	closeErr      error
	key           *Key
	session       [32]byte
	role          uint8
	relay         string
	peer          *[32]byte
	cursor        uint64
	received      map[uint64]retained
	receivedBytes int
	equivocated   bool
	ready         bool
	stream        *relayStream
}

func New(config Config) (*Transport, error) {
	if config.Dial == nil {
		return nil, ErrSession
	}
	if config.OperationTimeout == 0 {
		config.OperationTimeout = 10 * time.Second
	}
	if config.ReceiveTimeout == 0 {
		config.ReceiveTimeout = 250 * time.Millisecond
	}
	if config.MaxPendingMessages == 0 {
		config.MaxPendingMessages = 8
	}
	if config.MaxPendingBytes == 0 {
		config.MaxPendingBytes = 4 * game.MaxMessageBytes
	}
	if config.OperationTimeout < 0 || config.ReceiveTimeout < 0 || config.MaxPendingMessages < 1 || config.MaxPendingMessages > 1024 || config.MaxPendingBytes < game.MaxMessageBytes || config.MaxPendingBytes > 64*game.MaxMessageBytes {
		return nil, ErrLimit
	}
	life, cancel := context.WithCancel(context.Background())
	return &Transport{config: config, life: life, cancel: cancel, received: make(map[uint64]retained)}, nil
}
func (t *Transport) enter(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !t.op.TryLock() {
		return ErrBusy
	}
	if t.life.Err() != nil {
		t.op.Unlock()
		return ErrClosed
	}
	return nil
}
func (t *Transport) operation(ctx context.Context, timeout time.Duration) (context.Context, func()) {
	bounded, cancel := context.WithTimeout(ctx, timeout)
	stop := context.AfterFunc(t.life, cancel)
	return bounded, func() { stop(); cancel() }
}
func (t *Transport) pin(peer *[32]byte) error {
	if peer == nil {
		return nil
	}
	local, _ := t.key.PublicKey()
	if *peer == local {
		return ErrSession
	}
	if _, err := schnorr.ParsePubKey(peer[:]); err != nil {
		return ErrSession
	}
	if t.peer != nil && *t.peer != *peer {
		return ErrSession
	}
	copy := *peer
	t.peer = &copy
	return nil
}
func (t *Transport) Open(ctx context.Context, session ports.PeerSession) error {
	defer clear(session.TransportSecret[:])
	if err := t.enter(ctx); err != nil {
		return err
	}
	defer t.op.Unlock()
	if session.Role != uint8(covenant.Player1) && session.Role != uint8(covenant.Player2) {
		return ErrSession
	}
	endpoint, err := url.Parse(session.RelayURL)
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "ws" && endpoint.Scheme != "wss") || endpoint.User != nil || endpoint.Fragment != "" {
		return ErrSession
	}
	key, err := keyFromSecret(session.TransportSecret[:])
	if err != nil {
		return err
	}
	if t.key != nil {
		saved := t.key.secret.Serialize()
		same := bytes.Equal(saved, session.TransportSecret[:])
		clear(saved)
		key.Destroy()
		if !same || t.session != session.SessionID || t.role != session.Role || t.relay != session.RelayURL {
			return ErrSession
		}
	} else {
		t.key = key
		t.session = session.SessionID
		t.role = session.Role
		t.relay = session.RelayURL
	}
	if err := t.pin(session.PeerPublicKey); err != nil {
		return err
	}
	if t.role == uint8(covenant.Player2) && t.peer == nil {
		return ErrSession
	}
	if t.equivocated {
		return ErrEquivocation
	}
	if t.ready && t.stream != nil && t.stream.ctx.Err() == nil {
		return nil
	}
	t.ready = false
	if t.stream != nil {
		t.stream.close()
		t.stream = nil
	}
	bounded, finish := t.operation(ctx, t.config.OperationTimeout)
	defer finish()
	socket, err := t.config.Dial(bounded, t.relay)
	if err != nil {
		return err
	}
	if socket == nil {
		return ErrClosed
	}
	stream := newRelayStream(t.life, socket)
	t.stream = stream
	local, _ := t.key.PublicKey()
	filter := map[string]any{"kinds": []uint32{Kind}, "#g": []string{hex.EncodeToString(t.session[:])}, "#p": []string{hex.EncodeToString(local[:])}}
	if t.peer != nil {
		filter["authors"] = []string{hex.EncodeToString(t.peer[:])}
	}
	// No since/until/limit: relay history order must not hide sequence zero.
	request, _ := json.Marshal([]any{"REQ", hex.EncodeToString(t.session[:]), filter})
	if err := socket.Write(bounded, request); err != nil {
		stream.close()
		return err
	}
	if err := bounded.Err(); err != nil {
		stream.close()
		return err
	}
	if err := context.Cause(stream.ctx); err != nil {
		return err
	}
	t.ready = true
	return nil
}
func (t *Transport) check(request ports.PeerRequest, role uint8) error {
	if t.key == nil || request.SessionID != t.session || request.Role != role {
		return ErrSession
	}
	if t.equivocated {
		return ErrEquivocation
	}
	return t.pin(request.PeerPublicKey)
}
func (t *Transport) requireReady() error {
	if t.equivocated {
		return ErrEquivocation
	}
	if !t.ready || t.stream == nil {
		return ErrClosed
	}
	if err := context.Cause(t.stream.ctx); err != nil {
		return err
	}
	return nil
}
func (t *Transport) Prepare(ctx context.Context, request ports.PeerRequest, payload []byte, createdAt int64) (ports.PreparedMessage, error) {
	if err := t.enter(ctx); err != nil {
		return ports.PreparedMessage{}, err
	}
	defer t.op.Unlock()
	if err := t.check(request, t.role); err != nil {
		return ports.PreparedMessage{}, err
	}
	if t.peer == nil {
		return ports.PreparedMessage{}, ErrSession
	}
	event, err := t.key.Sign(Envelope{SessionID: t.session, Sequence: request.Sequence, Role: t.role, Recipient: *t.peer, Payload: payload}, createdAt)
	if err != nil {
		return ports.PreparedMessage{}, err
	}
	encoded, err := EncodeEvent(event)
	if err != nil {
		return ports.PreparedMessage{}, err
	}
	carrier, err := json.Marshal([]any{"EVENT", json.RawMessage(encoded)})
	if err != nil {
		return ports.PreparedMessage{}, err
	}
	if len(carrier) > MaxCarrierBytes {
		return ports.PreparedMessage{}, ErrLimit
	}
	envelope, err := Authenticate(event)
	if err != nil || !bytes.Equal(envelope.Payload, payload) {
		return ports.PreparedMessage{}, ErrSession
	}
	if err := ctx.Err(); err != nil {
		return ports.PreparedMessage{}, err
	}
	return ports.PreparedMessage{Payload: bytes.Clone(payload), Carrier: carrier}, nil
}
func (t *Transport) Publish(ctx context.Context, carrier []byte) error {
	if err := t.enter(ctx); err != nil {
		return err
	}
	defer t.op.Unlock()
	if err := t.requireReady(); err != nil {
		return err
	}
	fields, err := relayFields(carrier)
	if err != nil || len(fields) != 2 || stringValue(fields[0]) != "EVENT" {
		return ErrEncoding
	}
	event, err := DecodeEvent(fields[1])
	if err != nil {
		return err
	}
	envelope, err := Authenticate(event)
	if err != nil {
		return err
	}
	local, _ := t.key.PublicKey()
	if t.peer == nil || event.PublicKey != local || envelope.SessionID != t.session || envelope.Role != t.role || envelope.Recipient != *t.peer {
		return ErrSession
	}
	bounded, finish := t.operation(ctx, t.config.OperationTimeout)
	defer finish()
	// Failure after sending may mean uncertain delivery. Caller retains/retries
	// the exact carrier, and must explicitly Open after an operation error.
	t.ready = false
	defer func() {
		if !t.ready {
			t.stream.close()
		}
	}()
	if err := t.stream.socket.Write(bounded, bytes.Clone(carrier)); err != nil {
		return err
	}
	for {
		ack, err := t.pump(bounded)
		if err != nil {
			return err
		}
		if ack != nil && ack.id == event.ID {
			if err := bounded.Err(); err != nil {
				return err
			}
			if !ack.accepted {
				return ErrRejected
			}
			t.ready = true
			return nil
		}
	}
}
func (t *Transport) Receive(ctx context.Context, request ports.PeerRequest) (*ports.PeerDelivery, error) {
	if err := t.enter(ctx); err != nil {
		return nil, err
	}
	defer t.op.Unlock()
	expected := uint8(covenant.Player1)
	if t.role == expected {
		expected = uint8(covenant.Player2)
	}
	if err := t.check(request, expected); err != nil {
		return nil, err
	}
	if err := t.requireReady(); err != nil {
		return nil, err
	}
	if request.Sequence < t.cursor || t.peer == nil && request.Sequence != 0 {
		return nil, ErrSession
	}
	t.cursor = request.Sequence
	for seq, item := range t.received {
		if seq < t.cursor && item.payload != nil {
			t.receivedBytes -= len(item.payload)
			t.receivedBytes += 32
			item.digest = sha256.Sum256(item.payload)
			item.payload = nil
			t.received[seq] = item
		}
	}
	cached := func() *ports.PeerDelivery {
		if item, ok := t.received[t.cursor]; ok && item.payload != nil {
			return &ports.PeerDelivery{Identity: item.identity, Payload: bytes.Clone(item.payload)}
		}
		return nil
	}
	if value := cached(); value != nil {
		return value, nil
	}
	bounded, finish := t.operation(ctx, t.config.ReceiveTimeout)
	defer finish()
	for {
		_, err := t.pump(bounded)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && t.life.Err() == nil && t.stream.ctx.Err() == nil {
				return nil, nil
			}
			t.ready = false
			t.stream.close()
			return nil, err
		}
		// Already admitted late messages stay cached, even if the wait just expired.
		if bounded.Err() != nil {
			if errors.Is(bounded.Err(), context.DeadlineExceeded) {
				return nil, nil
			}
			t.ready = false
			t.stream.close()
			return nil, bounded.Err()
		}
		if value := cached(); value != nil {
			return value, nil
		}
	}
}
func (t *Transport) Close() error {
	t.closeOnce.Do(func() {
		t.cancel()
		t.op.Lock()
		defer t.op.Unlock()
		if t.stream != nil {
			t.closeErr = t.stream.close()
		}
		if t.key != nil {
			t.key.Destroy()
		}
		t.ready = false
		t.received = nil
		t.receivedBytes = 0
	})
	return t.closeErr
}

var _ ports.PeerTransport = (*Transport)(nil)

type acknowledgement struct {
	id       [32]byte
	accepted bool
}

func relayFields(data []byte) ([]json.RawMessage, error) {
	if len(data) > MaxCarrierBytes {
		return nil, ErrLimit
	}
	if !utf8.Valid(data) {
		return nil, ErrEncoding
	}
	var fields []json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) == 0 || len(fields) > 4 {
		return nil, ErrEncoding
	}
	return fields, nil
}
func stringValue(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}
func (t *Transport) pump(ctx context.Context) (*acknowledgement, error) {
	if err := context.Cause(t.stream.ctx); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.stream.ctx.Done():
		return nil, context.Cause(t.stream.ctx)
	case data := <-t.stream.frames:
		if err := context.Cause(t.stream.ctx); err != nil {
			return nil, err
		}
		fields, err := relayFields(data)
		if err != nil {
			return nil, err
		}
		switch stringValue(fields[0]) {
		case "OK":
			if len(fields) != 4 || !isString(fields[3]) {
				return nil, ErrEncoding
			}
			var a acknowledgement
			if err := decodeHex(stringValue(fields[1]), a.id[:]); err != nil {
				return nil, err
			}
			if bytes.Equal(bytes.TrimSpace(fields[2]), []byte("null")) || json.Unmarshal(fields[2], &a.accepted) != nil {
				return nil, ErrEncoding
			}
			return &a, nil
		case "EVENT":
			if len(fields) != 3 || !isString(fields[1]) {
				return nil, ErrEncoding
			}
			if stringValue(fields[1]) != hex.EncodeToString(t.session[:]) {
				return nil, nil
			}
			encoded, err := decodeEventJSON(fields[2])
			if err != nil {
				return nil, err
			}
			local, _ := t.key.PublicKey()
			if encoded.Kind != Kind || !reflect.DeepEqual(encoded.Tags, tags(t.session, local)) {
				return nil, nil
			}
			var author [32]byte
			if err := decodeHex(encoded.PublicKey, author[:]); err != nil {
				return nil, err
			}
			if _, err := schnorr.ParsePubKey(author[:]); err != nil {
				return nil, ErrAuthentication
			}
			if author == local || t.peer != nil && author != *t.peer {
				return nil, nil
			}
			event, err := eventFromJSON(encoded)
			if err != nil {
				return nil, err
			}
			return nil, t.admit(event)
		case "CLOSED":
			if len(fields) != 3 || !isString(fields[1]) || !isString(fields[2]) {
				return nil, ErrEncoding
			}
			if stringValue(fields[1]) == hex.EncodeToString(t.session[:]) {
				return nil, ErrClosed
			}
			return nil, nil
		case "EOSE", "NOTICE":
			if len(fields) != 2 || !isString(fields[1]) {
				return nil, ErrEncoding
			}
			return nil, nil
		default:
			return nil, ErrEncoding
		}
	}
}
func isString(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '"' && json.Valid(raw)
}
func (t *Transport) admit(event Event) error {
	local, _ := t.key.PublicKey()
	if event.Kind != Kind || !reflect.DeepEqual(event.Tags, tags(t.session, local)) || event.PublicKey == local || t.peer != nil && event.PublicKey != *t.peer {
		return nil
	}
	envelope, err := Authenticate(event)
	if err != nil {
		return err
	}
	expected := uint8(covenant.Player1)
	if t.role == expected {
		expected = uint8(covenant.Player2)
	}
	if envelope.Role != expected {
		return ErrSession
	}
	if t.peer == nil {
		message, err := game.DecodeMessage(envelope.Payload)
		if err != nil || envelope.Sequence != 0 || message.Kind != game.KeyOffer || expected != uint8(covenant.Player2) {
			return ErrSession
		}
	}
	if old, ok := t.received[envelope.Sequence]; ok {
		same := bytes.Equal(old.payload, envelope.Payload)
		if old.payload == nil {
			same = old.digest == sha256.Sum256(envelope.Payload)
		}
		if !same {
			t.equivocated = true
			return ErrEquivocation
		}
		return nil
	}
	if envelope.Sequence < t.cursor {
		return nil
	}
	if len(t.received) >= t.config.MaxPendingMessages || len(envelope.Payload) > t.config.MaxPendingBytes-t.receivedBytes {
		return ErrLimit
	}
	t.received[envelope.Sequence] = retained{payload: envelope.Payload, identity: event.PublicKey}
	t.receivedBytes += len(envelope.Payload)
	peer := event.PublicKey
	t.peer = &peer
	return nil
}
