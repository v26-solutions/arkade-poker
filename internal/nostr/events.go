// Package nostr authenticates public setup delivery using NIP-01 and btcec.
// The game codec admits public message structure; poker/shuffle proof validation
// and durable progress remain in the game. No private event codec is used here.
package nostr

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"arkade-poker/go/internal/game"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

const (
	Kind            uint32 = 2100 // Application-specific regular stored event, not a registered NIP.
	MaxCarrierBytes        = 128 * 1024
	contentPrefix          = "arkade-poker/go/1:"
	keyDomain              = "arkade-poker/nostr-key\x00\x01"
)

var (
	ErrEncoding       = errors.New("nostr: invalid event encoding")
	ErrAuthentication = errors.New("nostr: invalid event signature")
	ErrKey            = errors.New("nostr: invalid session key")
	ErrSession        = errors.New("nostr: session or peer mismatch")
	ErrLimit          = errors.New("nostr: message or buffer limit")
)

// Key is independent of the imported wallet key. Its explicit binary codec is
// for private local storage only; generic formatting/JSON never exports it.
type Key struct{ secret *btcec.PrivateKey }

func (*Key) String() string               { return "[session transport key]" }
func (*Key) GoString() string             { return "[session transport key]" }
func (*Key) MarshalJSON() ([]byte, error) { return nil, ErrKey }

type Event struct {
	ID        [32]byte
	PublicKey [32]byte
	CreatedAt int64
	Kind      uint32
	Tags      [][]string
	Content   string
	Signature [64]byte
}

type Envelope struct {
	SessionID [32]byte
	Sequence  uint64
	Role      uint8
	Recipient [32]byte
	Payload   []byte
}

func keyFromSecret(data []byte) (*Key, error) {
	if len(data) != 32 {
		return nil, ErrKey
	}
	var scalar btcec.ModNScalar
	defer scalar.Zero()
	if scalar.SetByteSlice(data) || scalar.IsZero() {
		return nil, ErrKey
	}
	return &Key{secret: btcec.PrivKeyFromScalar(&scalar)}, nil
}
func GenerateKey(ctx context.Context, entropy io.Reader) (*Key, error) {
	if entropy == nil {
		entropy = rand.Reader
	}
	var data [32]byte
	defer clear(data[:])
	for range 128 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(entropy, data[:]); err != nil {
			return nil, err
		}
		key, err := keyFromSecret(data[:])
		if err == nil {
			if err := ctx.Err(); err != nil {
				key.Destroy()
				return nil, err
			}
			return key, nil
		}
	}
	return nil, ErrKey
}
func DecodeKey(data []byte) (*Key, error) {
	if len(data) != len(keyDomain)+32 || !bytes.Equal(data[:len(keyDomain)], []byte(keyDomain)) {
		return nil, ErrKey
	}
	return keyFromSecret(data[len(keyDomain):])
}
func (k *Key) MarshalBinary() ([]byte, error) {
	if _, err := k.PublicKey(); err != nil {
		return nil, err
	}
	data := k.secret.Serialize()
	defer clear(data)
	return append([]byte(keyDomain), data...), nil
}
func (k *Key) PublicKey() ([32]byte, error) {
	if k == nil || k.secret == nil || k.secret.Key.IsZero() {
		return [32]byte{}, ErrKey
	}
	var out [32]byte
	copy(out[:], schnorr.SerializePubKey(k.secret.PubKey()))
	return out, nil
}
func (k *Key) Destroy() error {
	if k != nil && k.secret != nil {
		k.secret.Zero()
	}
	return nil
}

func tags(session, recipient [32]byte) [][]string {
	return [][]string{{"g", hex.EncodeToString(session[:])}, {"p", hex.EncodeToString(recipient[:])}}
}
func eventDigest(e Event) ([32]byte, error) {
	// This profile uses ASCII-only hex tags and prefixed base64 content, avoiding
	// JSON encoder differences for HTML characters and U+2028/U+2029 in NIP-01.
	b, err := json.Marshal([]any{0, hex.EncodeToString(e.PublicKey[:]), e.CreatedAt, e.Kind, e.Tags, e.Content})
	return sha256.Sum256(b), err
}

func (k *Key) Sign(envelope Envelope, createdAt int64) (Event, error) {
	public, err := k.PublicKey()
	if err != nil {
		return Event{}, err
	}
	if createdAt < 0 || envelope.Recipient == public {
		return Event{}, ErrSession
	}
	if _, err := schnorr.ParsePubKey(envelope.Recipient[:]); err != nil {
		return Event{}, ErrSession
	}
	m, err := game.DecodeMessage(envelope.Payload)
	if err != nil || [32]byte(m.SessionID) != envelope.SessionID || m.Sequence != envelope.Sequence || uint8(m.Sender) != envelope.Role || m.Identity != public {
		return Event{}, ErrSession
	}
	e := Event{PublicKey: public, CreatedAt: createdAt, Kind: Kind, Tags: tags(envelope.SessionID, envelope.Recipient), Content: contentPrefix + base64.RawStdEncoding.EncodeToString(envelope.Payload)}
	e.ID, err = eventDigest(e)
	if err != nil {
		return Event{}, err
	}
	sig, err := schnorr.Sign(k.secret, e.ID[:])
	if err != nil {
		return Event{}, err
	}
	copy(e.Signature[:], sig.Serialize())
	return e, nil
}

