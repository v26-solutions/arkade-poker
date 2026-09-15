package shuffle

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
)

// work propagates the first entropy/cancellation error through arithmetic
// helpers. Public operations discard every result if err is set. No partial
// proof or secret is returned. Entropy readers must not block indefinitely;
// context cancellation cannot interrupt an arbitrary io.Reader.Read call.
type work struct {
	ctx     context.Context
	entropy io.Reader
	err     error
	steps   uint32
}

func newWork(ctx context.Context, entropy io.Reader) *work {
	w := &work{ctx: ctx, entropy: entropy}
	if entropy == nil {
		w.entropy = rand.Reader
	}
	if ctx == nil {
		w.err = fmt.Errorf("shuffle: nil context")
	} else {
		w.err = ctx.Err()
	}
	return w
}
func (w *work) check() bool {
	if w.err != nil {
		return false
	}
	w.steps++
	if w.steps%8 == 0 {
		cooperate()
	}
	w.err = w.ctx.Err()
	return w.err == nil
}
func (w *work) read(b []byte) bool {
	if !w.check() {
		return false
	}
	if _, err := io.ReadFull(w.entropy, b); err != nil {
		w.err = fmt.Errorf("shuffle: entropy: %w", err)
		return false
	}
	return w.check()
}
func (w *work) random(nonzero bool) scalar {
	var b [32]byte
	defer clear(b[:])
	for w.read(b[:]) {
		var s scalar
		if s.value.SetBytes(&b) == 0 && (!nonzero || !s.zero()) {
			return s
		}
		s.value.Zero()
	}
	return scalar{}
}
func (w *work) vector() (out [DeckSize]scalar) {
	for i := range out {
		out[i] = w.random(false)
	}
	return out
}

// Uniform Fisher-Yates, rejecting the biased prefix of each 32-bit draw.
func (w *work) permute(out []int) {
	for i := len(out) - 1; i > 0; i-- {
		bound := uint32(i + 1)
		threshold := -bound % bound
		var b [4]byte
		for {
			if !w.read(b[:]) {
				return
			}
			n := binary.LittleEndian.Uint32(b[:])
			if n >= threshold {
				j := int(n % bound)
				out[i], out[j] = out[j], out[i]
				break
			}
		}
	}
}
