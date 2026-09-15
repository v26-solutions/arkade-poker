// Package shuffle implements the reference mental-poker protocol on btcec's
// pure-Go secp256k1 backend. See protocol.md for formats and caller obligations.
package shuffle

import (
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"sync"

	"github.com/btcsuite/btcd/btcec/v2"
)

var (
	ErrInvalid = errors.New("shuffle: invalid value")
	ErrProof   = errors.New("shuffle: invalid proof")
	ErrSecret  = errors.New("shuffle: missing or destroyed secret")
)

// Values never expose the backend's mutable pointers. Infinity is the zero
// point; complete group algebra includes zero scalars and point cancellation.
// This backend deliberately makes no constant-time execution promise.
type point struct{ value btcec.JacobianPoint }
type scalar struct{ value btcec.ModNScalar }

func (p point) infinity() bool { return p.value.Z.IsZero() }
func (p point) add(q point) point {
	var out point
	btcec.AddNonConst(&p.value, &q.value, &out.value)
	return out
}
func (p point) neg() point {
	if p.infinity() {
		return point{}
	}
	p.value.Y.Normalize().Negate(1).Normalize()
	return p
}
func (p point) sub(q point) point { return p.add(q.neg()) }
func (p point) mul(s scalar) point {
	if p.infinity() || s.zero() {
		return point{}
	}
	var out point
	btcec.ScalarMultNonConst(&s.value, &p.value, &out.value)
	return out
}
func baseMul(s scalar) point {
	if s.zero() {
		return point{}
	}
	var out point
	btcec.ScalarBaseMultNonConst(&s.value, &out.value)
	return out
}
func (p point) equal(q point) bool {
	if p.infinity() || q.infinity() {
		return p.infinity() == q.infinity()
	}
	p.value.ToAffine()
	q.value.ToAffine()
	return p.value.X.Equals(&q.value.X) && p.value.Y.Equals(&q.value.Y)
}
func (p point) bytes() []byte        { b, _ := p.compact(true); return b }
func scalarInt(n uint32) scalar      { var s scalar; s.value.SetInt(n); return s }
func (s scalar) zero() bool          { return s.value.IsZero() }
func (s scalar) add(t scalar) scalar { s.value.Add(&t.value); return s }
func (s scalar) neg() scalar         { s.value.Negate(); return s }
func (s scalar) sub(t scalar) scalar { return s.add(t.neg()) }
func (s scalar) mul(t scalar) scalar { s.value.Mul(&t.value); return s }
func (s scalar) equal(t scalar) bool { return s.value.Equals(&t.value) }
func (s scalar) pow(n uint32) scalar {
	out := scalarInt(1)
	for n != 0 {
		if n&1 != 0 {
			out = out.mul(s)
		}
		n >>= 1
		s = s.mul(s)
	}
	return out
}
func (s scalar) bytes() []byte { b := s.value.Bytes(); slices.Reverse(b[:]); return b[:] }

// Reduction is only for public SHA256 challenges. Wire values and entropy use
// canonical parsing/rejection sampling and must never call this function.
func reduceDigest(b [32]byte) scalar { var s scalar; s.value.SetBytes(&b); return s }

type PublicKey struct{ point point }
type AggregatePublicKey struct{ point point }
type VerifiedPublicKey struct {
	key   PublicKey
	valid bool
}

// SecretKey is a handle: copying it shares destruction, never duplicates the
// stored scalar. Methods synchronize with Destroy. Temporary arithmetic copies
// are cleared on return, but Go does not guarantee erasure of compiler/GC copies.
// These keys are independently generated session keys, never wallet keys.
type SecretKey struct{ state *secretState }
type secretState struct {
	mu    sync.Mutex
	value scalar
}

func (k SecretKey) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "<shuffle secret>") }
func secretFromScalar(s scalar) *SecretKey     { return &SecretKey{state: &secretState{value: s}} }
func DecodeSecretKey(data []byte) (*SecretKey, error) {
	s, err := decodeScalar(data)
	defer s.value.Zero()
	if err != nil {
		return nil, err
	}
	if s.zero() {
		return nil, ErrSecret
	}
	return secretFromScalar(s), nil
}
func (k *SecretKey) snapshot() (scalar, error) {
	if k == nil || k.state == nil {
		return scalar{}, ErrSecret
	}
	k.state.mu.Lock()
	defer k.state.mu.Unlock()
	if k.state.value.zero() {
		return scalar{}, ErrSecret
	}
	return k.state.value, nil
}
func (k *SecretKey) MarshalBinary() ([]byte, error) {
	s, err := k.snapshot()
	defer s.value.Zero()
	if err != nil {
		return nil, err
	}
	return s.bytes(), nil
}
func (k *SecretKey) PublicKey() (PublicKey, error) {
	s, err := k.snapshot()
	defer s.value.Zero()
	if err != nil {
		return PublicKey{}, err
	}
	return PublicKey{baseMul(s)}, nil
}

// Destroy is idempotent. Already-running operations can finish with their
// private snapshot; future operations through any copied handle fail.
func (k *SecretKey) Destroy() error {
	if k == nil || k.state == nil {
		return nil
	}
	k.state.mu.Lock()
	defer k.state.mu.Unlock()
	k.state.value.value.Zero()
	runtime.KeepAlive(k)
	return nil
}
func AggregateKeys(keys []VerifiedPublicKey) (AggregatePublicKey, error) {
	if len(keys) == 0 {
		return AggregatePublicKey{}, ErrInvalid
	}
	var sum point
	for _, k := range keys {
		if !k.valid || k.key.point.infinity() {
			return AggregatePublicKey{}, ErrInvalid
		}
		sum = sum.add(k.key.point)
	}
	if sum.infinity() {
		return AggregatePublicKey{}, ErrInvalid
	}
	return AggregatePublicKey{sum}, nil
}

// MarshalBinary permits context binding, not reconstruction of verified keys.
func (k AggregatePublicKey) MarshalBinary() ([]byte, error) { return k.point.compact(false) }
func (k VerifiedPublicKey) PublicKey() (PublicKey, error) {
	if !k.valid {
		return PublicKey{}, ErrInvalid
	}
	return k.key, nil
}
