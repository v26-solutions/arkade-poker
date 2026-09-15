package wallet

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
)

func spendingFixture(t testing.TB) (*Wallet, *walletArkd, *walletEmulator, ports.Bundle, ports.Bundle) {
	t.Helper()
	w, a, e, index := policyFixture(t)
	addFundingCoin(index, w.config.Receive.Script, 700, 1)
	addFundingCoin(index, w.config.Receive.Script, 500, 2)
	funding, err := w.SelectFunding(context.Background(), 1200)
	if err != nil {
		t.Fatal(err)
	}
	var inputs []offchain.VtxoInput
	for _, s := range funding.Inputs {
		inputs = append(inputs, s.Vtxo)
	}
	main, cps, err := offchain.BuildTxs(inputs, []*wire.TxOut{wire.NewTxOut(1200, w.config.Receive.Script)}, w.config.CheckpointScript)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := w.Prepare(&covenant.Unsigned{Ark: main, Checkpoints: cps})
	if err != nil {
		t.Fatal(err)
	}
	main.Unknowns = append(main.Unknowns, &psbt.Unknown{Key: []byte{0xfc, 0x77}, Value: []byte("global")})
	for _, p := range append([]*psbt.Packet{main}, cps...) {
		for j := range p.Inputs {
			p.Inputs[j].Unknowns = append(p.Inputs[j].Unknowns, &psbt.Unknown{Key: []byte{0xfc, 0x42}, Value: []byte("saved origin")})
		}
	}
	prepared, err := w.Prepare(&covenant.Unsigned{Ark: main, Checkpoints: cps})
	if err != nil {
		t.Fatal(err)
	}
	return w, a, e, prepared, plain
}

