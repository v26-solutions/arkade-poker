package shuffle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func proofPoints(p *bgProof) []*point {
	m, a := &p.multi, &p.product
	return []*point{&p.cPi, &p.cXPi, &m.cAlpha, &m.cBeta, &m.ct0.c1, &m.ct0.c2, &m.ct1.c1, &m.ct1.c2, &a.cD, &a.cSmallDelta, &a.cCapitalDelta}
}
func proofScalars(p *bgProof) []*scalar {
	m, a := &p.multi, &p.product
	out := make([]*scalar, 0, 3*DeckSize+6)
	for i := range m.oAlpha {
		out = append(out, &m.oAlpha[i])
	}
	out = append(out, &m.oR, &m.beta, &m.oBeta, &m.tau)
	for _, v := range []*[DeckSize]scalar{&a.aTilde, &a.bTilde} {
		for i := range v {
			out = append(out, &v[i])
		}
	}
	return append(out, &a.rTilde, &a.sTilde)
}
func TestEveryShuffleProofFieldAndCiphertextBound(t *testing.T) {
	f := flow()
	ctx := context.Background()
	binding := []byte("full-flow-v2")
	for stage := range 2 {
		deck, proof := f.initial, f.initialProof
		verify := func(d Deck, p ShuffleProof) error {
			_, err := f.p.VerifyInitial(ctx, f.aggregate, d, p, binding)
			return err
		}
		if stage == 1 {
			deck, proof = f.final, f.finalProof
			verify = func(d Deck, p ShuffleProof) error {
				_, err := f.p.Verify(ctx, f.aggregate, f.first, d, p, binding)
				return err
			}
		}
		original := must(parseShuffleProof(proof.encoded))
		for i := range proofPoints(&original) {
			p := original
			q := proofPoints(&p)[i]
			*q = q.add(testPoint(1))
			if err := verify(deck, ShuffleProof{p.encode()}); !errors.Is(err, ErrProof) {
				t.Fatalf("stage %d point %d: %v", stage, i, err)
			}
		}
		for i := range proofScalars(&original) {
			p := original
			s := proofScalars(&p)[i]
			*s = s.add(scalarInt(1))
			if err := verify(deck, ShuffleProof{p.encode()}); !errors.Is(err, ErrProof) {
				t.Fatalf("stage %d scalar %d: %v", stage, i, err)
			}
		}
		for i := range deck {
			for component := range 2 {
				altered := deck
				if component == 0 {
					altered[i].c1 = altered[i].c1.add(testPoint(1))
				} else {
					altered[i].c2 = altered[i].c2.add(testPoint(1))
				}
				if err := verify(altered, proof); !errors.Is(err, ErrProof) {
					t.Fatalf("stage %d card %d/%d: %v", stage, i, component, err)
				}
			}
		}
	}
	if _, err := f.p.VerifyInitial(ctx, f.aggregate, f.initial, f.initialProof, []byte("wrong session")); !errors.Is(err, ErrProof) {
		t.Fatal("initial context")
	}
	if _, err := f.p.Verify(ctx, f.aggregate, f.first, f.final, f.finalProof, []byte("wrong session")); !errors.Is(err, ErrProof) {
		t.Fatal("reshuffle context")
	}
	other := AggregatePublicKey{testPoint(3)}
	if _, err := f.p.VerifyInitial(ctx, other, f.initial, f.initialProof, binding); !errors.Is(err, ErrProof) {
		t.Fatal("initial key")
	}
	if _, err := f.p.Verify(ctx, other, f.first, f.final, f.finalProof, binding); !errors.Is(err, ErrProof) {
		t.Fatal("reshuffle key")
	}
	if _, err := f.p.Verify(ctx, f.aggregate, f.last, f.final, f.finalProof, binding); !errors.Is(err, ErrProof) {
		t.Fatal("predecessor")
	}
	if _, err := f.p.VerifyInitial(ctx, f.aggregate, f.final, f.finalProof, binding); !errors.Is(err, ErrProof) {
		t.Fatal("reshuffle passed as initial")
	}
	swapped := f.final
	swapped[0], swapped[1] = swapped[1], swapped[0]
	if _, err := f.p.Verify(ctx, f.aggregate, f.first, swapped, f.finalProof, binding); !errors.Is(err, ErrProof) {
		t.Fatal("output order")
	}
}
func TestHonestZeroBlindingsAndDishonestShuffleWitnesses(t *testing.T) {
	p := must(New())
	key := AggregatePublicKey{testPoint(1).neg()}
	ctx := context.Background()
	binding := []byte("zero-blindings")
	prev := p.initial()
	var next Deck
	var perm [DeckSize]int
	var rho [DeckSize]scalar
	for i := range next {
		perm[i] = i
		rho[i] = scalarInt(1)
		next[i] = MaskedCard{testPoint(1), prev[i].c2.add(key.point)}
	}
	if !next[0].c2.infinity() {
		t.Fatal("expected infinite c2")
	}
	w := newWork(ctx, zeroReader{})
	proof := p.prove(w, key, perm, prev, next, rho, binding)
	if w.err != nil {
		t.Fatal(w.err)
	}
	if !proof.multi.cBeta.infinity() || !proof.multi.ct0.c1.infinity() || !proof.product.cD.infinity() {
		t.Fatal("zero commitments not exercised")
	}
	v := must(p.VerifyInitial(ctx, key, must(DecodeDeck(must(next.MarshalBinary()))), must(DecodeShuffleProof(proof.encode())), binding))
	if _, err := v.Card(0); err != nil {
		t.Fatal(err)
	}
	// All equations except the permutation assertion can be satisfied when
	// duplicating an input and dropping another; the product argument rejects it.
	perm[1] = 0
	next[1] = next[0]
	proof = p.prove(newWork(ctx, zeroReader{}), key, perm, prev, next, rho, binding)
	ts, x := shuffleChallenge(key, prev[:], next[:], proof.cPi, binding)
	ts, y, z := shuffleYZ(ts, proof.cXPi)
	if p.verifyProduct(newWork(ctx, nil), proof, x, y, z, ts) {
		t.Fatal("nonpermutation accepted")
	}
	if _, err := p.VerifyInitial(ctx, key, next, ShuffleProof{proof.encode()}, binding); !errors.Is(err, ErrProof) {
		t.Fatal("duplicated plaintext")
	}
	// Honest permutation commitments do not excuse a wrong ciphertext/remask.
	perm[1] = 1
	next[1] = MaskedCard{testPoint(1), prev[1].c2.add(key.point)}
	next[7].c2 = next[7].c2.add(testPoint(1))
	proof = p.prove(newWork(ctx, zeroReader{}), key, perm, prev, next, rho, binding)
	if _, err := p.VerifyInitial(ctx, key, next, ShuffleProof{proof.encode()}, binding); !errors.Is(err, ErrProof) {
		t.Fatal("invalid remask")
	}
}
func TestRevealEquationsBindingsAndAggregateAdmission(t *testing.T) {
	f := flow()
	ctx := context.Background()
	card := must(f.last.Card(0))
	binding := []byte("reveal")
	token, proof, err := Reveal(ctx, testEntropy(71), f.secrets[0], card, binding)
	if err != nil {
		t.Fatal(err)
	}
	_ = must(VerifyReveal(f.verified[0], card, token, proof, binding))
	for _, change := range []func([]byte){func(b []byte) { b[0] ^= 1 }, func(b []byte) { b[33] ^= 1 }, func(b []byte) { b[66] ^= 1 }} {
		b := bytes.Clone(proof.encoded)
		change(b)
		if _, err := VerifyReveal(f.verified[0], card, token, RevealProof{b}, binding); err == nil {
			t.Fatal("reveal field")
		}
	}
	for _, key := range []VerifiedPublicKey{{}, f.verified[1]} {
		if _, err := VerifyReveal(key, card, token, proof, binding); err == nil {
			t.Fatal("reveal key")
		}
	}
	if _, err := VerifyReveal(f.verified[0], card, token, proof, []byte("other")); err == nil {
		t.Fatal("reveal context")
	}
	if _, err := VerifyReveal(f.verified[0], must(f.last.Card(1)), token, proof, binding); err == nil {
		t.Fatal("reveal card")
	}
	if _, err := VerifyReveal(f.verified[0], card, RevealToken{token.point.add(testPoint(1))}, proof, binding); err == nil {
		t.Fatal("reveal share")
	}
	sk := must(f.secrets[0].snapshot())
	defer sk.value.Zero()
	nonce := scalarInt(29)
	// Independently make each equation true while falsifying the other.
	for equation := range 2 {
		tg, tc1 := baseMul(nonce), card.c1.mul(nonce)
		if equation == 0 {
			tg = tg.add(testPoint(1))
		} else {
			tc1 = tc1.add(testPoint(1))
		}
		e := revealChallenge(f.keys[0], token.point, card.c1, tg, tc1, binding)
		z := nonce.sub(e.mul(sk))
		first := tg.equal(baseMul(z).add(f.keys[0].point.mul(e)))
		second := tc1.equal(card.c1.mul(z).add(token.point.mul(e)))
		if first != (equation == 1) || second != (equation == 0) {
			t.Fatal("equation fixture")
		}
		bad := RevealProof{append(append(tg.bytes(), tc1.bytes()...), z.bytes()...)}
		if _, err := VerifyReveal(f.verified[0], card, token, bad, binding); !errors.Is(err, ErrProof) {
			t.Fatal("missing DLEQ equation")
		}
	}
	id := [32]byte{0x42}
	for slot := byte(0); slot < 18; slot++ {
		b := must(RevealBinding(id, slot))
		if len(b) != 55 {
			t.Fatal("binding length")
		}
		token, proof, err := Reveal(ctx, testEntropy(slot+1), f.secrets[0], card, b)
		if err != nil {
			t.Fatal(err)
		}
		_ = must(VerifyReveal(f.verified[0], card, token, proof, b))
		b[54] = (slot + 1) % 18
		if _, err := VerifyReveal(f.verified[0], card, token, proof, b); err == nil {
			t.Fatal("slot not bound")
		}
		b[54] = slot
		b[22] ^= 1
		if _, err := VerifyReveal(f.verified[0], card, token, proof, b); err == nil {
			t.Fatal("contract not bound")
		}
	}
	if _, err := RevealBinding(id, 18); err == nil {
		t.Fatal("invalid slot")
	}
	if _, err := AggregateKeys(nil); err == nil {
		t.Fatal("empty keys")
	}
	if _, err := AggregateReveals(nil); err == nil {
		t.Fatal("empty shares")
	}
	if _, err := AggregateKeys([]VerifiedPublicKey{{}}); err == nil {
		t.Fatal("unverified key")
	}
	if _, err := AggregateReveals([]VerifiedRevealToken{{}}); err == nil {
		t.Fatal("unverified share")
	}
	var keys [2]VerifiedPublicKey
	var shares [2]VerifiedRevealToken
	for i, s := range []scalar{scalarInt(1), scalarInt(1).neg()} {
		entropy := append(beScalar(s), beScalar(scalarInt(11))...)
		sk, pk, own, err := GenerateKey(ctx, bytes.NewReader(entropy), binding)
		if err != nil {
			t.Fatal(err)
		}
		defer sk.Destroy()
		keys[i] = must(VerifyOwnership(pk, own, binding))
		rt, rp, err := Reveal(ctx, testEntropy(byte(i)), sk, card, binding)
		if err != nil {
			t.Fatal(err)
		}
		shares[i] = must(VerifyReveal(keys[i], card, rt, rp, binding))
	}
	if _, err := AggregateKeys(keys[:]); err == nil {
		t.Fatal("cancelled key")
	}
	if _, err := AggregateReveals(shares[:]); err == nil {
		t.Fatal("cancelled shares")
	}
	if _, err := f.p.RevealCard(card, AggregateRevealToken{}); err == nil {
		t.Fatal("zero aggregate")
	}
	if _, err := f.p.RevealCard(card, AggregateRevealToken{card.c2}); err == nil {
		t.Fatal("plaintext infinity")
	}
	if _, err := f.p.RevealCard(card, AggregateRevealToken{card.c2.sub(testPoint(53))}); err == nil {
		t.Fatal("plaintext outside deck")
	}
}
func TestInvalidZeroValuesAndCancellation(t *testing.T) {
	f := flow()
	ctx := context.Background()
	binding := []byte("full-flow-v2")
	for _, p := range []*Protocol{nil, {}} {
		if _, _, err := p.ShuffleInitial(ctx, nil, f.aggregate, binding); err == nil {
			t.Fatal("zero protocol")
		}
	}
	if _, _, err := f.p.ShuffleInitial(ctx, nil, AggregatePublicKey{}, binding); err == nil {
		t.Fatal("zero aggregate key")
	}
	if _, _, err := f.p.Shuffle(ctx, nil, f.aggregate, VerifiedDeck{}, binding); err == nil {
		t.Fatal("unverified shuffle")
	}
	if _, err := f.p.Verify(ctx, f.aggregate, VerifiedDeck{}, f.final, f.finalProof, binding); err == nil {
		t.Fatal("unverified verify")
	}
	if _, err := (VerifiedDeck{}).Card(0); err == nil {
		t.Fatal("unverified card")
	}
	if _, err := (VerifiedDeck{}).MarshalBinary(); err == nil {
		t.Fatal("unverified marshal")
	}
	for _, i := range []int{-1, DeckSize} {
		if _, err := f.last.Card(i); err == nil {
			t.Fatal("card range")
		}
	}
	if _, err := f.p.VerifyInitial(ctx, f.aggregate, Deck{}, f.initialProof, binding); err == nil {
		t.Fatal("zero ciphertexts")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := f.p.ShuffleInitial(canceled, nil, f.aggregate, binding); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel initial")
	}
	if _, _, err := f.p.Shuffle(canceled, nil, f.aggregate, f.first, binding); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel reshuffle")
	}
	if _, err := f.p.VerifyInitial(canceled, f.aggregate, f.initial, f.initialProof, binding); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel verify initial")
	}
	if _, err := f.p.Verify(canceled, f.aggregate, f.first, f.final, f.finalProof, binding); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel verify")
	}
	fault := errors.New("rng failure")
	for _, op := range []func() (Deck, ShuffleProof, error){func() (Deck, ShuffleProof, error) {
		return f.p.ShuffleInitial(ctx, failingReader{fault}, f.aggregate, binding)
	}, func() (Deck, ShuffleProof, error) {
		return f.p.Shuffle(ctx, failingReader{fault}, f.aggregate, f.first, binding)
	}} {
		d, p, err := op()
		if !errors.Is(err, fault) || d != (Deck{}) || len(p.encoded) != 0 {
			t.Fatal("partial proof after entropy failure")
		}
	}
	// Rejection loops must cooperate and observe cancellation too.
	timeout, stop := context.WithTimeout(ctx, 10*time.Millisecond)
	defer stop()
	if sk, _, _, err := GenerateKey(timeout, zeroReader{}, binding); !errors.Is(err, context.DeadlineExceeded) || sk != nil {
		t.Fatal("stuck zero RNG")
	}
}

