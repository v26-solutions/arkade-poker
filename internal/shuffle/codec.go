package shuffle

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/btcsuite/btcd/btcec/v2"
)

// Compact points are exactly 33-byte compressed SEC1; the all-zero 33 bytes
// represent infinity only where explicitly allowed. Affine points are exactly
// x_le[32] || y_le[32], with (0,0) infinity. Public keys, c1, reveal tokens and
// proof commitments must be finite; only ciphertext c2 may be infinity here.
// Parsing checks coordinates without reduction. These codecs establish syntax,
// never key ownership, shuffle provenance, a valid proof or session binding.
func decodePoint(data []byte, allowInfinity bool) (point, error) {
	if len(data) != 33 {
		return point{}, fmt.Errorf("shuffle: point must be 33 bytes")
	}
	if bytes.Equal(data, make([]byte, 33)) {
		if allowInfinity {
			return point{}, nil
		}
		return point{}, fmt.Errorf("shuffle: point must be finite")
	}
	if data[0] != 2 && data[0] != 3 {
		return point{}, fmt.Errorf("shuffle: point must be compressed SEC1")
	}
	key, err := btcec.ParsePubKey(data)
	if err != nil {
		return point{}, fmt.Errorf("shuffle: invalid point: %w", err)
	}
	var p point
	key.AsJacobian(&p.value)
	return p, nil
}
func pointFromAffine(data [64]byte, allowInfinity bool) (point, error) {
	if data == ([64]byte{}) {
		if allowInfinity {
			return point{}, nil
		}
		return point{}, fmt.Errorf("shuffle: point must be finite")
	}
	var sec [65]byte
	sec[0] = 4
	copy(sec[1:], data[:])
	slices.Reverse(sec[1:33])
	slices.Reverse(sec[33:])
	key, err := btcec.ParsePubKey(sec[:])
	if err != nil {
		return point{}, fmt.Errorf("shuffle: invalid affine point: %w", err)
	}
	var p point
	key.AsJacobian(&p.value)
	return p, nil
}
func (p point) key(allowInfinity bool) (*btcec.PublicKey, error) {
	if p.value.Z.IsZero() {
		if allowInfinity {
			return nil, nil
		}
		return nil, fmt.Errorf("shuffle: point must be finite")
	}
	p.value.ToAffine()
	key := btcec.NewPublicKey(&p.value.X, &p.value.Y)
	if !key.IsOnCurve() {
		return nil, fmt.Errorf("shuffle: invalid point")
	}
	return key, nil
}
func (p point) compact(allowInfinity bool) ([]byte, error) {
	key, err := p.key(allowInfinity)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return make([]byte, 33), nil
	}
	return key.SerializeCompressed(), nil
}
func (p point) affine(allowInfinity bool) ([64]byte, error) {
	var out [64]byte
	key, err := p.key(allowInfinity)
	if err != nil {
		return out, err
	}
	if key == nil {
		return out, nil
	}
	copy(out[:], key.SerializeUncompressed()[1:])
	slices.Reverse(out[:32])
	slices.Reverse(out[32:])
	return out, nil
}

// Protocol scalars use exactly 32 little-endian bytes and must be < n. Zero is
// valid for a proof response. Secret-key admission additionally rejects it.
func decodeScalar(data []byte) (scalar, error) {
	if len(data) != 32 {
		return scalar{}, fmt.Errorf("shuffle: scalar must be 32 bytes")
	}
	b := [32]byte(data)
	defer clear(b[:])
	slices.Reverse(b[:])
	var s scalar
	if s.value.SetBytes(&b) != 0 {
		s.value.Zero()
		return scalar{}, fmt.Errorf("shuffle: scalar out of range")
	}
	return s, nil
}
func DecodePublicKey(data []byte) (PublicKey, error) {
	p, err := decodePoint(data, false)
	return PublicKey{point: p}, err
}
func (k PublicKey) MarshalBinary() ([]byte, error) { return k.point.compact(false) }
func (k PublicKey) AffineBytes() ([64]byte, error) { return k.point.affine(false) }
func PublicKeyFromAffine(data [64]byte) (PublicKey, error) {
	p, err := pointFromAffine(data, false)
	return PublicKey{point: p}, err
}

