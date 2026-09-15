package shuffle

import (
	"bytes"
	"encoding/hex"
	"slices"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
)

func TestCovenantPointAndCiphertextCodecs(t *testing.T) {
	for _, scalar := range []byte{1, 2, 3, 27, 255} {
		_, pk := btcec.PrivKeyFromBytes([]byte{scalar})
		compact := pk.SerializeCompressed()
		k, err := DecodePublicKey(compact)
		if err != nil {
			t.Fatal(err)
		}
		affine, err := k.AffineBytes()
		if err != nil {
			t.Fatal(err)
		}
		expected := pk.SerializeUncompressed()[1:]
		slices.Reverse(expected[:32])
		slices.Reverse(expected[32:])
		if !bytes.Equal(affine[:], expected) {
			t.Fatal("wrong affine endianness")
		}
		k2, err := PublicKeyFromAffine(affine)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := k2.MarshalBinary()
		if !bytes.Equal(got, compact) {
			t.Fatal("public key roundtrip")
		}
		token, err := RevealTokenFromAffine(affine)
		if err != nil {
			t.Fatal(err)
		}
		got, _ = token.MarshalBinary()
		if !bytes.Equal(got, compact) {
			t.Fatal("token roundtrip")
		}
		for _, c2 := range [][]byte{compact, make([]byte, 33)} {
			encoded := append(bytes.Clone(compact), c2...)
			card, err := DecodeMaskedCard(encoded)
			if err != nil {
				t.Fatal(err)
			}
			affine, err := card.AffineBytes()
			if err != nil {
				t.Fatal(err)
			}
			card2, err := MaskedCardFromAffine(affine)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := card2.MarshalBinary()
			if !bytes.Equal(got, encoded) {
				t.Fatal("card roundtrip")
			}
		}
	}
	_, pk := btcec.PrivKeyFromBytes([]byte{1})
	invalid := [][]byte{nil, make([]byte, 33), pk.SerializeUncompressed(), append(pk.SerializeCompressed(), 0), append([]byte{2}, bytes.Repeat([]byte{255}, 32)...)}
	bad := pk.SerializeCompressed()
	bad[0] = 4
	invalid = append(invalid, bad)
	for _, b := range invalid {
		if _, err := DecodePublicKey(b); err == nil {
			t.Fatalf("key accepted %x", b)
		}
		if _, err := DecodeRevealToken(b); err == nil {
			t.Fatalf("token accepted %x", b)
		}
	}
	for _, b := range [][64]byte{{}, [64]byte(bytes.Repeat([]byte{255}, 64)), {1, 2, 3}} {
		if _, err := PublicKeyFromAffine(b); err == nil {
			t.Fatal("invalid affine accepted")
		}
		if _, err := RevealTokenFromAffine(b); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
	if _, err := (PublicKey{}).MarshalBinary(); err == nil {
		t.Fatal("zero key accepted")
	}
	if _, err := (MaskedCard{}).AffineBytes(); err == nil {
		t.Fatal("zero card accepted")
	}
	if _, err := DecodeMaskedCard(make([]byte, 66)); err == nil {
		t.Fatal("infinite c1 accepted")
	}
	if _, err := MaskedCardFromAffine([128]byte{}); err == nil {
		t.Fatal("infinite affine c1 accepted")
	}
}
func TestRevealProofCodecBounds(t *testing.T) {
	_, pk := btcec.PrivKeyFromBytes([]byte{1})
	data := append(pk.SerializeCompressed(), pk.SerializeCompressed()...)
	data = append(data, make([]byte, 32)...)
	proof, err := DecodeRevealProof(data)
	if err != nil {
		t.Fatal(err)
	}
	affine, err := proof.AffineBytes()
	if err != nil {
		t.Fatal(err)
	}
	again, err := RevealProofFromAffine(affine)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := again.MarshalBinary()
	if !bytes.Equal(got, data) {
		t.Fatal("proof roundtrip")
	}
	data[0] = 0
	got2, _ := proof.MarshalBinary()
	if got2[0] == 0 {
		t.Fatal("decoder retains caller buffer")
	}
	got[0] = 0
	got2, _ = again.MarshalBinary()
	if got2[0] == 0 {
		t.Fatal("encoder returns owned buffer")
	}
	order, _ := hex.DecodeString("fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141")
	slices.Reverse(order)
	valid, _ := proof.MarshalBinary()
	for _, edit := range []func([]byte) []byte{
		func(b []byte) []byte { return b[:97] }, func(b []byte) []byte { return append(b, 0) },
		func(b []byte) []byte { clear(b[:33]); return b }, func(b []byte) []byte { clear(b[33:66]); return b },
		func(b []byte) []byte { copy(b[66:], order); return b }, func(b []byte) []byte { copy(b[66:], bytes.Repeat([]byte{255}, 32)); return b },
	} {
		if _, err := DecodeRevealProof(edit(bytes.Clone(valid))); err == nil {
			t.Fatal("malformed proof accepted")
		}
	}
	if _, err := (RevealProof{}).AffineBytes(); err == nil {
		t.Fatal("zero proof accepted")
	}
	copy(affine[128:], order)
	if _, err := RevealProofFromAffine(affine); err == nil {
		t.Fatal("out-of-range affine response accepted")
	}
	// n-1 is canonical, while n above was rejected without reduction.
	order[0]--
	copy(valid[66:], order)
	if _, err := DecodeRevealProof(valid); err != nil {
		t.Fatalf("n-1 rejected: %v", err)
	}
}