func Authenticate(event Event) (Envelope, error) {
	var envelope Envelope
	if event.Kind != Kind || event.CreatedAt < 0 || len(event.Tags) != 2 || len(event.Tags[0]) != 2 || len(event.Tags[1]) != 2 || event.Tags[0][0] != "g" || event.Tags[1][0] != "p" || !strings.HasPrefix(event.Content, contentPrefix) {
		return envelope, ErrEncoding
	}
	if err := decodeHex(event.Tags[0][1], envelope.SessionID[:]); err != nil {
		return Envelope{}, err
	}
	if err := decodeHex(event.Tags[1][1], envelope.Recipient[:]); err != nil {
		return Envelope{}, err
	}
	if _, err := schnorr.ParsePubKey(envelope.Recipient[:]); err != nil {
		return Envelope{}, ErrEncoding
	}
	public, err := schnorr.ParsePubKey(event.PublicKey[:])
	if err != nil {
		return Envelope{}, ErrAuthentication
	}
	content := strings.TrimPrefix(event.Content, contentPrefix)
	if len(content) > base64.RawStdEncoding.EncodedLen(game.MaxMessageBytes) {
		return Envelope{}, ErrLimit
	}
	payload, err := base64.RawStdEncoding.DecodeString(content)
	if err != nil || base64.RawStdEncoding.EncodeToString(payload) != content {
		return Envelope{}, ErrEncoding
	}
	id, err := eventDigest(event)
	if err != nil || id != event.ID {
		return Envelope{}, ErrAuthentication
	}
	sig, err := schnorr.ParseSignature(event.Signature[:])
	if err != nil || !sig.Verify(id[:], public) {
		return Envelope{}, ErrAuthentication
	}
	m, err := game.DecodeMessage(payload)
	if err != nil || m.Identity != event.PublicKey || [32]byte(m.SessionID) != envelope.SessionID {
		return Envelope{}, ErrSession
	}
	envelope.Payload = payload
	envelope.Role = uint8(m.Sender)
	envelope.Sequence = m.Sequence
	return envelope, nil
}

type eventJSON struct {
	ID        string     `json:"id"`
	PublicKey string     `json:"pubkey"`
	CreatedAt int64      `json:"created_at"`
	Kind      uint32     `json:"kind"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
	Signature string     `json:"sig"`
}

func EncodeEvent(event Event) ([]byte, error) {
	if _, err := Authenticate(event); err != nil {
		return nil, err
	}
	b, err := json.Marshal(eventJSON{hex.EncodeToString(event.ID[:]), hex.EncodeToString(event.PublicKey[:]), event.CreatedAt, event.Kind, event.Tags, event.Content, hex.EncodeToString(event.Signature[:])})
	if len(b) > MaxCarrierBytes {
		return nil, ErrLimit
	}
	return b, err
}
func decodeHex(text string, out []byte) error {
	if len(text) != 2*len(out) {
		return ErrEncoding
	}
	for _, c := range text {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return ErrEncoding
		}
	}
	_, err := hex.Decode(out, []byte(text))
	if err != nil {
		return ErrEncoding
	}
	return nil
}
func decodeEventJSON(data []byte) (eventJSON, error) {
	if len(data) > MaxCarrierBytes {
		return eventJSON{}, ErrLimit
	}
	if !utf8.Valid(data) {
		return eventJSON{}, ErrEncoding
	}
	// Do not let encoding/json collapse duplicate fields or match case aliases.
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return eventJSON{}, ErrEncoding
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return eventJSON{}, ErrEncoding
		}
		name, ok := token.(string)
		if !ok {
			return eventJSON{}, ErrEncoding
		}
		if _, ok := fields[name]; ok {
			return eventJSON{}, ErrEncoding
		}
		if len(fields) >= 16 {
			return eventJSON{}, ErrLimit
		}
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return eventJSON{}, ErrEncoding
		}
		fields[name] = raw
	}
	if _, err := d.Token(); err != nil {
		return eventJSON{}, ErrEncoding
	}
	if _, err := d.Token(); err != io.EOF {
		return eventJSON{}, ErrEncoding
	}
	var encoded eventJSON
	for name, target := range map[string]any{"id": &encoded.ID, "pubkey": &encoded.PublicKey, "created_at": &encoded.CreatedAt, "kind": &encoded.Kind, "tags": &encoded.Tags, "content": &encoded.Content, "sig": &encoded.Signature} {
		raw, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, target) != nil {
			return eventJSON{}, ErrEncoding
		}
	}
	var rawTags []json.RawMessage
	if json.Unmarshal(fields["tags"], &rawTags) != nil {
		return eventJSON{}, ErrEncoding
	}
	for _, raw := range rawTags {
		var tag []json.RawMessage
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &tag) != nil {
			return eventJSON{}, ErrEncoding
		}
		for _, value := range tag {
			if !isString(value) {
				return eventJSON{}, ErrEncoding
			}
		}
	}
	if encoded.Kind > 65535 || encoded.CreatedAt < 0 {
		return eventJSON{}, ErrEncoding
	}
	return encoded, nil
}

func DecodeEvent(data []byte) (Event, error) {
	encoded, err := decodeEventJSON(data)
	if err != nil {
		return Event{}, err
	}
	return eventFromJSON(encoded)
}

func eventFromJSON(encoded eventJSON) (Event, error) {
	var e Event
	if err := decodeHex(encoded.ID, e.ID[:]); err != nil {
		return Event{}, err
	}
	if err := decodeHex(encoded.PublicKey, e.PublicKey[:]); err != nil {
		return Event{}, err
	}
	if err := decodeHex(encoded.Signature, e.Signature[:]); err != nil {
		return Event{}, err
	}
	e.CreatedAt, e.Kind, e.Tags, e.Content = encoded.CreatedAt, encoded.Kind, encoded.Tags, encoded.Content
	return e, nil
}