// Masked cards use c1 || c2, in compact (66-byte) or affine (128-byte) form.
func DecodeMaskedCard(data []byte) (MaskedCard, error) {
	if len(data) != 66 {
		return MaskedCard{}, fmt.Errorf("shuffle: masked card must be 66 bytes")
	}
	a, err := decodePoint(data[:33], false)
	if err != nil {
		return MaskedCard{}, err
	}
	b, err := decodePoint(data[33:], true)
	if err != nil {
		return MaskedCard{}, err
	}
	return MaskedCard{a, b}, nil
}
func (c MaskedCard) MarshalBinary() ([]byte, error) {
	a, err := c.c1.compact(false)
	if err != nil {
		return nil, err
	}
	b, err := c.c2.compact(true)
	if err != nil {
		return nil, err
	}
	return append(a, b...), nil
}
func (c MaskedCard) AffineBytes() ([128]byte, error) {
	var out [128]byte
	a, err := c.c1.affine(false)
	if err != nil {
		return out, err
	}
	b, err := c.c2.affine(true)
	if err != nil {
		return out, err
	}
	copy(out[:64], a[:])
	copy(out[64:], b[:])
	return out, nil
}
func MaskedCardFromAffine(data [128]byte) (MaskedCard, error) {
	a, err := pointFromAffine([64]byte(data[:64]), false)
	if err != nil {
		return MaskedCard{}, err
	}
	b, err := pointFromAffine([64]byte(data[64:]), true)
	if err != nil {
		return MaskedCard{}, err
	}
	return MaskedCard{a, b}, nil
}
func DecodeRevealToken(data []byte) (RevealToken, error) {
	p, err := decodePoint(data, false)
	return RevealToken{point: p}, err
}
func (t RevealToken) MarshalBinary() ([]byte, error) { return t.point.compact(false) }
func (t RevealToken) AffineBytes() ([64]byte, error) { return t.point.affine(false) }
func RevealTokenFromAffine(data [64]byte) (RevealToken, error) {
	p, err := pointFromAffine(data, false)
	return RevealToken{point: p}, err
}

// Reveal proofs are t_g || t_c1 || z_le32: 98 compact bytes or 160 affine bytes.
// Neither decoding nor re-encoding proves either DLEQ equation.
func DecodeRevealProof(data []byte) (RevealProof, error) {
	if len(data) != 98 {
		return RevealProof{}, fmt.Errorf("shuffle: reveal proof must be 98 bytes")
	}
	if _, err := decodePoint(data[:33], false); err != nil {
		return RevealProof{}, err
	}
	if _, err := decodePoint(data[33:66], false); err != nil {
		return RevealProof{}, err
	}
	if _, err := decodeScalar(data[66:]); err != nil {
		return RevealProof{}, err
	}
	return RevealProof{encoded: bytes.Clone(data)}, nil
}
func (p RevealProof) MarshalBinary() ([]byte, error) {
	valid, err := DecodeRevealProof(p.encoded)
	if err != nil {
		return nil, err
	}
	return valid.encoded, nil
}
func (p RevealProof) AffineBytes() ([160]byte, error) {
	var out [160]byte
	data, err := p.MarshalBinary()
	if err != nil {
		return out, err
	}
	a, _ := decodePoint(data[:33], false)
	b, _ := decodePoint(data[33:66], false)
	aa, _ := a.affine(false)
	bb, _ := b.affine(false)
	copy(out[:64], aa[:])
	copy(out[64:128], bb[:])
	copy(out[128:], data[66:])
	return out, nil
}
func RevealProofFromAffine(data [160]byte) (RevealProof, error) {
	a, err := pointFromAffine([64]byte(data[:64]), false)
	if err != nil {
		return RevealProof{}, err
	}
	b, err := pointFromAffine([64]byte(data[64:128]), false)
	if err != nil {
		return RevealProof{}, err
	}
	if _, err := decodeScalar(data[128:]); err != nil {
		return RevealProof{}, err
	}
	aa, _ := a.compact(false)
	bb, _ := b.compact(false)
	encoded := append(aa, bb...)
	encoded = append(encoded, data[128:]...)
	return RevealProof{encoded: encoded}, nil
}
