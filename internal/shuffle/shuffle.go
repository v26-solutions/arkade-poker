package shuffle

import (
	"context"
	"fmt"
	"io"
)

const DeckSize = 52

type MaskedCard struct{ c1, c2 point }
type Deck [DeckSize]MaskedCard

// VerifiedDeck is produced only after all BG12 equations succeed. Decoding
// never grants verification. Values/copies cannot alias a caller-owned deck.
type VerifiedDeck struct {
	deck  Deck
	valid bool
}
type ShuffleProof struct{ encoded []byte }
type Protocol struct {
	parameters Parameters
	open       [DeckSize]point
}

func New() (*Protocol, error) {
	params, err := LoadParameters()
	if err != nil {
		return nil, err
	}
	p := &Protocol{parameters: params}
	g := baseMul(scalarInt(1))
	q := g
	for i := range p.open {
		p.open[i] = q
		q = q.add(g)
	}
	return p, nil
}
func (p *Protocol) initial() (d Deck) {
	for i := range d {
		d[i].c2 = p.open[i]
	}
	return d
}
func (p *Protocol) admit(ctx context.Context, key AggregatePublicKey, binding []byte) error {
	if ctx == nil || p == nil || !p.parameters.valid || key.point.infinity() {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return validBinding(binding)
}
func (p *Protocol) ShuffleInitial(ctx context.Context, entropy io.Reader, key AggregatePublicKey, binding []byte) (Deck, ShuffleProof, error) {
	if err := p.admit(ctx, key, binding); err != nil {
		return Deck{}, ShuffleProof{}, err
	}
	return p.shuffle(ctx, entropy, key, p.initial(), binding)
}
func (p *Protocol) Shuffle(ctx context.Context, entropy io.Reader, key AggregatePublicKey, input VerifiedDeck, binding []byte) (Deck, ShuffleProof, error) {
	if err := p.admit(ctx, key, binding); err != nil {
		return Deck{}, ShuffleProof{}, err
	}
	if !input.valid {
		return Deck{}, ShuffleProof{}, ErrInvalid
	}
	return p.shuffle(ctx, entropy, key, input.deck, binding)
}
func (p *Protocol) VerifyInitial(ctx context.Context, key AggregatePublicKey, output Deck, proof ShuffleProof, binding []byte) (VerifiedDeck, error) {
	if err := p.admit(ctx, key, binding); err != nil {
		return VerifiedDeck{}, err
	}
	return p.verify(ctx, key, p.initial(), output, proof, binding)
}
func (p *Protocol) Verify(ctx context.Context, key AggregatePublicKey, input VerifiedDeck, output Deck, proof ShuffleProof, binding []byte) (VerifiedDeck, error) {
	if err := p.admit(ctx, key, binding); err != nil {
		return VerifiedDeck{}, err
	}
	if !input.valid {
		return VerifiedDeck{}, ErrInvalid
	}
	return p.verify(ctx, key, input.deck, output, proof, binding)
}
func (w *work) remask(key point, card MaskedCard) (MaskedCard, scalar) {
	for w.check() {
		r := w.random(false)
		if w.err != nil {
			return MaskedCard{}, scalar{}
		}
		c1 := card.c1.add(baseMul(r))
		// Initial cards require nonzero r. For reshuffles zero is valid; the single
		// value cancelling the previous c1 is rejected so all shares stay finite.
		if c1.infinity() {
			r.value.Zero()
			continue
		}
		return MaskedCard{c1, card.c2.add(key.mul(r))}, r
	}
	return MaskedCard{}, scalar{}
}
func (p *Protocol) shuffle(ctx context.Context, entropy io.Reader, key AggregatePublicKey, prev Deck, binding []byte) (Deck, ShuffleProof, error) {
	w := newWork(ctx, entropy)
	var perm [DeckSize]int
	var rho [DeckSize]scalar
	var next Deck
	defer clear(perm[:])
	defer clear(rho[:])
	for i := range perm {
		perm[i] = i
	}
	w.permute(perm[:])
	for i := range next {
		next[i], rho[i] = w.remask(key.point, prev[perm[i]])
	}
	if w.err != nil {
		return Deck{}, ShuffleProof{}, w.err
	}
	proof := p.prove(w, key, perm, prev, next, rho, binding)
	if !w.check() {
		return Deck{}, ShuffleProof{}, w.err
	}
	return next, ShuffleProof{proof.encode()}, nil
}
func (p *Protocol) verify(ctx context.Context, key AggregatePublicKey, prev, next Deck, proof ShuffleProof, binding []byte) (VerifiedDeck, error) {
	for _, c := range next {
		if c.c1.infinity() {
			return VerifiedDeck{}, ErrInvalid
		}
	}
	arg, err := parseShuffleProof(proof.encoded)
	if err != nil {
		return VerifiedDeck{}, err
	}
	w := newWork(ctx, nil)
	ts, x := shuffleChallenge(key, prev[:], next[:], arg.cPi, binding)
	ts, y, z := shuffleYZ(ts, arg.cXPi)
	valid := p.verifyMulti(w, key, prev, next, arg, x, ts) && p.verifyProduct(w, arg, x, y, z, ts)
	if !w.check() {
		return VerifiedDeck{}, w.err
	}
	if !valid {
		return VerifiedDeck{}, ErrProof
	}
	return VerifiedDeck{deck: next, valid: true}, nil
}
func (d VerifiedDeck) Card(index int) (MaskedCard, error) {
	if !d.valid {
		return MaskedCard{}, ErrInvalid
	}
	if index < 0 || index >= DeckSize {
		return MaskedCard{}, fmt.Errorf("shuffle: card index out of range")
	}
	return d.deck[index], nil
}
func (d VerifiedDeck) MarshalBinary() ([]byte, error) {
	if !d.valid {
		return nil, ErrInvalid
	}
	return d.deck.MarshalBinary()
}
func DecodeDeck(data []byte) (Deck, error) {
	if len(data) != 66*DeckSize {
		return Deck{}, fmt.Errorf("shuffle: deck must be %d bytes", 66*DeckSize)
	}
	var d Deck
	for i := range d {
		c, err := DecodeMaskedCard(data[i*66:][:66])
		if err != nil {
			return Deck{}, fmt.Errorf("shuffle: card %d: %w", i, err)
		}
		d[i] = c
	}
	return d, nil
}
func (d Deck) MarshalBinary() ([]byte, error) {
	out := make([]byte, 0, 66*DeckSize)
	for i, c := range d {
		b, err := c.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("shuffle: card %d: %w", i, err)
		}
		out = append(out, b...)
	}
	return out, nil
}
func (c MaskedCard) bytes() []byte           { return append(c.c1.bytes(), c.c2.bytes()...) }
func (c MaskedCard) equal(d MaskedCard) bool { return c.c1.equal(d.c1) && c.c2.equal(d.c2) }
func (w *work) cipherProduct(cards Deck, exponents [DeckSize]scalar) (out MaskedCard) {
	for i, c := range cards {
		if !w.check() {
			return MaskedCard{}
		}
		out.c1 = out.c1.add(c.c1.mul(exponents[i]))
		out.c2 = out.c2.add(c.c2.mul(exponents[i]))
	}
	return out
}