func signAsServer(t testing.TB, b ports.Bundle) ports.Bundle {
	t.Helper()
	signer := &Wallet{key: fixtureKey(2)}
	signed, err := signer.Sign(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestSignExactUpstreamBundle(t *testing.T) {
	w, _, _, prepared, _ := spendingFixture(t)
	original := ports.Bundle{Ark: prepared.Ark, Checkpoints: append([]string(nil), prepared.Checkpoints...)}
	signed, err := w.Sign(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prepared, original) || signed.Ark == prepared.Ark {
		t.Fatal("input changed or unsigned")
	}
	if err := CompareBundles(prepared, signed); err != nil {
		t.Fatal(err)
	}
	server := fixtureKey(2).secret.PubKey()
	for _, encoded := range append([]string{signed.Ark}, signed.Checkpoints...) {
		p, err := psbt.NewFromRawBytes(strings.NewReader(encoded), true)
		if err != nil {
			t.Fatal(err)
		}
		ok, err := script.VerifyTapscriptSigs(p, signingPrevouts(p), script.WithSkipPublicKeys(server))
		if err != nil || len(ok) != len(p.Inputs) {
			t.Fatal("upstream participant signature verification", err)
		}
	}
	again, err := w.Sign(context.Background(), prepared)
	if err != nil || !reflect.DeepEqual(signed, again) {
		t.Fatal("deterministic signatures", err)
	}
	// Explicit default sighash and opaque metadata must survive signing even
	// when upstream's serializer would omit an explicit zero-value field.
	_, maps, err := packetMaps(prepared.Ark)
	if err != nil {
		t.Fatal(err)
	}
	maps[1] = append(maps[1], psbtField{key: []byte{byte(psbt.SighashType)}, value: []byte{0, 0, 0, 0}})
	prepared.Ark = encodeMaps(maps)
	if signed, err = w.Sign(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	if err := CompareBundles(prepared, signed); err != nil {
		t.Fatal("explicit defaults lost", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.Sign(ctx, prepared); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSignRejectsMissingContext(t *testing.T) {
	w, _, _, prepared, _ := spendingFixture(t)
	for name, change := range map[string]func(*psbt.Packet){
		"prevout": func(p *psbt.Packet) { p.Inputs[0].WitnessUtxo = nil },
		"leaf":    func(p *psbt.Packet) { p.Inputs[0].TaprootLeafScript = nil },
		"sighash": func(p *psbt.Packet) { p.Inputs[0].SighashType = txscript.SigHashAll },
	} {
		t.Run(name, func(t *testing.T) {
			p, _, err := unsignedPSBT(prepared.Ark)
			if err != nil {
				t.Fatal(err)
			}
			change(p)
			encoded, err := p.B64Encode()
			if err != nil {
				t.Fatal(err)
			}
			bad := ports.Bundle{Ark: encoded, Checkpoints: prepared.Checkpoints}
			if _, err := w.Sign(context.Background(), bad); err == nil {
				t.Fatal("accepted incomplete signing context")
			}
		})
	}
}

func TestArkdSubmitRebuiltCheckpointsAndExactRetry(t *testing.T) {
	w, a, _, prepared, plain := spendingFixture(t)
	signed, err := w.Sign(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	remote := signAsServer(t, signed)
	// The actual upstream builder recreates checkpoints without saved origin
	// metadata or participant signatures. Its main transaction remains intact.
	rebuilt := signAsServer(t, plain)
	remote.Checkpoints = []string{rebuilt.Checkpoints[1], rebuilt.Checkpoints[0]}
	var calls []string
	finalizeErr := errors.New("lost finalize response")
	a.submit = func(_ context.Context, b ports.Bundle) (ports.Submitted, error) {
		calls = append(calls, "submit")
		if !reflect.DeepEqual(b, signed) {
			t.Fatal("saved submission changed")
		}
		p, _, _ := unsignedPSBT(b.Ark)
		return ports.Submitted{TxID: p.UnsignedTx.TxHash().String(), Bundle: remote}, nil
	}
	a.finalize = func(_ context.Context, id string, cps []string) error {
		calls = append(calls, "finalize")
		merged := ports.Bundle{Ark: remote.Ark, Checkpoints: cps}
		if err := CompareBundles(signed, merged); err != nil {
			t.Fatal("saved origin metadata lost", err)
		}
		for _, encoded := range cps {
			p, err := psbt.NewFromRawBytes(strings.NewReader(encoded), true)
			if err != nil {
				t.Fatal(err)
			}
			ok, err := script.VerifyTapscriptSigs(p, signingPrevouts(p))
			if err != nil || len(ok) != len(p.Inputs) {
				t.Fatal("final checkpoint signatures", err)
			}
		}
		return finalizeErr
	}
	if _, err := w.Submit(context.Background(), ArkdRoute, signed); !errors.Is(err, finalizeErr) || !strings.Contains(err.Error(), "Arkd finalize") {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"submit", "finalize"}) {
		t.Fatal("hidden retry", calls)
	}
	finalizeErr = nil
	got, err := w.RetrySaved(context.Background(), ArkdRoute, signed)
	if err != nil || got.TxID == "" {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"submit", "finalize", "submit", "finalize"}) {
		t.Fatal(calls)
	}
}

func signingPrevouts(p *psbt.Packet) txscript.PrevOutputFetcher {
	f := txscript.NewMultiPrevOutFetcher(nil)
	for i, input := range p.Inputs {
		f.AddPrevOut(p.UnsignedTx.TxIn[i].PreviousOutPoint, input.WitnessUtxo)
	}
	return f
}

func emulatorSpendingFixture(t *testing.T, emulatorFinalizes bool) (*Wallet, *walletArkd, *walletEmulator, ports.Bundle, ports.Bundle) {
	t.Helper()
	w, a, e, index := policyFixture(t)
	// A real upstream-built three-signer leaf exercises non-ordinary routing.
	// The controlled service test does not claim live covenant acceptance.
	closure := &script.MultisigClosure{PubKeys: []*btcec.PublicKey{fixtureKey(3).secret.PubKey(), w.key.secret.PubKey(), fixtureKey(2).secret.PubKey()}}
	if emulatorFinalizes {
		closure.PubKeys[0], closure.PubKeys[1] = closure.PubKeys[1], closure.PubKeys[0]
	}
	tree := &script.TapscriptsVtxoScript{Closures: []script.Closure{closure}}
	tapKey, proofs, err := tree.TapTree()
	if err != nil {
		t.Fatal(err)
	}
	pkScript, err := script.P2TRScript(tapKey)
	if err != nil {
		t.Fatal(err)
	}
	point := addFundingCoin(index, pkScript, 1000, 1)
	leaf, err := closure.Script()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := proofs.GetTaprootMerkleProof(txscript.NewBaseTapLeaf(leaf).TapHash())
	if err != nil {
		t.Fatal(err)
	}
	control, err := txscript.ParseControlBlock(proof.ControlBlock)
	if err != nil {
		t.Fatal(err)
	}
	leaves, err := tree.Encode()
	if err != nil {
		t.Fatal(err)
	}
	addFundingCoin(index, w.config.Receive.Script, 500, 2)
	funding, err := w.SelectFunding(context.Background(), 500)
	if err != nil {
		t.Fatal(err)
	}
	inputs := []offchain.VtxoInput{{Outpoint: &point, Amount: 1000, RevealedTapscripts: leaves, Tapscript: &waddrmgr.Tapscript{ControlBlock: control, RevealedScript: leaf}}, funding.Inputs[0].Vtxo}
	main, cps, err := offchain.BuildTxs(inputs, []*wire.TxOut{wire.NewTxOut(1500, w.config.Receive.Script)}, w.config.CheckpointScript)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := w.Prepare(&covenant.Unsigned{Ark: main, Checkpoints: cps})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range append([]*psbt.Packet{main}, cps...) {
		for i := range p.Inputs {
			p.Inputs[i].Unknowns = append(p.Inputs[i].Unknowns, &psbt.Unknown{Key: []byte{0xfc, 0x42}, Value: []byte("saved origin")})
		}
	}
	prepared, err := w.Prepare(&covenant.Unsigned{Ark: main, Checkpoints: cps})
	if err != nil {
		t.Fatal(err)
	}
	return w, a, e, prepared, plain
}

// The emulator signs the covenant input and its checkpoint, leaving ordinary
// wallet inputs untouched. These fixture keys do not claim live acceptance.
func signAsEmulator(t *testing.T, b ports.Bundle) ports.Bundle {
	t.Helper()
	remote, err := (&Wallet{key: fixtureKey(3)}).Sign(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	_, original, err := packetMaps(b.Ark)
	if err != nil {
		t.Fatal(err)
	}
	_, signed, err := packetMaps(remote.Ark)
	if err != nil {
		t.Fatal(err)
	}
	signed[2] = original[2]
	remote.Ark = encodeMaps(signed)
	remote.Checkpoints[1] = b.Checkpoints[1]
	return remote
}

func TestEmulatorSignsThenClientSubmitsAndFinalizes(t *testing.T) {
	for _, failedStage := range []string{"", "emulator sign", "Arkd submit", "Arkd finalize"} {
		t.Run(failedStage, func(t *testing.T) {
			w, a, e, prepared, plain := emulatorSpendingFixture(t, false)
			signed, err := w.Sign(context.Background(), prepared)
			if err != nil {
				t.Fatal(err)
			}
			original := ports.Bundle{Ark: signed.Ark, Checkpoints: append([]string(nil), signed.Checkpoints...)}
			var calls []string
			failure := errors.New("service HTTP status 500")
			var emulated ports.Bundle
			e.sign = func(_ context.Context, b ports.Bundle) (ports.Bundle, error) {
				calls = append(calls, "emulator sign")
				if !reflect.DeepEqual(b, original) {
					t.Fatal("exact saved emulator request changed")
				}
				if failedStage == "emulator sign" {
					return ports.Bundle{}, failure
				}
				emulated = signAsEmulator(t, b)
				return emulated, nil
			}
			a.submit = func(_ context.Context, b ports.Bundle) (ports.Submitted, error) {
				calls = append(calls, "Arkd submit")
				if !reflect.DeepEqual(b, emulated) {
					t.Fatal("Arkd did not receive merged emulator signatures")
				}
				if failedStage == "Arkd submit" {
					return ports.Submitted{}, failure
				}
				remote := signAsServer(t, b)
				// Arkd rebuilds and reorders checkpoints, omitting both earlier
				// signers and opaque metadata. Finalize must receive all of them.
				rebuilt := signAsServer(t, plain)
				remote.Checkpoints = []string{rebuilt.Checkpoints[1], rebuilt.Checkpoints[0]}
				p, _, err := unsignedPSBT(b.Ark)
				if err != nil {
					t.Fatal(err)
				}
				return ports.Submitted{TxID: p.UnsignedTx.TxHash().String(), Bundle: remote}, nil
			}
			a.finalize = func(_ context.Context, id string, cps []string) error {
				calls = append(calls, "Arkd finalize")
				main, _, err := unsignedPSBT(original.Ark)
				if err != nil || id != main.UnsignedTx.TxHash().String() {
					t.Fatal("finalize identity", err)
				}
				if err := CompareBundles(original, ports.Bundle{Ark: original.Ark, Checkpoints: cps}); err != nil {
					t.Fatal("finalize metadata", err)
				}
				for _, encoded := range cps {
					p, err := psbt.NewFromRawBytes(strings.NewReader(encoded), true)
					if err != nil {
						t.Fatal(err)
					}
					ok, err := script.VerifyTapscriptSigs(p, signingPrevouts(p))
					if err != nil || len(ok) != len(p.Inputs) {
						t.Fatal("missing/invalid final checkpoint signatures", err)
					}
				}
				if failedStage == "Arkd finalize" {
					return failure
				}
				return nil
			}
			if _, err := w.Submit(context.Background(), ArkdRoute, signed); err == nil || len(calls) != 0 {
				t.Fatal("wrong route sent to service", err)
			}
			got, err := w.Submit(context.Background(), EmulatorRoute, signed)
			want := []string{"emulator sign", "Arkd submit", "Arkd finalize"}
			if failedStage != "" {
				if !errors.Is(err, failure) || !strings.Contains(err.Error(), failedStage) {
					t.Fatal("lost failure stage", err)
				}
				for i, stage := range want {
					if stage == failedStage {
						want = want[:i+1]
						break
					}
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatal("unexpected service order/retry", calls)
			}
			if failedStage != "" {
				failedStage = ""
				calls = nil
				got, err = w.RetrySaved(context.Background(), EmulatorRoute, signed)
				if err != nil || !reflect.DeepEqual(calls, []string{"emulator sign", "Arkd submit", "Arkd finalize"}) {
					t.Fatal("exact retry", err, calls)
				}
			}
			if err := CompareBundles(original, got.Bundle); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(original, signed) {
				t.Fatal("saved work mutated")
			}
		})
	}
}

func TestRejectEmulatorFinalizerLeafBeforeNetwork(t *testing.T) {
	w, a, e, prepared, _ := emulatorSpendingFixture(t, true)
	e.sign = func(context.Context, ports.Bundle) (ports.Bundle, error) {
		t.Fatal("emulator must not finalize")
		return ports.Bundle{}, nil
	}
	a.submit = func(context.Context, ports.Bundle) (ports.Submitted, error) {
		t.Fatal("wrong leaf submitted")
		return ports.Submitted{}, nil
	}
	if _, err := w.Submit(context.Background(), EmulatorRoute, prepared); err == nil || !strings.Contains(err.Error(), "requires client finalization") {
		t.Fatal(err)
	}
}

func TestServiceMergeRejectsChanges(t *testing.T) {
	w, _, _, prepared, _ := spendingFixture(t)
	signed, err := w.Sign(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	good := signAsServer(t, signed)
	for name, change := range map[string]func(*ports.Bundle){
		"amount": func(b *ports.Bundle) {
			p, _ := psbt.NewFromRawBytes(strings.NewReader(b.Ark), true)
			p.UnsignedTx.TxOut[0].Value++
			b.Ark, _ = p.B64Encode()
		},
		"metadata": func(b *ports.Bundle) {
			p, _ := psbt.NewFromRawBytes(strings.NewReader(b.Ark), true)
			p.Unknowns[0].Value[0] ^= 1
			b.Ark, _ = p.B64Encode()
		},
		"witness prevout": func(b *ports.Bundle) {
			p, _ := psbt.NewFromRawBytes(strings.NewReader(b.Checkpoints[0]), true)
			p.Inputs[0].WitnessUtxo.Value++
			b.Checkpoints[0], _ = p.B64Encode()
		},
		"local signature": func(b *ports.Bundle) {
			_, maps, _ := packetMaps(b.Ark)
			for j := range maps[1] {
				f := &maps[1][j]
				public := w.key.PublicKey()
				if len(f.key) == 65 && bytes.Equal(f.key[1:33], public[:]) {
					f.value[0] ^= 1
				}
			}
			b.Ark = encodeMaps(maps)
		},
		"removed main signature": func(b *ports.Bundle) { p, _, _ := unsignedPSBT(b.Ark); b.Ark, _ = p.B64Encode() },
		"missing checkpoint":     func(b *ports.Bundle) { b.Checkpoints = b.Checkpoints[:1] },
		"duplicate checkpoint":   func(b *ports.Bundle) { b.Checkpoints[1] = b.Checkpoints[0] },
		"added rebuilt metadata": func(b *ports.Bundle) {
			p, _ := psbt.NewFromRawBytes(strings.NewReader(b.Checkpoints[0]), true)
			p.Inputs[0].Unknowns = append(p.Inputs[0].Unknowns, &psbt.Unknown{Key: []byte{0xfc, 99}, Value: []byte{1}})
			b.Checkpoints[0], _ = p.B64Encode()
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := ports.Bundle{Ark: good.Ark, Checkpoints: append([]string(nil), good.Checkpoints...)}
			change(&bad)
			if _, err := mergeBundles(signed, bad, true); err == nil {
				t.Fatal("service mutation accepted")
			}
		})
	}
	// Missing local signatures are allowed only on rebuilt checkpoints, and
	// arbitrary missing metadata is not equivalent to the builder projection.
	_, maps, _ := packetMaps(good.Checkpoints[0])
	var kept []psbtField
	for _, f := range maps[1] {
		if bytes.Equal(f.key, append([]byte{txutils.ArkPsbtFieldKeyType}, txutils.ArkFieldTaprootTree...)) {
			continue
		}
		kept = append(kept, f)
	}
	maps[1] = kept
	good.Checkpoints[0] = encodeMaps(maps)
	if _, err := mergeBundles(signed, good, true); err == nil {
		t.Fatal("removed full tree accepted")
	}
}
