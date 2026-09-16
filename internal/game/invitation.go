package game

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"strings"
)

const (
	invitationPrefix         = "arkpg1:"
	invitationChecksumBytes  = 8
	maxSharedInvitationBytes = 4*binary.MaxVarintLen64 + 3*32 + binary.MaxVarintLen64 + 2048 + invitationChecksumBytes
)

// EncodeInvitation compacts the sharing representation only. Session IDs,
// ownership proofs and stored events still commit to invitationBinary's bytes.
func EncodeInvitation(inv Invitation) (string, error) {
	canonical, err := invitationBinary(inv)
	if err != nil {
		return "", err
	}
	if SessionID(digest("arkade-poker/session/v1", canonical)) != inv.SessionID {
		return "", ErrInvitation
	}
	var b []byte
	for _, n := range []int64{inv.Terms.Stake, inv.Terms.Bond, inv.Terms.MinBet, inv.Terms.MaxWager} {
		b = binary.AppendUvarint(b, uint64(n))
	}
	b = append(b, inv.ServiceBinding[:]...)
	b = append(b, inv.CreatorTransportKey[:]...)
	b = append(b, inv.JoinCapability[:]...)
	b = binary.AppendUvarint(b, uint64(len(inv.RelayURL)))
	b = append(b, inv.RelayURL...)
	// Carry the first eight bytes of the canonical SHA256 checksum. This is
	// corruption detection; neither the full nor shortened checksum authenticates.
	b = append(b, canonical[len(canonical)-32:len(canonical)-32+invitationChecksumBytes]...)
	return invitationPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func DecodeInvitation(s string) (Invitation, error) {
	s, ok := strings.CutPrefix(s, invitationPrefix)
	if !ok || len(s) > base64.RawURLEncoding.EncodedLen(maxSharedInvitationBytes) {
		return Invitation{}, ErrInvitation
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	// Go's base64 decoder ignores CR/LF even in strict mode. Sharing accepts
	// only the exact unpadded URL alphabet and canonical trailing bits.
	if err != nil || base64.RawURLEncoding.EncodeToString(b) != s || len(b) < invitationChecksumBytes {
		return Invitation{}, ErrInvitation
	}
	r := &decoder{data: b[:len(b)-invitationChecksumBytes]}
	inv := Invitation{Terms: Terms{int64(r.uvarint()), int64(r.uvarint()), int64(r.uvarint()), int64(r.uvarint())}}
	copy(inv.ServiceBinding[:], r.raw(32))
	copy(inv.CreatorTransportKey[:], r.raw(32))
	copy(inv.JoinCapability[:], r.raw(32))
	n := r.uvarint()
	if r.err != nil || n > 2048 || n > uint64(len(r.data)-r.pos) {
		return Invitation{}, ErrInvitation
	}
	inv.RelayURL = string(r.raw(int(n)))
	if err := r.done(); err != nil {
		return Invitation{}, ErrInvitation
	}
	canonical, err := invitationBinary(inv)
	if err != nil {
		return Invitation{}, err
	}
	if !bytes.Equal(b[len(b)-invitationChecksumBytes:], canonical[len(canonical)-32:len(canonical)-32+invitationChecksumBytes]) {
		return Invitation{}, ErrInvitation
	}
	inv.SessionID = SessionID(digest("arkade-poker/session/v1", canonical))
	return inv, nil
}

// uvarint is used only by compact invitation sharing. All record lengths and
// amounts retain their fixed-width encoding. Reject overflow and overlong forms.
func (r *decoder) uvarint() uint64 {
	if r.err != nil {
		return 0
	}
	n, size := binary.Uvarint(r.data[r.pos:])
	if size <= 0 || size > 1 && r.data[r.pos+size-1] == 0 {
		r.err = ErrEncoding
		return 0
	}
	r.pos += size
	return n
}
