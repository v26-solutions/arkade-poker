package shuffle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"arkade-poker/go/internal/merkel"
	"golang.org/x/crypto/chacha20"
)

// Test-only ChaCha stream reproduces rand_chacha::ChaCha20Rng's public seed.
// All draws are word aligned and counter < 2^32 (zero stream/nonce).
func testEntropy(seed byte) io.Reader {
	c, err := chacha20.NewUnauthenticatedCipher(bytes.Repeat([]byte{seed}, 32), make([]byte, 12))
	if err != nil {
		panic(err)
	}
	return &chachaReader{c}
}

type chachaReader struct{ c *chacha20.Cipher }

func (r *chachaReader) Read(b []byte) (int, error) {
	clear(b)
	r.c.XORKeyStream(b, b)
	return len(b), nil
}
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
func testPoint(n uint32) point { return baseMul(scalarInt(n)) }
func unhex(s string) []byte    { return must(hex.DecodeString(s)) }

type flowFixture struct {
	p                                                       *Protocol
	keys                                                    [2]PublicKey
	verified                                                [2]VerifiedPublicKey
	secrets                                                 [2]*SecretKey
	aggregate                                               AggregatePublicKey
	initial, final                                          Deck
	initialProof, finalProof                                ShuffleProof
	first, last                                             VerifiedDeck
	revealed                                                [DeckSize]byte
	digest                                                  string
	initialTime, initialVerifyTime, shuffleTime, verifyTime time.Duration
}

var flowOnce sync.Once
var fixedFlow flowFixture