// The context cancels at a deterministic checkpoint in a long calculation,
// exercising discard semantics without depending on machine speed.
type checkpointContext struct {
	context.Context
	remaining int
}

func (c *checkpointContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}
func TestInFlightErrorsDiscardWork(t *testing.T) {
	f := flow()
	binding := []byte("full-flow-v2")
	c := &checkpointContext{Context: context.Background(), remaining: 100}
	if d, p, err := f.p.ShuffleInitial(c, testEntropy(9), f.aggregate, binding); !errors.Is(err, context.Canceled) || d != (Deck{}) || len(p.encoded) != 0 {
		t.Fatal("initial partial result on cancellation")
	}
	c = &checkpointContext{Context: context.Background(), remaining: 100}
	if d, err := f.p.Verify(c, f.aggregate, f.first, f.final, f.finalProof, binding); !errors.Is(err, context.Canceled) || d.valid {
		t.Fatal("verified value after cancellation")
	}
	// Enough entropy for the permutation and deck, then fail inside BG12 proving.
	r := io.LimitReader(testEntropy(9), 4*(DeckSize-1)+32*(DeckSize+20))
	if d, p, err := f.p.ShuffleInitial(context.Background(), r, f.aggregate, binding); !errors.Is(err, io.EOF) || d != (Deck{}) || len(p.encoded) != 0 {
		t.Fatalf("proof entropy failure: %v", err)
	}
}
