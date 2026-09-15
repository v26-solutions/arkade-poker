package shuffle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"
)

func splitmix(state *uint64) uint64 {
	*state += 0x9e3779b97f4a7c15
	v := *state
	v = (v ^ (v >> 30)) * 0xbf58476d1ce4e5b9
	v = (v ^ (v >> 27)) * 0x94d049bb133111eb
	return v ^ (v >> 31)
}
func oracleDraw(state *uint64) ([8]uint64, [32]byte, [32]byte) {
	var words [8]uint64
	var a, b [32]byte
	for i := range words {
		words[i] = splitmix(state)
	}
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint64(a[i*8:], words[i])
		binary.LittleEndian.PutUint64(b[i*8:], words[i+4])
	}
	return words, a, b
}
func TestIndependentArithmeticOracles(t *testing.T) {
	// Frozen independent Python integer/affine oracles, also used by Rust.
	state := uint64(0x6a09e667f3bcc909)
	h := sha256.New()
	for range 2048 {
		words, a, b := oracleDraw(&state)
		left, right := reduceDigest(a), reduceDigest(b)
		var result scalar
		op := byte(words[0] % 7)
		switch op {
		case 0:
			result = left.add(right)
		case 1:
			result = left.sub(right)
		case 2:
			result = left.mul(right)
		case 3:
			result = left.neg()
		case 4:
			result = left.pow(uint32(words[1] & 127))
		case 5:
			result = right
		case 6:
			result = left
		}
		h.Write([]byte{op})
		be := result.value.Bytes()
		h.Write(be[:])
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != "70f557f33d5238c00a410892cbe8d92b8fd7501ab8c24cc4d916a8e8df1f7d9b" {
		t.Fatal("integer oracle")
	}
	state = 0x510e527fade682d1
	h.Reset()
	for range 256 {
		_, a, b := oracleDraw(&state)
		left, right := reduceDigest(a), reduceDigest(b)
		if left.zero() {
			left = scalarInt(1)
		}
		if right.zero() {
			right = scalarInt(1)
		}
		product := baseMul(left).mul(right)
		if !product.equal(baseMul(left.mul(right))) {
			t.Fatal("ECDH/generator mismatch")
		}
		la, rb := left.value.Bytes(), right.value.Bytes()
		h.Write(la[:])
		h.Write(rb[:])
		h.Write(product.bytes())
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != "ed4185af98b95874b227b71af669a2c25a4cade0bd260e847630fde2b7f497db" {
		t.Fatal("affine oracle")
	}
	p := must(New())
	h.Reset()
	for _, q := range p.open {
		h.Write(q.bytes())
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != "5516491e9d9e019e1c0b3b6723c6d615c633e76bc090b23d02d8e3d3dc6bb0fb" {
		t.Fatal("card mapping")
	}
}
func TestCompleteGroupAndScalarBoundaries(t *testing.T) {
	g, z := testPoint(1), point{}
	for _, p := range []point{g.add(z), z.add(g), g.add(g).sub(g), g.neg().neg()} {
		if !p.equal(g) {
			t.Fatal("point identity")
		}
	}
	for _, p := range []point{g.sub(g), g.add(g.neg()), g.mul(scalar{}), z.mul(scalarInt(7)), baseMul(scalar{})} {
		if !p.infinity() {
			t.Fatal("point cancellation/zero")
		}
	}
	one := scalarInt(1)
	order := [32]byte(unhex("fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141"))
	if !reduceDigest(order).zero() {
		t.Fatal("n reduction")
	}
	order[31]++
	if !reduceDigest(order).equal(one) {
		t.Fatal("n+1 reduction")
	}
	order[31] -= 2
	if !reduceDigest(order).equal(one.neg()) {
		t.Fatal("n-1 reduction")
	}
	if !one.add(one.neg()).zero() || !one.mul(scalar{}).zero() || !one.neg().mul(one.neg()).equal(one) {
		t.Fatal("scalar zero/cancellation")
	}
	if !baseMul(one.neg()).equal(g.neg()) {
		t.Fatal("negative scalar")
	}
	field := unhex("fffffffffffffffffffffffffffffffffffffffffffffffffffffffefffffc2f")
	if _, err := decodePoint(append([]byte{2}, field...), true); err == nil {
		t.Fatal("x == p accepted")
	}
	affine := must(g.affine(false))
	slices.Reverse(field)
	copy(affine[:32], field)
	if _, err := pointFromAffine(affine, true); err == nil {
		t.Fatal("affine x == p accepted")
	}
}
func TestParameterIntegrityAndCommitmentAlgebra(t *testing.T) {
	a, b := must(LoadParameters()), must(LoadParameters())
	if !a.valid || !b.valid {
		t.Fatal("invalid parameters")
	}
	a.bases[0] = point{}
	if b.bases[0].infinity() || must(LoadParameters()).bases[0].infinity() {
		t.Fatal("parameter alias")
	}
	for _, data := range [][]byte{nil, parameterBytes[:len(parameterBytes)-1], append(bytes.Clone(parameterBytes), 0)} {
		if _, err := parseParameters(data); err == nil {
			t.Fatal("parameter length")
		}
	}
	altered := bytes.Clone(parameterBytes)
	altered[5] ^= 1
	if _, err := parseParameters(altered); err == nil {
		t.Fatal("parameter checksum")
	}
	w := newWork(context.Background(), nil)
	var x, y, xy [DeckSize]scalar
	for i := range x {
		x[i] = scalarInt(uint32(i + 1))
		y[i] = scalarInt(uint32(2*i + 3))
		xy[i] = x[i].add(y[i])
	}
	r, s := scalarInt(7), scalarInt(11)
	if !b.vectorCommit(w, x, r).add(b.vectorCommit(w, y, s)).equal(b.vectorCommit(w, xy, r.add(s))) {
		t.Fatal("vector homomorphism")
	}
	if !b.commit(r, s).add(b.commit(s, r)).equal(b.commit(r.add(s), s.add(r))) {
		t.Fatal("scalar homomorphism")
	}
	if !b.vectorCommit(w, [DeckSize]scalar{}, scalar{}).infinity() || !b.commit(scalar{}, scalar{}).infinity() {
		t.Fatal("zero commitment")
	}
}

type zeroReader struct{}

func (zeroReader) Read(b []byte) (int, error) { clear(b); return len(b), nil }

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
func beScalar(s scalar) []byte                   { b := s.value.Bytes(); return b[:] }
func TestSamplingEntropyErrorsCancellationAndSecretLifecycle(t *testing.T) {
	ctx := context.Background()
	n := unhex("fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141")
	draws := append(bytes.Clone(n), make([]byte, 32)...)
	draws = append(draws, beScalar(scalarInt(1))...)
	w := newWork(ctx, bytes.NewReader(draws))
	if !w.random(false).zero() || !w.random(true).equal(scalarInt(1)) || w.err != nil {
		t.Fatal("scalar rejection")
	}
	w = newWork(ctx, bytes.NewReader(draws))
	if !w.random(true).equal(scalarInt(1)) {
		t.Fatal("secret zero rejection")
	}
	var words []byte
	for _, n := range []uint32{0, 2, 0} {
		words = binary.LittleEndian.AppendUint32(words, n)
	}
	w = newWork(ctx, bytes.NewReader(words))
	perm := []int{0, 1, 2}
	w.permute(perm)
	if !slices.Equal(perm, []int{1, 0, 2}) || w.err != nil {
		t.Fatal("permutation rejection")
	}
	draws = append(make([]byte, 32), beScalar(scalarInt(1))...)
	draws = append(draws, beScalar(scalarInt(1).neg())...)
	draws = append(draws, make([]byte, 32)...)
	w = newWork(ctx, bytes.NewReader(draws))
	card, r := w.remask(testPoint(2), MaskedCard{c2: testPoint(1)})
	if !r.equal(scalarInt(1)) {
		t.Fatal("initial zero not rejected")
	}
	again, r := w.remask(testPoint(2), card)
	if !r.zero() || !again.equal(card) || w.err != nil {
		t.Fatal("remask cancellation or valid zero")
	}
	fault := errors.New("entropy unavailable")
	for _, r := range []io.Reader{failingReader{fault}, bytes.NewReader([]byte{1})} {
		sk, pk, proof, err := GenerateKey(ctx, r, []byte("test"))
		if err == nil || sk != nil || !pk.point.infinity() || len(proof.encoded) != 0 {
			t.Fatal("partial key result")
		}
	}
	sk := must(DecodeSecretKey(scalarInt(7).bytes()))
	copyHandle := *sk
	for _, format := range []string{"%v", "%+v", "%#v", "%x", "%s"} {
		if got := fmt.Sprintf(format, sk); got != "<shuffle secret>" {
			t.Fatalf("secret formatting: %s", got)
		}
	}
	raw := must(sk.MarshalBinary())
	raw[0] = 42
	if must(sk.MarshalBinary())[0] != 7 {
		t.Fatal("secret buffer alias")
	}
	for _, raw := range [][]byte{nil, make([]byte, 32), func() []byte { b := bytes.Clone(n); slices.Reverse(b); return b }(), bytes.Repeat([]byte{255}, 32)} {
		if _, err := DecodeSecretKey(raw); err == nil {
			t.Fatal("invalid secret")
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if s, _, _, err := GenerateKey(canceled, zeroReader{}, nil); !errors.Is(err, context.Canceled) || s != nil {
		t.Fatal("cancel key")
	}
	if _, _, err := Reveal(canceled, zeroReader{}, sk, card, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel reveal")
	}
	if _, _, err := Reveal(ctx, failingReader{fault}, sk, card, nil); !errors.Is(err, fault) {
		t.Fatal("reveal entropy")
	}
	// Concurrent calls and destruction must not race; all copies share erasure.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				_, _ = sk.PublicKey()
				_, _ = sk.MarshalBinary()
			}
		})
	}
	_ = sk.Destroy()
	wg.Wait()
	_ = copyHandle.Destroy()
	if _, err := copyHandle.PublicKey(); !errors.Is(err, ErrSecret) {
		t.Fatal("copy survived destruction")
	}
	if _, _, err := Reveal(ctx, nil, &copyHandle, card, nil); !errors.Is(err, ErrSecret) {
		t.Fatal("destroyed reveal")
	}
	if _, err := (*SecretKey)(nil).MarshalBinary(); !errors.Is(err, ErrSecret) {
		t.Fatal("nil secret")
	}
	if _, err := (&SecretKey{}).PublicKey(); !errors.Is(err, ErrSecret) {
		t.Fatal("zero secret")
	}
}
