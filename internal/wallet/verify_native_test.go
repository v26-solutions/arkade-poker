//go:build !js

package wallet

import (
	"context"
	"strings"
	"testing"

	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/btcutil/psbt"
)

func TestNativeServiceSignaturesBeforeFinalize(t *testing.T) {
	w, a, _, prepared, _ := spendingFixture(t)
	signed, err := w.Sign(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	good := signAsServer(t, signed)
	for _, kind := range []string{"missing server", "corrupt server"} {
		t.Run(kind, func(t *testing.T) {
			remote := signed
			if kind == "corrupt server" {
				remote = good
				p, err := psbt.NewFromRawBytes(strings.NewReader(good.Ark), true)
				if err != nil {
					t.Fatal(err)
				}
				for _, s := range p.Inputs[0].TaprootScriptSpendSig {
					if string(s.XOnlyPubKey) == string(w.config.ArkSigningKey[:]) {
						s.Signature[0] ^= 1
					}
				}
				remote.Ark, err = p.B64Encode()
				if err != nil {
					t.Fatal(err)
				}
			}
			a.submit = func(context.Context, ports.Bundle) (ports.Submitted, error) {
				p, _, _ := unsignedPSBT(signed.Ark)
				return ports.Submitted{TxID: p.UnsignedTx.TxHash().String(), Bundle: remote}, nil
			}
			a.finalize = func(context.Context, string, []string) error {
				t.Fatal("finalize after invalid service signatures")
				return nil
			}
			if _, err := w.Submit(context.Background(), ArkdRoute, signed); err == nil {
				t.Fatal("invalid service signature accepted")
			}
		})
	}
}
