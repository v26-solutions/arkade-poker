package nostr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/shuffle"
)

func testKey(t testing.TB, n byte) *Key {
	t.Helper()
	var b [32]byte
	b[31] = n
	k, err := keyFromSecret(b[:])
	if err != nil {
		t.Fatal(err)
	}
	return k
}
func testPublic(t testing.TB, k *Key) [32]byte {
	t.Helper()
	p, err := k.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func testPayload(t testing.TB, k *Key, session [32]byte, role uint8, sequence uint64, marker byte) []byte {
	t.Helper()
	secret, public, proof, err := shuffle.GenerateKey(context.Background(), nil, []byte("transport encoding fixture"))
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	identity := testPublic(t, k)
	p := covenant.Participant{SigningKey: identity, EncryptionKey: public, PayoutScript: append([]byte{0x51, 0x20}, identity[:]...)}
	kind := game.KeyOffer
	if role == uint8(covenant.Player1) {
		kind = game.KeyReply
	}
	m := game.Message{SessionID: game.SessionID(session), Sender: covenant.Player(role), Identity: identity, Sequence: sequence, Kind: kind, Participant: &p, Ownership: &proof, WalletOwnership: []byte{marker}}
	b, err := game.EncodeMessage(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func testEvent(t testing.TB, k *Key, recipient, session [32]byte, role uint8, sequence uint64) Event {
	t.Helper()
	e, err := k.Sign(Envelope{SessionID: session, Sequence: sequence, Role: role, Recipient: recipient, Payload: testPayload(t, k, session, role, sequence, 1)}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestNIP01EventAuthenticationAndCanonicalPayload(t *testing.T) {
	k := testKey(t, 1)
	recipient := testPublic(t, testKey(t, 2))
	session := [32]byte{3}
	e := testEvent(t, k, recipient, session, uint8(covenant.Player2), 0)
	b, err := EncodeEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEvent(b)
	if err != nil || !reflect.DeepEqual(decoded, e) {
		t.Fatal(err)
	}
	envelope, err := Authenticate(decoded)
	if err != nil || envelope.SessionID != session || envelope.Recipient != recipient {
		t.Fatal(err)
	}
	// Independent fixed-field NIP-01 serialization, without struct field order.
	canonical := fmt.Sprintf("[0,\"%x\",1000,2100,[[\"g\",\"%x\"],[\"p\",\"%x\"]],\"%s\"]", e.PublicKey, session, recipient, e.Content)
	if e.ID != sha256.Sum256([]byte(canonical)) {
		t.Fatal("NIP-01 event digest")
	}
	for name, change := range map[string]func(*Event){
		"id": func(e *Event) { e.ID[0] ^= 1 }, "signature": func(e *Event) { e.Signature[0] ^= 1 }, "timestamp": func(e *Event) { e.CreatedAt++ }, "author": func(e *Event) { e.PublicKey = recipient }, "tag order": func(e *Event) { e.Tags = [][]string{e.Tags[1], e.Tags[0]} }, "recipient": func(e *Event) { e.Tags = tags(session, testPublic(t, testKey(t, 4))) }, "session": func(e *Event) { e.Tags = tags([32]byte{4}, recipient) }, "domain": func(e *Event) { e.Content = "other:" + strings.TrimPrefix(e.Content, contentPrefix) }, "padded base64": func(e *Event) { e.Content += "=" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := e
			change(&bad)
			if _, err := Authenticate(bad); err == nil {
				t.Fatal("unauthenticated mutation accepted")
			}
		})
	}
	for _, bad := range [][]byte{
		append(append(bytes.Clone(b[:len(b)-1]), []byte(",\"id\":\"00\"")...), '}'),
		bytes.Replace(b, []byte("\"kind\":2100"), []byte("\"kind\":null"), 1),
		bytes.Replace(b, []byte("\"created_at\":1000"), []byte("\"created_at\":1e3"), 1),
		bytes.Replace(b, []byte("\"id\":"), []byte("\"ID\":"), 1),
		[]byte("null"), append(bytes.Clone(b), 'x'),
	} {
		if _, err := DecodeEvent(bad); err == nil {
			t.Fatal("ambiguous event encoding")
		}
	}
	// A valid outer signature cannot admit a different inner identity/session.
	m, err := game.DecodeMessage(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	m.Identity = recipient
	badPayload, err := game.EncodeMessage(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Sign(Envelope{SessionID: session, Role: envelope.Role, Recipient: recipient, Payload: badPayload}, 1000); !errors.Is(err, ErrSession) {
		t.Fatal(err)
	}
	if _, err := k.Sign(Envelope{SessionID: session, Role: envelope.Role, Recipient: recipient, Payload: []byte("private event")}, 1000); err == nil {
		t.Fatal("private event admitted")
	}
}

func TestMaximumPublicMessageFitsCarrier(t *testing.T) {
	k := testKey(t, 1)
	recipient := testPublic(t, testKey(t, 2))
	session := [32]byte{3}
	payload := testPayload(t, k, session, uint8(covenant.Player2), 0, 1)
	m, err := game.DecodeMessage(payload)
	if err != nil {
		t.Fatal(err)
	}
	m.WalletOwnership = bytes.Repeat([]byte{0x88}, game.MaxMessageBytes-len(payload)+1)
	payload, err = game.EncodeMessage(m)
	if err != nil || len(payload) != game.MaxMessageBytes {
		t.Fatal(len(payload), err)
	}
	e, err := k.Sign(Envelope{SessionID: session, Role: uint8(covenant.Player2), Recipient: recipient, Payload: payload}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	carrier, _ := json.Marshal([]any{"EVENT", json.RawMessage(encoded)})
	if len(carrier) > MaxCarrierBytes {
		t.Fatal(len(carrier))
	}
	if !strings.HasSuffix(e.Content, base64.RawStdEncoding.EncodeToString(payload)) {
		t.Fatal("payload changed")
	}
}

func TestSessionKeyOwnershipAndRedaction(t *testing.T) {
	k := testKey(t, 1)
	encoded, err := k.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeKey(encoded)
	if err != nil {
		t.Fatal(err)
	}
	public := testPublic(t, k)
	clear(encoded)
	if testPublic(t, restored) != public {
		t.Fatal("key buffer retained")
	}
	if _, err := json.Marshal(k); err == nil {
		t.Fatal("secret JSON export")
	}
	secret := k.secret.Serialize()
	defer clear(secret)
	if strings.Contains(fmt.Sprintf("%v %#v", k, k), hex.EncodeToString(secret)) {
		t.Fatal("secret formatting")
	}
	if _, err := GenerateKey(context.Background(), bytes.NewReader(make([]byte, 128*32))); !errors.Is(err, ErrKey) {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, make([]byte, 32), bytes.Repeat([]byte{0xff}, 32)} {
		if _, err := keyFromSecret(bad); err == nil {
			t.Fatal("invalid scalar")
		}
	}
	k.Destroy()
	if _, err := k.PublicKey(); !errors.Is(err, ErrKey) {
		t.Fatal(err)
	}
	if testPublic(t, restored) != public {
		t.Fatal("independent decoded key destroyed")
	}
}

func FuzzEventDecode(f *testing.F) {
	e := testEvent(f, testKey(f, 1), testPublic(f, testKey(f, 2)), [32]byte{3}, uint8(covenant.Player2), 0)
	b, err := EncodeEvent(e)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(b)
	f.Add([]byte("{}"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > MaxCarrierBytes+1 {
			return
		}
		e, err := DecodeEvent(b)
		if err == nil {
			_, _ = Authenticate(e)
		}
	})
}
