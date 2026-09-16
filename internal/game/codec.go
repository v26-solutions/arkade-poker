package game

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"unicode/utf8"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/shuffle"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

const (
	MaxMessageBytes  = 64 * 1024
	MaxEventBytes    = 4 * 1024 * 1024
	maxScriptBytes   = 10000
	invitationDomain = "arkade-poker/invitation\x00"
	messageDomain    = "arkade-poker/message\x00"
	eventDomain      = "arkade-poker/private-event\x00"
	configDomain     = "arkade-poker/config\x00"
)

// All lengths are u32le; fixed fields have no length prefix. Readers bound a
// length against both its field limit and remaining bytes before allocating.
type encoder struct {
	bytes.Buffer
	err error
}

func (w *encoder) raw(b []byte) {
	if w.err == nil {
		_, w.err = w.Write(b)
	}
}
func (w *encoder) u8(n byte)    { w.raw([]byte{n}) }
func (w *encoder) u16(n uint16) { var b [2]byte; binary.LittleEndian.PutUint16(b[:], n); w.raw(b[:]) }
func (w *encoder) u32(n uint32) { var b [4]byte; binary.LittleEndian.PutUint32(b[:], n); w.raw(b[:]) }
func (w *encoder) u64(n uint64) { var b [8]byte; binary.LittleEndian.PutUint64(b[:], n); w.raw(b[:]) }
func (w *encoder) blob(b []byte, max int) {
	if len(b) > max {
		w.err = ErrEncoding
		return
	}
	w.u32(uint32(len(b)))
	w.raw(b)
}
func (w *encoder) str(s string, max int) {
	if !utf8.ValidString(s) {
		w.err = ErrEncoding
		return
	}
	w.blob([]byte(s), max)
}
func (w *encoder) crypto(v interface{ MarshalBinary() ([]byte, error) }) {
	b, err := v.MarshalBinary()
	if err != nil {
		w.err = err
		return
	}
	w.raw(b)
}
func (w *encoder) finish() ([]byte, error) {
	if w.err != nil {
		return nil, w.err
	}
	return bytes.Clone(w.Bytes()), nil
}
func (w *encoder) header(domain string) { w.raw([]byte(domain)); w.u16(1) }
func (w *encoder) checksum() {
	if w.err == nil {
		sum := sha256.Sum256(w.Bytes())
		w.raw(sum[:])
	}
}

type decoder struct {
	data []byte
	pos  int
	err  error
}

func (r *decoder) raw(n int) []byte {
	if r.err != nil {
		return make([]byte, n)
	}
	if n < 0 || n > len(r.data)-r.pos {
		r.err = ErrEncoding
		return make([]byte, n)
	}
	b := r.data[r.pos : r.pos+n]
	r.pos += n
	return b
}
func (r *decoder) u8() byte    { return r.raw(1)[0] }
func (r *decoder) u16() uint16 { return binary.LittleEndian.Uint16(r.raw(2)) }
func (r *decoder) u32() uint32 { return binary.LittleEndian.Uint32(r.raw(4)) }
func (r *decoder) u64() uint64 { return binary.LittleEndian.Uint64(r.raw(8)) }
func (r *decoder) blob(max int) []byte {
	n := r.u32()
	if r.err != nil || uint64(n) > uint64(max) || uint64(n) > uint64(len(r.data)-r.pos) {
		r.err = ErrEncoding
		return nil
	}
	return bytes.Clone(r.raw(int(n)))
}
func (r *decoder) str(max int) string {
	b := r.blob(max)
	if !utf8.Valid(b) {
		r.err = ErrEncoding
	}
	return string(b)
}
func (r *decoder) done() error {
	if r.err != nil || r.pos != len(r.data) {
		return ErrEncoding
	}
	return nil
}
func (r *decoder) header(domain string) {
	if !bytes.Equal(r.raw(len(domain)), []byte(domain)) || r.u16() != 1 {
		r.err = ErrEncoding
	}
}
func checkedBody(data []byte, max int) ([]byte, error) {
	if len(data) < 32 || len(data) > max {
		return nil, ErrEncoding
	}
	body := data[:len(data)-32]
	sum := sha256.Sum256(body)
	if !bytes.Equal(sum[:], data[len(body):]) {
		return nil, ErrEncoding
	}
	return body, nil
}
func digest(domain string, parts ...[]byte) [32]byte {
	h := sha256.New()
	_, _ = io.WriteString(h, domain)
	for _, b := range parts {
		_, _ = h.Write(b)
	}
	return [32]byte(h.Sum(nil))
}
func validXOnly(b [32]byte) bool { _, err := schnorr.ParsePubKey(b[:]); return err == nil }

