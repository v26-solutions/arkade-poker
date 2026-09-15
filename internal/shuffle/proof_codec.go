package shuffle

import (
	"bytes"
	"fmt"
)

const ShuffleProofSize = 555 + 96*DeckSize

// BG12 compact v2: c_pi, c_xpi, c_alpha, c_beta, ct_mxp0(c1,c2),
// ct_mxp1(c1,c2), o_alpha[52], o_r, beta, o_beta, tau, c_d, c_sdelta,
// c_cdelta, a_tilde[52], b_tilde[52], r_tilde, s_tilde. All 11 points use
// complete group encoding (including infinity); all 162 scalars are canonical
// LE32. Exact size is 5547 bytes. Neither parsing nor re-encoding verifies it.
func parseShuffleProof(data []byte) (bgProof, error) {
	var p bgProof
	if len(data) != ShuffleProofSize {
		return p, fmt.Errorf("shuffle: proof must be %d bytes", ShuffleProofSize)
	}
	offset := 0
	readPoint := func(dst *point) error {
		q, err := decodePoint(data[offset:][:33], true)
		offset += 33
		*dst = q
		return err
	}
	readScalar := func(dst *scalar) error {
		s, err := decodeScalar(data[offset:][:32])
		offset += 32
		*dst = s
		return err
	}
	m, a := &p.multi, &p.product
	for _, dst := range []*point{&p.cPi, &p.cXPi, &m.cAlpha, &m.cBeta, &m.ct0.c1, &m.ct0.c2, &m.ct1.c1, &m.ct1.c2} {
		if err := readPoint(dst); err != nil {
			return bgProof{}, fmt.Errorf("shuffle: proof point at %d: %w", offset-33, err)
		}
	}
	for i := range m.oAlpha {
		if err := readScalar(&m.oAlpha[i]); err != nil {
			return bgProof{}, err
		}
	}
	for _, dst := range []*scalar{&m.oR, &m.beta, &m.oBeta, &m.tau} {
		if err := readScalar(dst); err != nil {
			return bgProof{}, err
		}
	}
	for _, dst := range []*point{&a.cD, &a.cSmallDelta, &a.cCapitalDelta} {
		if err := readPoint(dst); err != nil {
			return bgProof{}, err
		}
	}
	for _, vec := range []*[DeckSize]scalar{&a.aTilde, &a.bTilde} {
		for i := range vec {
			if err := readScalar(&vec[i]); err != nil {
				return bgProof{}, err
			}
		}
	}
	for _, dst := range []*scalar{&a.rTilde, &a.sTilde} {
		if err := readScalar(dst); err != nil {
			return bgProof{}, err
		}
	}
	return p, nil
}
func (p bgProof) encode() []byte {
	out := make([]byte, 0, ShuffleProofSize)
	m, a := p.multi, p.product
	for _, q := range []point{p.cPi, p.cXPi, m.cAlpha, m.cBeta, m.ct0.c1, m.ct0.c2, m.ct1.c1, m.ct1.c2} {
		out = append(out, q.bytes()...)
	}
	for _, s := range m.oAlpha {
		out = append(out, s.bytes()...)
	}
	for _, s := range []scalar{m.oR, m.beta, m.oBeta, m.tau} {
		out = append(out, s.bytes()...)
	}
	for _, q := range []point{a.cD, a.cSmallDelta, a.cCapitalDelta} {
		out = append(out, q.bytes()...)
	}
	for _, vec := range [][DeckSize]scalar{a.aTilde, a.bTilde} {
		for _, s := range vec {
			out = append(out, s.bytes()...)
		}
	}
	for _, s := range []scalar{a.rTilde, a.sTilde} {
		out = append(out, s.bytes()...)
	}
	return out
}
func DecodeShuffleProof(data []byte) (ShuffleProof, error) {
	if _, err := parseShuffleProof(data); err != nil {
		return ShuffleProof{}, err
	}
	return ShuffleProof{bytes.Clone(data)}, nil
}
func (p ShuffleProof) MarshalBinary() ([]byte, error) {
	valid, err := DecodeShuffleProof(p.encoded)
	return valid.encoded, err
}
