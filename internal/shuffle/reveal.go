package shuffle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
)

type OwnershipProof struct{ encoded []byte }
type RevealToken struct{ point point }
type RevealProof struct{ encoded []byte }
type VerifiedRevealToken struct {
	token RevealToken
	valid bool
}
type AggregateRevealToken struct{ point point }

func ownershipChallenge(key PublicKey, a point, binding []byte) scalar {
	return newTranscript(binding).append("pk", key.point.bytes()).append("a", a.bytes()).challenge("ziffle/DLOG/v2", 0)
}

// GenerateKey uses rejection-sampled nonzero secrets and nonces. Nil entropy
// selects crypto/rand.Reader. Callers supplying a reader must use secure entropy.
func GenerateKey(ctx context.Context, entropy io.Reader, binding []byte) (*SecretKey, PublicKey, OwnershipProof, error) {
	if err := validBinding(binding); err != nil {
		return nil, PublicKey{}, OwnershipProof{}, err
	}
	w := newWork(ctx, entropy)
	sk, nonce := w.random(true), w.random(true)
	defer sk.value.Zero()
	defer nonce.value.Zero()
	if w.err != nil {
		return nil, PublicKey{}, OwnershipProof{}, w.err
	}
	key := PublicKey{baseMul(sk)}
	a := baseMul(nonce)
	z := nonce.add(ownershipChallenge(key, a, binding).mul(sk))
	if !w.check() {
		return nil, PublicKey{}, OwnershipProof{}, w.err
	}
	return secretFromScalar(sk), key, OwnershipProof{append(a.bytes(), z.bytes()...)}, nil
}
func VerifyOwnership(key PublicKey, proof OwnershipProof, binding []byte) (VerifiedPublicKey, error) {
	if err := validBinding(binding); err != nil {
		return VerifiedPublicKey{}, err
	}
	if key.point.infinity() {
		return VerifiedPublicKey{}, ErrInvalid
	}
	data, err := proof.MarshalBinary()
	if err != nil {
		return VerifiedPublicKey{}, err
	}
	a, _ := decodePoint(data[:33], false)
	z, _ := decodeScalar(data[33:])
	e := ownershipChallenge(key, a, binding)
	if !baseMul(z).equal(a.add(key.point.mul(e))) {
		return VerifiedPublicKey{}, ErrProof
	}
	return VerifiedPublicKey{key: key, valid: true}, nil
}

// This separate transcript is also executed by covenant bytecode. The variable
// binding precedes exactly five fixed 64-byte affine points, so framing is
// unambiguous. Keep this order, endian convention and minus-sign response.
func revealChallenge(key PublicKey, share, c1, tg, tc1 point, binding []byte) scalar {
	h := sha256.New()
	h.Write([]byte("ziffle/DLEQ/v2"))
	h.Write(binding)
	for _, p := range []point{key.point, share, c1, tg, tc1} {
		b, _ := p.affine(false)
		h.Write(b[:])
	}
	return reduceDigest([32]byte(h.Sum(nil)))
}
func Reveal(ctx context.Context, entropy io.Reader, secret *SecretKey, card MaskedCard, binding []byte) (RevealToken, RevealProof, error) {
	if err := validBinding(binding); err != nil {
		return RevealToken{}, RevealProof{}, err
	}
	if card.c1.infinity() {
		return RevealToken{}, RevealProof{}, ErrInvalid
	}
	sk, err := secret.snapshot()
	defer sk.value.Zero()
	if err != nil {
		return RevealToken{}, RevealProof{}, err
	}
	w := newWork(ctx, entropy)
	nonce := w.random(true)
	defer nonce.value.Zero()
	if w.err != nil {
		return RevealToken{}, RevealProof{}, w.err
	}
	pk := PublicKey{baseMul(sk)}
	share, tg, tc1 := card.c1.mul(sk), baseMul(nonce), card.c1.mul(nonce)
	e := revealChallenge(pk, share, card.c1, tg, tc1, binding)
	z := nonce.sub(e.mul(sk))
	if !w.check() {
		return RevealToken{}, RevealProof{}, w.err
	}
	b := append(tg.bytes(), tc1.bytes()...)
	b = append(b, z.bytes()...)
	return RevealToken{share}, RevealProof{b}, nil
}
func VerifyReveal(key VerifiedPublicKey, card MaskedCard, token RevealToken, proof RevealProof, binding []byte) (VerifiedRevealToken, error) {
	if err := validBinding(binding); err != nil {
		return VerifiedRevealToken{}, err
	}
	if !key.valid || key.key.point.infinity() || card.c1.infinity() || token.point.infinity() {
		return VerifiedRevealToken{}, ErrInvalid
	}
	data, err := proof.MarshalBinary()
	if err != nil {
		return VerifiedRevealToken{}, err
	}
	tg, _ := decodePoint(data[:33], false)
	tc1, _ := decodePoint(data[33:66], false)
	z, _ := decodeScalar(data[66:])
	e := revealChallenge(key.key, token.point, card.c1, tg, tc1, binding)
	if !tg.equal(baseMul(z).add(key.key.point.mul(e))) || !tc1.equal(card.c1.mul(z).add(token.point.mul(e))) {
		return VerifiedRevealToken{}, ErrProof
	}
	return VerifiedRevealToken{token: token, valid: true}, nil
}

// As in the reference, the caller selects the required players and shares for
// the same hand/card. Different players' semantic slots have different bindings.
func AggregateReveals(tokens []VerifiedRevealToken) (AggregateRevealToken, error) {
	if len(tokens) == 0 {
		return AggregateRevealToken{}, ErrInvalid
	}
	var sum point
	for _, t := range tokens {
		if !t.valid || t.token.point.infinity() {
			return AggregateRevealToken{}, ErrInvalid
		}
		sum = sum.add(t.token.point)
	}
	if sum.infinity() {
		return AggregateRevealToken{}, ErrInvalid
	}
	return AggregateRevealToken{sum}, nil
}

// RevealCard decodes precisely (index+1)*G for indices 0..51.
func (p *Protocol) RevealCard(card MaskedCard, token AggregateRevealToken) (byte, error) {
	if p == nil || !p.parameters.valid || card.c1.infinity() || token.point.infinity() {
		return 0, ErrInvalid
	}
	plaintext := card.c2.sub(token.point)
	for i, pt := range p.open {
		if plaintext.equal(pt) {
			return byte(i), nil
		}
	}
	return 0, fmt.Errorf("shuffle: plaintext is not a card")
}

// RevealBinding is the reference's fixed 55-byte contract/semantic-slot binding.
// Slots 0..17 follow covenant's publication identities, not deck positions.
func RevealBinding(contractID [32]byte, slot uint8) ([]byte, error) {
	if slot >= 18 {
		return nil, fmt.Errorf("shuffle: invalid reveal slot")
	}
	b := append([]byte("arkade/poker/reveal/v1"), contractID[:]...)
	return append(b, slot), nil
}
func DecodeOwnershipProof(data []byte) (OwnershipProof, error) {
	if len(data) != 65 {
		return OwnershipProof{}, fmt.Errorf("shuffle: ownership proof must be 65 bytes")
	}
	if _, err := decodePoint(data[:33], false); err != nil {
		return OwnershipProof{}, err
	}
	if _, err := decodeScalar(data[33:]); err != nil {
		return OwnershipProof{}, err
	}
	return OwnershipProof{bytes.Clone(data)}, nil
}
func (p OwnershipProof) MarshalBinary() ([]byte, error) {
	valid, err := DecodeOwnershipProof(p.encoded)
	return valid.encoded, err
}
