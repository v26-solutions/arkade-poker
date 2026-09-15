//go:build !js

package main

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"arkade-poker/go/internal/wallet"
	"github.com/btcsuite/btcd/btcutil/bech32"
)

func TestInitialKeyImport(t *testing.T) {
	const mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	raw := strings.Repeat("0", 63) + "1"
	data, _ := hex.DecodeString(raw)
	words, _ := bech32.ConvertBits(data, 8, 5, true)
	nsec, _ := bech32.Encode("nsec", words)
	for _, input := range []string{raw, nsec, "invalid-secret"} {
		key, err := parseInitialKey(t.Context(), input, func(context.Context) (string, error) {
			t.Fatal("non-mnemonic input queried Arkd")
			return "", nil
		})
		if input == "invalid-secret" {
			if key != nil || err != wallet.ErrKey {
				t.Fatal("invalid key accepted")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			key.Destroy()
		}
	}
	for _, network := range []string{"bitcoin", "regtest", "mutinynet"} {
		key, err := parseInitialKey(t.Context(), mnemonic, func(ctx context.Context) (string, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("unbounded network discovery")
			}
			return network, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		want, err := wallet.ParseKey(mnemonic, network)
		if err != nil {
			t.Fatal(err)
		}
		if key.PublicKey() != want.PublicKey() {
			t.Fatal("startup ignored Arkd network")
		}
		key.Destroy()
		want.Destroy()
	}
	offline := errors.New("Arkd unavailable")
	key, err := parseInitialKey(t.Context(), mnemonic, func(context.Context) (string, error) { return "", offline })
	if key != nil || !errors.Is(err, offline) {
		t.Fatal("failed discovery derived a key")
	}
}
