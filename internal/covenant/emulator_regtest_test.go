//go:build !js && regtest

package covenant

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	native "arkade-poker/go/internal/adapters/grpc"
	web "arkade-poker/go/internal/adapters/http"
	"arkade-poker/go/internal/ports"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// This qualifies real emulator signatures on a synthetic, unbroadcast source.
// Emulator-first / participant-next matches poker's non-finalizer ordering.
// This fixture qualifies signing only; the wallet owns Arkd Submit and Finalize.
func TestRegtestEmulatorSigningAdapters(t *testing.T) {
	endpoint := func(name, fallback string) string {
		if value := os.Getenv(name); value != "" {
			return value
		}
		return fallback
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a, err := native.NewArkd(endpoint("POKER_ARKD_URL", "http://localhost:7070"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	info, err := a.Info(ctx)
	if err != nil || info.Network != "regtest" {
		t.Fatal("regtest discovery", err)
	}
	n, err := native.NewUnverifiedEmulator(endpoint("POKER_EMULATOR_URL", "http://localhost:7073"))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	w, err := web.NewUnverifiedEmulator(endpoint("POKER_EMULATOR_URL", "http://localhost:7073"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	emuInfo, err := n.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	parseKey := func(text string) *btcec.PublicKey {
		data, err := hex.DecodeString(text)
		if err != nil {
			t.Fatal(err)
		}
		var key *btcec.PublicKey
		if len(data) == 32 {
			key, err = schnorr.ParsePubKey(data)
		} else {
			key, err = btcec.ParsePubKey(data)
		}
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	server, emulator := parseKey(info.Signer), parseKey(emuInfo.Signer)
	checkpoint, err := hex.DecodeString(info.CheckpointScript)
	if err != nil {
		t.Fatal(err)
	}
	_, owner := btcec.PrivKeyFromBytes([]byte{31})
	program := []byte{txscript.OP_TRUE}
	tweaked := arkade.ComputeArkadeScriptPublicKey(emulator, arkade.ArkadeScriptHash(program))
	closure := &script.MultisigClosure{PubKeys: []*btcec.PublicKey{tweaked, owner, server}}
	tree := &script.TapscriptsVtxoScript{Closures: []script.Closure{closure}}
	key, proofs, err := tree.TapTree()
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := closure.Script()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := proofs.GetTaprootMerkleProof(txscript.NewBaseTapLeaf(leaf).TapHash())
	if err != nil {
		t.Fatal(err)
	}
	source, _, _ := sourceFixture(t, 1000, 230)
	source.Vtxo.RevealedTapscripts, err = tree.Encode()
	if err != nil {
		t.Fatal(err)
	}
	source.Vtxo.Tapscript.RevealedScript = proof.Script
	source.Vtxo.Tapscript.ControlBlock, err = txscript.ParseControlBlock(proof.ControlBlock)
	if err != nil {
		t.Fatal(err)
	}
	source.PreviousTx.TxOut[0].PkScript, err = script.P2TRScript(key)
	if err != nil {
		t.Fatal(err)
	}
	source.Vtxo.Outpoint.Hash = source.PreviousTx.TxHash()
	ext, err := ActionExtension(nil, arkade.EmulatorEntry{Vin: 0, Script: program})
	if err != nil {
		t.Fatal(err)
	}
	packet, err := ext.TxOut()
	if err != nil {
		t.Fatal(err)
	}
	payout, err := script.P2TRScript(owner)
	if err != nil {
		t.Fatal(err)
	}
	built, err := buildUnsigned([]Source{source}, []*wire.TxOut{wire.NewTxOut(1000, payout), packet}, checkpoint, [32]byte(schnorr.SerializePubKey(server)), nil)
	if err != nil {
		t.Fatal(err)
	}
	input := ports.Bundle{}
	input.Ark, err = built.Ark.B64Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, cp := range built.Checkpoints {
		text, err := cp.B64Encode()
		if err != nil {
			t.Fatal(err)
		}
		input.Checkpoints = append(input.Checkpoints, text)
	}
	if path := os.Getenv("POKER_EMULATOR_FIXTURE_FILE"); path != "" {
		data, err := json.Marshal(struct {
			Bundle   ports.Bundle
			SkipKeys []string
		}{input, []string{hex.EncodeToString(schnorr.SerializePubKey(owner)), hex.EncodeToString(schnorr.SerializePubKey(server))}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, adapter := range map[string]ports.Emulator{"native": n, "http": w} {
		t.Run(name, func(t *testing.T) {
			result, err := adapter.Sign(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			original := append([]string{input.Ark}, input.Checkpoints...)
			returned := append([]string{result.Ark}, result.Checkpoints...)
			if len(original) != len(returned) {
				t.Fatal("checkpoint count changed")
			}
			for i, text := range returned {
				p, err := psbt.NewFromRawBytes(strings.NewReader(text), true)
				if err != nil {
					t.Fatal(err)
				}
				before, err := psbt.NewFromRawBytes(strings.NewReader(original[i]), true)
				if err != nil {
					t.Fatal(err)
				}
				fetcher := txscript.NewMultiPrevOutFetcher(nil)
				for j, in := range p.Inputs {
					fetcher.AddPrevOut(p.UnsignedTx.TxIn[j].PreviousOutPoint, in.WitnessUtxo)
				}
				signed, err := script.VerifyTapscriptSigs(p, fetcher, script.WithSkipPublicKeys(owner, server))
				if err != nil || len(signed) != 1 {
					t.Fatal("emulator signature verification", signed, err)
				}
				for j := range p.Inputs {
					p.Inputs[j].TaprootScriptSpendSig = nil
				}
				if !reflect.DeepEqual(p, before) {
					t.Fatal("emulator altered non-signature fields")
				}
			}
			t.Logf("emulator %s signed main + %d checkpoints; signatures verified; no Arkd submission", emuInfo.Version, len(result.Checkpoints))
		})
	}
}
