package shuffle

import (
	"bytes"
	"context"
	"testing"
)

func TestCompleteCodecAdmissionAndOwnershipBinding(t *testing.T) {
	f := flow()
	binding := []byte("codec")
	ctx := context.Background()
	sk, key, own, err := GenerateKey(ctx, testEntropy(8), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer sk.Destroy()
	_ = must(VerifyOwnership(key, own, binding))
	for _, b := range [][]byte{nil, []byte("other")} {
		if _, err := VerifyOwnership(key, own, b); err == nil {
			t.Fatal("ownership context")
		}
	}
	if _, err := VerifyOwnership(f.keys[0], own, binding); err == nil {
		t.Fatal("ownership public key")
	}
	if _, err := VerifyOwnership(PublicKey{}, own, binding); err == nil {
		t.Fatal("zero public key")
	}
	for _, offset := range []int{0, 33} {
		b := bytes.Clone(own.encoded)
		b[offset] ^= 1
		if _, err := VerifyOwnership(key, OwnershipProof{b}, binding); err == nil {
			t.Fatal("ownership field")
		}
	}
	type codec struct {
		data   []byte
		decode func([]byte) error
	}
	codecs := []codec{
		{must(sk.MarshalBinary()), func(b []byte) error { _, err := DecodeSecretKey(b); return err }},
		{must(key.MarshalBinary()), func(b []byte) error { _, err := DecodePublicKey(b); return err }},
		{own.encoded, func(b []byte) error { _, err := DecodeOwnershipProof(b); return err }},
		{must(f.initial.MarshalBinary()), func(b []byte) error { _, err := DecodeDeck(b); return err }},
		{f.initialProof.encoded, func(b []byte) error { _, err := DecodeShuffleProof(b); return err }},
	}
	for _, c := range codecs {
		for n := 0; n < len(c.data); n++ {
			if err := c.decode(c.data[:n]); err == nil {
				t.Fatalf("accepted truncation %d/%d", n, len(c.data))
			}
		}
		if err := c.decode(append(bytes.Clone(c.data), 0)); err == nil {
			t.Fatal("trailing byte")
		}
	}
	// Every scalar offset, including every element of all three 52-vectors.
	var offsets []int
	for i := 0; i < DeckSize+4; i++ {
		offsets = append(offsets, 264+32*i)
	}
	productStart := 264 + 32*(DeckSize+4)
	for i := 0; i < 2*DeckSize+2; i++ {
		offsets = append(offsets, productStart+99+32*i)
	}
	order := unhex("414136d08c5ed2bf3ba048afe6dcaebafeffffffffffffffffffffffffffffffffff")
	for _, i := range offsets {
		b := bytes.Clone(f.initialProof.encoded)
		copy(b[i:], order)
		if _, err := DecodeShuffleProof(b); err == nil {
			t.Fatalf("scalar n at %d", i)
		}
	}
	pointOffsets := []int{0, 33, 66, 99, 132, 165, 198, 231, productStart, productStart + 33, productStart + 66}
	for _, i := range pointOffsets {
		b := bytes.Clone(f.initialProof.encoded)
		clear(b[i : i+33])
		if _, err := DecodeShuffleProof(b); err != nil {
			t.Fatalf("proof infinity %d: %v", i, err)
		}
		b[i] = 4
		if _, err := DecodeShuffleProof(b); err == nil {
			t.Fatalf("point prefix %d", i)
		}
	}
	for i := 0; i < DeckSize; i++ {
		b := must(f.initial.MarshalBinary())
		clear(b[i*66 : i*66+33])
		if _, err := DecodeDeck(b); err == nil {
			t.Fatal("infinite c1")
		}
		b = must(f.initial.MarshalBinary())
		clear(b[i*66+33 : i*66+66])
		if _, err := DecodeDeck(b); err != nil {
			t.Fatal("infinite c2")
		}
	}
	// All owned byte boundaries stay immutable when caller buffers are changed.
	ownBytes := must(own.MarshalBinary())
	decodedOwn := must(DecodeOwnershipProof(ownBytes))
	ownBytes[0] ^= 1
	if !bytes.Equal(must(decodedOwn.MarshalBinary()), own.encoded) {
		t.Fatal("ownership decoder alias")
	}
	ownBytes = must(decodedOwn.MarshalBinary())
	ownBytes[0] ^= 1
	if !bytes.Equal(must(decodedOwn.MarshalBinary()), own.encoded) {
		t.Fatal("ownership encoder alias")
	}
	proofBytes := must(f.finalProof.MarshalBinary())
	decodedProof := must(DecodeShuffleProof(proofBytes))
	proofBytes[0] ^= 1
	if !bytes.Equal(must(decodedProof.MarshalBinary()), f.finalProof.encoded) {
		t.Fatal("shuffle decoder alias")
	}
	proofBytes = must(decodedProof.MarshalBinary())
	proofBytes[0] ^= 1
	if !bytes.Equal(must(decodedProof.MarshalBinary()), f.finalProof.encoded) {
		t.Fatal("shuffle encoder alias")
	}
	d := must(f.last.MarshalBinary())
	d[0] ^= 1
	if !bytes.Equal(must(f.last.MarshalBinary()), must(f.final.MarshalBinary())) {
		t.Fatal("verified deck alias")
	}
	for _, marshal := range []func() ([]byte, error){(ShuffleProof{}).MarshalBinary, (OwnershipProof{}).MarshalBinary, (AggregatePublicKey{}).MarshalBinary} {
		if _, err := marshal(); err == nil {
			t.Fatal("zero-value encoding")
		}
	}
}

func FuzzCanonicalDecoders(f *testing.F) {
	fixture := flow()
	p := fixture.p
	f.Add(byte(5), fixture.initialProof.encoded)
	f.Add(byte(6), must(fixture.initial.MarshalBinary()))
	f.Add(byte(0), testPoint(1).bytes())
	f.Add(byte(1), scalarInt(1).bytes())
	f.Add(byte(2), append(testPoint(1).bytes(), scalarInt(1).bytes()...))
	f.Add(byte(3), append(testPoint(1).bytes(), testPoint(2).bytes()...))
	f.Add(byte(4), append(append(testPoint(1).bytes(), testPoint(2).bytes()...), scalarInt(1).bytes()...))
	f.Add(byte(5), make([]byte, ShuffleProofSize))
	f.Add(byte(6), make([]byte, DeckSize*66))
	f.Add(byte(7), make([]byte, 128))
	f.Fuzz(func(t *testing.T, kind byte, b []byte) {
		var encoded []byte
		var err error
		switch kind % 8 {
		case 0:
			var v PublicKey
			v, err = DecodePublicKey(b)
			if err == nil {
				encoded, err = v.MarshalBinary()
			}
		case 1:
			var v *SecretKey
			v, err = DecodeSecretKey(b)
			if err == nil {
				defer v.Destroy()
				encoded, err = v.MarshalBinary()
			}
		case 2:
			var v OwnershipProof
			v, err = DecodeOwnershipProof(b)
			if err == nil {
				encoded, err = v.MarshalBinary()
				_, _ = VerifyOwnership(PublicKey{testPoint(1)}, v, []byte("fuzz"))
			}
		case 3:
			var v MaskedCard
			v, err = DecodeMaskedCard(b)
			if err == nil {
				encoded, err = v.MarshalBinary()
			}
		case 4:
			var v RevealProof
			v, err = DecodeRevealProof(b)
			if err == nil {
				encoded, err = v.MarshalBinary()
				_, _ = VerifyReveal(VerifiedPublicKey{}, MaskedCard{}, RevealToken{}, v, nil)
			}
		case 5:
			var v ShuffleProof
			v, err = DecodeShuffleProof(b)
			if err == nil {
				encoded, err = v.MarshalBinary()
				_, _ = p.VerifyInitial(context.Background(), fixture.aggregate, fixture.initial, v, []byte("full-flow-v2"))
			}
		case 6:
			var v Deck
			v, err = DecodeDeck(b)
			if err == nil {
				encoded, err = v.MarshalBinary()
			}
		case 7:
			if len(b) != 128 {
				return
			}
			v, e := MaskedCardFromAffine([128]byte(b))
			if e != nil {
				return
			}
			a, e := v.AffineBytes()
			if e != nil || !bytes.Equal(a[:], b) {
				t.Fatal("affine noncanonical")
			}
			return
		}
		if err == nil && !bytes.Equal(encoded, b) {
			t.Fatal("accepted noncanonical bytes")
		}
	})
}