func flow() flowFixture {
	flowOnce.Do(func() {
		f := &fixedFlow
		f.p = must(New())
		rng := testEntropy(52)
		ctx := context.Background()
		binding := []byte("full-flow-v2")
		h := sha256.New()
		h.Write(binding)
		var own [2]OwnershipProof
		for i := range f.keys {
			var err error
			f.secrets[i], f.keys[i], own[i], err = GenerateKey(ctx, rng, binding)
			if err != nil {
				panic(err)
			}
		}
		for i, key := range f.keys {
			h.Write(must(f.secrets[i].MarshalBinary()))
			h.Write(must(key.MarshalBinary()))
			affine := must(key.AffineBytes())
			h.Write(affine[:])
			h.Write(must(own[i].MarshalBinary()))
			f.verified[i] = must(VerifyOwnership(key, must(DecodeOwnershipProof(must(own[i].MarshalBinary()))), binding))
		}
		f.aggregate = must(AggregateKeys(f.verified[:]))
		start := time.Now()
		var err error
		f.initial, f.initialProof, err = f.p.ShuffleInitial(ctx, rng, f.aggregate, binding)
		if err != nil {
			panic(err)
		}
		f.initialTime = time.Since(start)
		h.Write(must(f.aggregate.MarshalBinary()))
		h.Write(must(f.initial.MarshalBinary()))
		h.Write(must(f.initialProof.MarshalBinary()))
		f.initial = must(DecodeDeck(must(f.initial.MarshalBinary())))
		f.initialProof = must(DecodeShuffleProof(must(f.initialProof.MarshalBinary())))
		start = time.Now()
		f.first = must(f.p.VerifyInitial(ctx, f.aggregate, f.initial, f.initialProof, binding))
		f.initialVerifyTime = time.Since(start)
		start = time.Now()
		f.final, f.finalProof, err = f.p.Shuffle(ctx, rng, f.aggregate, f.first, binding)
		if err != nil {
			panic(err)
		}
		f.shuffleTime = time.Since(start)
		h.Write(must(f.final.MarshalBinary()))
		h.Write(must(f.finalProof.MarshalBinary()))
		f.final = must(DecodeDeck(must(f.final.MarshalBinary())))
		f.finalProof = must(DecodeShuffleProof(must(f.finalProof.MarshalBinary())))
		start = time.Now()
		f.last = must(f.p.Verify(ctx, f.aggregate, f.first, f.final, f.finalProof, binding))
		f.verifyTime = time.Since(start)
		for i := 0; i < DeckSize; i++ {
			card := must(f.last.Card(i))
			affine := must(card.AffineBytes())
			h.Write(affine[:])
			var shares [2]VerifiedRevealToken
			for j := range shares {
				token, proof, err := Reveal(ctx, rng, f.secrets[j], card, binding)
				if err != nil {
					panic(err)
				}
				h.Write(must(token.MarshalBinary()))
				h.Write(must(proof.MarshalBinary()))
				ta, pa := must(token.AffineBytes()), must(proof.AffineBytes())
				h.Write(ta[:])
				h.Write(pa[:])
				if j == 0 {
					token = must(DecodeRevealToken(must(token.MarshalBinary())))
					proof = must(DecodeRevealProof(must(proof.MarshalBinary())))
				} else {
					token = must(RevealTokenFromAffine(ta))
					proof = must(RevealProofFromAffine(pa))
				}
				shares[j] = must(VerifyReveal(f.verified[j], card, token, proof, binding))
			}
			f.revealed[i] = must(f.p.RevealCard(card, must(AggregateReveals(shares[:]))))
			h.Write([]byte{f.revealed[i]})
		}
		f.digest = fmt.Sprintf("%x", h.Sum(nil))
	})
	return fixedFlow
}
func TestFullFlowReferenceDigestAndHandProofs(t *testing.T) {
	f := flow()
	// Frozen Rust protocol_tests::complete_fifty_two_card_hand: hashes every
	// secret/key/proof/deck/reveal, affine encoding and recovered card index.
	if f.digest != "340069afc720fb92a59612e0d880a2532e2b25761a349f2987e5c5df93f6533e" {
		t.Fatalf("Rust full-flow digest mismatch: %s", f.digest)
	}
	seen := [DeckSize]bool{}
	for _, n := range f.revealed {
		if n >= DeckSize || seen[n] {
			t.Fatal("not a complete permutation")
		}
		seen[n] = true
	}
	if !must(parseShuffleProof(f.initialProof.encoded)).multi.ct1.c1.infinity() {
		t.Fatal("initial anchor must be infinity")
	}
	start := time.Now()
	deal := [9]byte(f.revealed[:9])
	evaluation := must(merkel.Evaluate(context.Background(), deal))
	elapsed := time.Since(start)
	if !evaluation.Player1.Verify([7]byte{deal[0], deal[1], deal[4], deal[5], deal[6], deal[7], deal[8]}) || !evaluation.Player2.Verify([7]byte{deal[2], deal[3], deal[4], deal[5], deal[6], deal[7], deal[8]}) {
		t.Fatal("hand proofs for revealed cards")
	}
	t.Logf("52 cards: initial+proof %s; initial verify %s; reshuffle+proof %s; reshuffle verify %s; two hand proofs %s", f.initialTime, f.initialVerifyTime, f.shuffleTime, f.verifyTime, elapsed)
}
func TestIndependentTranscriptVectors(t *testing.T) {
	binding := []byte("poker-shuffle/transcript-vector/v2")
	check := func(s scalar, expected string) {
		t.Helper()
		if !bytes.Equal(s.bytes(), unhex(expected)) {
			t.Fatalf("challenge %x != %s", s.bytes(), expected)
		}
	}
	check(ownershipChallenge(PublicKey{testPoint(2)}, testPoint(17), binding), "64f74e6400f74c3b446bbdcbd405b4bbb4f5866d3eea6eaf8f79999daed7ec70")
	prev := []MaskedCard{{point{}, testPoint(1)}, {point{}, testPoint(2)}}
	next := []MaskedCard{{testPoint(3), testPoint(4)}, {testPoint(5), testPoint(6)}}
	ts, x := shuffleChallenge(AggregatePublicKey{testPoint(2)}, prev, next, testPoint(7), binding)
	check(x, "ac52f6ff303a910b7e0c52f0d617d29ff479260a94419e95319d62542f6de44e")
	ts, y, z := shuffleYZ(ts, testPoint(8))
	check(y, "1c853e2826dc57b5b8afe8715f08213d61e665e6c7ebe99f360a71cb4921c71a")
	check(z, "637020f9c94039abdd05e11b11ae8fa05145b80f0620e4a65b77072f60b19c13")
	check((multiArg{cAlpha: testPoint(9), cBeta: testPoint(10), ct0: MaskedCard{testPoint(11), testPoint(12)}, ct1: MaskedCard{point{}, testPoint(13)}}).challenge(ts), "13f2abeeecee32b3947c72e6078817e7a9ce8aed7ccd67f0e1c54bf45a29c41d")
	check((productArg{cD: testPoint(14), cSmallDelta: testPoint(15), cCapitalDelta: testPoint(16)}).challenge(ts), "0ebf2885fbbe472227d33facb0c891f0af3a457251c1e8a02d09ae2ee76f6ab5")
	base := newTranscript([]byte("ctx"))
	want := base.append("cards", []byte("ab"), []byte("cd"))
	for _, other := range []transcript{
		newTranscript([]byte("ctx2")).append("cards", []byte("ab"), []byte("cd")), base.append("other", []byte("ab"), []byte("cd")), base.append("cards", []byte("cd"), []byte("ab")), base.append("cards", []byte("abcd")), base.append("cards", []byte("ab"), []byte("c"), []byte("d")),
	} {
		if other == want {
			t.Fatal("ambiguous framing")
		}
	}
	if want.challenge("ziffle/BG12/yz/v2", 0).equal(want.challenge("ziffle/BG12/yz/v2", 1)) || want.challenge("ziffle/BG12/yz/v2", 0).equal(want.challenge("ziffle/DLOG/v2", 0)) {
		t.Fatal("challenge domain/index collision")
	}
}
