package game

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"arkade-poker/go/internal/ports"
	"arkade-poker/go/internal/wallet"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
)

type signingArkd struct {
	ports.Arkd
	info ports.ArkInfo
}

func (s signingArkd) Info(context.Context) (ports.ArkInfo, error) { return s.info, nil }

type signingEmulator struct {
	ports.Emulator
	info ports.EmulatorInfo
}

func (s signingEmulator) Info(context.Context) (ports.EmulatorInfo, error) { return s.info, nil }

// Exercise the actual wallet signer on every driver-prepared bundle through a
// complete ordinary hand. Funding/indexing remain controlled; poker scripts
// still execute in the real emulator VM used by the shared driver harness.
func TestDriverActualWalletSignsCompleteHand(t *testing.T) {
	runDriverHand(t, true, -1, func(f *fixtureDriverWallet) {
		c := f.config
		server, err := schnorr.ParsePubKey(c.ArkSigningKey[:])
		if err != nil {
			t.Fatal(err)
		}
		emulator, err := schnorr.ParsePubKey(c.EmulatorSigningKey[:])
		if err != nil {
			t.Fatal(err)
		}
		checkpoint := new(script.CSVMultisigClosure)
		ok, err := checkpoint.Decode(c.CheckpointScript)
		if err != nil || !ok || len(checkpoint.PubKeys) != 1 {
			t.Fatal("checkpoint fixture", err)
		}
		ark := signingArkd{info: ports.ArkInfo{Network: c.Network, Signer: hex.EncodeToString(server.SerializeCompressed()), Forfeit: hex.EncodeToString(checkpoint.PubKeys[0].SerializeCompressed()), CheckpointScript: hex.EncodeToString(c.CheckpointScript), ExitDelay: 144, Dust: c.OutputPolicy.MinAmount, MinVtxo: c.OutputPolicy.MinAmount, MaxVtxo: c.OutputPolicy.MaxAmount}}
		emu := signingEmulator{info: ports.EmulatorInfo{Signer: hex.EncodeToString(emulator.SerializeCompressed())}}
		actual, err := wallet.New(context.Background(), wallet.Services{Arkd: ark, Emulator: emu, Indexer: f.f}, f.key, wallet.Config{Network: c.Network})
		if err != nil {
			t.Fatal(err)
		}
		f.signResult = func(b ports.Bundle) ports.Bundle {
			signed, err := actual.Sign(context.Background(), b)
			if err != nil {
				t.Error("actual wallet sign", err)
				return ports.Bundle{}
			}
			for _, encoded := range append([]string{signed.Ark}, signed.Checkpoints...) {
				p, err := psbt.NewFromRawBytes(strings.NewReader(encoded), true)
				if err != nil {
					t.Error(err)
					return ports.Bundle{}
				}
				fetcher := txscript.NewMultiPrevOutFetcher(nil)
				var skip []*btcec.PublicKey
				for i, input := range p.Inputs {
					fetcher.AddPrevOut(p.UnsignedTx.TxIn[i].PreviousOutPoint, input.WitnessUtxo)
					closure, err := script.DecodeClosure(input.TaprootLeafScript[0].Script)
					if err != nil {
						t.Error(err)
						return ports.Bundle{}
					}
					multi, ok := closure.(*script.MultisigClosure)
					if !ok {
						t.Error("unexpected selected closure")
						return ports.Bundle{}
					}
					for _, key := range multi.PubKeys {
						if string(schnorr.SerializePubKey(key)) != string(c.WalletPublicKey[:]) {
							skip = append(skip, key)
						}
					}
				}
				verified, err := script.VerifyTapscriptSigs(p, fetcher, script.WithSkipPublicKeys(skip...))
				if err != nil || len(verified) != len(p.Inputs) {
					t.Error("actual participant signature verification", err)
					return ports.Bundle{}
				}
			}
			return signed
		}
	})
}