func invitationBinary(inv Invitation) ([]byte, error) {
	if err := inv.Terms.Validate(); err != nil {
		return nil, err
	}
	if err := validateRelay(inv.RelayURL); err != nil {
		return nil, err
	}
	if !validXOnly(inv.CreatorTransportKey) {
		return nil, ErrInvitation
	}
	w := new(encoder)
	w.header(invitationDomain)
	for _, n := range []int64{inv.Terms.Stake, inv.Terms.Bond, inv.Terms.MinBet, inv.Terms.MaxWager} {
		w.u64(uint64(n))
	}
	w.raw(inv.ServiceBinding[:])
	w.raw(inv.CreatorTransportKey[:])
	w.raw(inv.JoinCapability[:])
	w.str(inv.RelayURL, 2048)
	w.checksum()
	return w.finish()
}
func invitationID(inv Invitation) (SessionID, error) {
	b, err := invitationBinary(inv)
	if err != nil {
		return SessionID{}, err
	}
	return SessionID(digest("arkade-poker/session/v1", b)), nil
}
func decodeInvitationBinary(data []byte) (Invitation, error) {
	body, err := checkedBody(data, 4096)
	if err != nil {
		return Invitation{}, err
	}
	r := &decoder{data: body}
	r.header(invitationDomain)
	inv := Invitation{Terms: Terms{int64(r.u64()), int64(r.u64()), int64(r.u64()), int64(r.u64())}}
	copy(inv.ServiceBinding[:], r.raw(32))
	copy(inv.CreatorTransportKey[:], r.raw(32))
	copy(inv.JoinCapability[:], r.raw(32))
	inv.RelayURL = r.str(2048)
	if err := r.done(); err != nil {
		return Invitation{}, err
	}
	b, err := invitationBinary(inv)
	if err != nil {
		return Invitation{}, err
	}
	if !bytes.Equal(b, data) {
		return Invitation{}, ErrEncoding
	}
	inv.SessionID = SessionID(digest("arkade-poker/session/v1", b))
	return inv, nil
}

func encodeParticipant(w *encoder, p covenant.Participant) {
	if !validXOnly(p.SigningKey) {
		w.err = ErrEncoding
		return
	}
	w.raw(p.SigningKey[:])
	w.crypto(p.EncryptionKey)
	w.blob(p.PayoutScript, maxScriptBytes)
}
func decodeParticipant(r *decoder) covenant.Participant {
	var p covenant.Participant
	copy(p.SigningKey[:], r.raw(32))
	key, err := shuffle.DecodePublicKey(r.raw(33))
	if err != nil {
		r.err = err
	}
	p.EncryptionKey = key
	p.PayoutScript = r.blob(maxScriptBytes)
	if !validXOnly(p.SigningKey) {
		r.err = ErrEncoding
	}
	return p
}
func EncodeMessage(m Message) ([]byte, error) {
	if !validXOnly(m.Identity) || (m.Sender != covenant.Player1 && m.Sender != covenant.Player2) {
		return nil, ErrEncoding
	}
	w := new(encoder)
	w.header(messageDomain)
	w.raw(m.SessionID[:])
	w.u8(byte(m.Sender))
	w.raw(m.Identity[:])
	w.u64(m.Sequence)
	w.u8(byte(m.Kind))
	switch m.Kind {
	case KeyOffer, KeyReply:
		if m.Participant == nil || m.Ownership == nil || m.Deck != nil || m.ShuffleProof != nil || m.InitialDeadline != 0 {
			return nil, ErrEncoding
		}
		encodeParticipant(w, *m.Participant)
		w.crypto(*m.Ownership)
		w.blob(m.WalletOwnership, MaxMessageBytes)
	case InitialShuffle, FinalShuffle:
		if m.Participant != nil || m.Ownership != nil || len(m.WalletOwnership) != 0 || m.Deck == nil || m.ShuffleProof == nil || m.Kind == InitialShuffle && m.InitialDeadline != 0 {
			return nil, ErrEncoding
		}
		w.crypto(*m.Deck)
		w.crypto(*m.ShuffleProof)
		if m.Kind == FinalShuffle {
			w.u64(uint64(m.InitialDeadline))
		}
	default:
		return nil, ErrEncoding
	}
	b, err := w.finish()
	if len(b) > MaxMessageBytes {
		return nil, ErrEncoding
	}
	return b, err
}
func DecodeMessage(data []byte) (Message, error) {
	if len(data) > MaxMessageBytes {
		return Message{}, ErrEncoding
	}
	r := &decoder{data: data}
	r.header(messageDomain)
	var m Message
	copy(m.SessionID[:], r.raw(32))
	m.Sender = covenant.Player(r.u8())
	copy(m.Identity[:], r.raw(32))
	m.Sequence = r.u64()
	m.Kind = MessageKind(r.u8())
	switch m.Kind {
	case KeyOffer, KeyReply:
		if r.err != nil || len(data)-r.pos < 32+33+4+65+4 {
			return Message{}, ErrEncoding
		}
		p := decodeParticipant(r)
		m.Participant = &p
		proof, err := shuffle.DecodeOwnershipProof(r.raw(65))
		if err != nil {
			r.err = err
		}
		m.Ownership = &proof
		m.WalletOwnership = r.blob(MaxMessageBytes)
	case InitialShuffle, FinalShuffle:
		expected := shuffle.DeckSize*66 + shuffle.ShuffleProofSize
		if m.Kind == FinalShuffle {
			expected += 8
		}
		if r.err != nil || len(data)-r.pos != expected {
			return Message{}, ErrEncoding
		}
		deck, err := shuffle.DecodeDeck(r.raw(shuffle.DeckSize * 66))
		if err != nil {
			r.err = err
		}
		m.Deck = &deck
		proof, err := shuffle.DecodeShuffleProof(r.raw(shuffle.ShuffleProofSize))
		if err != nil {
			r.err = err
		}
		m.ShuffleProof = &proof
		if m.Kind == FinalShuffle {
			m.InitialDeadline = covenant.UnixSeconds(r.u64())
		}
	default:
		return Message{}, ErrEncoding
	}
	if err := r.done(); err != nil {
		return Message{}, err
	}
	b, err := EncodeMessage(m)
	if err != nil {
		return Message{}, err
	}
	if !bytes.Equal(b, data) {
		return Message{}, ErrEncoding
	}
	return m, nil
}
