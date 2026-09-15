package wallet

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestOwnershipSignatureBoundary(t *testing.T) {
	key, err := ParseKey("0000000000000000000000000000000000000000000000000000000000000001", "")
	if err != nil {
		t.Fatal(err)
	}
	defer key.Destroy()
	ctx := context.Background()
	digest := [32]byte{3}
	proof, err := key.ProveOwnership(ctx, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := AuthenticateOwnership(ctx, key.PublicKey(), digest, proof); err != nil {
		t.Fatal(err)
	}
	changed := bytes.Clone(proof)
	changed[0] ^= 1
	for _, bad := range [][]byte{nil, proof[:63], append(bytes.Clone(proof), 0), changed} {
		if err := AuthenticateOwnership(ctx, key.PublicKey(), digest, bad); !errors.Is(err, ErrOwnership) {
			t.Fatal("invalid ownership admitted", err)
		}
	}
	if err := AuthenticateOwnership(ctx, key.PublicKey(), [32]byte{4}, proof); !errors.Is(err, ErrOwnership) {
		t.Fatal("unbound ownership")
	}
	if err := AuthenticateOwnership(ctx, [32]byte{}, digest, proof); !errors.Is(err, ErrOwnership) {
		t.Fatal("invalid owner")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := key.ProveOwnership(cancelled, digest); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled signing", err)
	}
	if err := AuthenticateOwnership(cancelled, key.PublicKey(), digest, proof); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled verification", err)
	}
	key.Destroy()
	if _, err := key.ProveOwnership(ctx, digest); !errors.Is(err, ErrKey) {
		t.Fatal("destroyed key signed", err)
	}
}
