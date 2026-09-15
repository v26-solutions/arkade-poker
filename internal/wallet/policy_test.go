package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

type walletArkd struct {
	info     ports.ArkInfo
	infoErr  error
	submit   func(context.Context, ports.Bundle) (ports.Submitted, error)
	finalize func(context.Context, string, []string) error
}

func (s *walletArkd) Info(context.Context) (ports.ArkInfo, error) { return s.info, s.infoErr }
func (s *walletArkd) Submit(ctx context.Context, b ports.Bundle) (ports.Submitted, error) {
	return s.submit(ctx, b)
}
func (s *walletArkd) Finalize(ctx context.Context, id string, cps []string) error {
	return s.finalize(ctx, id, cps)
}
func (*walletArkd) Close() error { return nil }

type walletEmulator struct {
	info ports.EmulatorInfo
	sign func(context.Context, ports.Bundle) (ports.Bundle, error)
}

func (s *walletEmulator) Info(context.Context) (ports.EmulatorInfo, error) { return s.info, nil }
func (s *walletEmulator) Sign(ctx context.Context, b ports.Bundle) (ports.Bundle, error) {
	return s.sign(ctx, b)
}
func (*walletEmulator) Close() error { return nil }

func fixtureKey(n byte) *Key {
	secret, _ := btcec.PrivKeyFromBytes([]byte{n})
	return &Key{secret: secret}
}
func policyFixture(t testing.TB) (*Wallet, *walletArkd, *walletEmulator, *evidenceIndexer) {
	t.Helper()
	server, emulator, forfeit := fixtureKey(2), fixtureKey(3), fixtureKey(4)
	checkpoint, err := (&script.CSVMultisigClosure{MultisigClosure: script.MultisigClosure{PubKeys: []*btcec.PublicKey{forfeit.secret.PubKey()}}, Locktime: arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 16}}).Script()
	if err != nil {
		t.Fatal(err)
	}
	ark := &walletArkd{info: ports.ArkInfo{Network: "regtest", Signer: hex.EncodeToString(server.secret.PubKey().SerializeCompressed()), Forfeit: hex.EncodeToString(forfeit.secret.PubKey().SerializeCompressed()), CheckpointScript: hex.EncodeToString(checkpoint), ExitDelay: 512, Dust: 100, MinVtxo: 0, MaxVtxo: -1}}
	emu := &walletEmulator{info: ports.EmulatorInfo{Signer: hex.EncodeToString(emulator.secret.PubKey().SerializeCompressed())}}
	index := &evidenceIndexer{records: make(map[wire.OutPoint]ports.Vtxo), txs: make(map[chainhash.Hash]*wire.MsgTx)}
	w, err := New(context.Background(), Services{Arkd: ark, Emulator: emu, Indexer: index, Now: func() time.Time { return time.Unix(1000, 0) }}, fixtureKey(1), Config{Network: "regtest"})
	if err != nil {
		t.Fatal(err)
	}
	return w, ark, emu, index
}

func TestPolicyAdmissionAndSnapshot(t *testing.T) {
	w, ark, emu, _ := policyFixture(t)
	c, _ := w.Config()
	if c.OutputPolicy != (OutputPolicy{100, btcutil.MaxSatoshi}) {
		t.Fatal(c.OutputPolicy)
	}
	if _, err := New(context.Background(), w.services, w.key, c); err != nil {
		t.Fatal("saved snapshot", err)
	}
	c.Receive.Script[0] ^= 1
	c.CheckpointScript[0] ^= 1
	c.Receive.Tapscripts[0] = "00"
	if reflect.DeepEqual(c, w.config) {
		t.Fatal("configuration aliases wallet")
	}
	if _, err := New(context.Background(), w.services, w.key, c); !errors.Is(err, ErrPolicy) {
		t.Fatal("rotated saved config", err)
	}
	goodArk, goodEmu := ark.info, emu.info
	for name, change := range map[string]func(){
		"network":          func() { ark.info.Network = "signet" },
		"xonly server":     func() { ark.info.Signer = ark.info.Signer[2:] },
		"same emulator":    func() { emu.info.Signer = ark.info.Signer },
		"forfeit mismatch": func() { ark.info.Forfeit = ark.info.Signer },
		"zero exit":        func() { ark.info.ExitDelay = 0 },
		"rounded exit":     func() { ark.info.ExitDelay = 513 },
		"long exit":        func() { ark.info.ExitDelay = 65536 * 512 },
		"zero dust":        func() { ark.info.Dust = 0 },
		"negative minimum": func() { ark.info.MinVtxo = -1 },
		"maximum too low":  func() { ark.info.MaxVtxo = 99 },
		"excess maximum":   func() { ark.info.MaxVtxo = btcutil.MaxSatoshi + 1 },
		"bad checkpoint":   func() { ark.info.CheckpointScript = "51" },
		"reserved sequence": func() {
			b, _ := txscript.NewScriptBuilder().AddInt64(0x10010).AddOps([]byte{txscript.OP_CHECKSEQUENCEVERIFY, txscript.OP_DROP}).AddData(fixtureKey(4).secret.PubKey().SerializeCompressed()[1:]).AddOp(txscript.OP_CHECKSIG).Script()
			ark.info.CheckpointScript = hex.EncodeToString(b)
		},
	} {
		t.Run(name, func(t *testing.T) {
			ark.info, emu.info = goodArk, goodEmu
			change()
			if _, err := New(context.Background(), w.services, w.key, Config{Network: "regtest"}); !errors.Is(err, ErrPolicy) {
				t.Fatal(err)
			}
		})
	}
	ark.info, emu.info = goodArk, goodEmu
	for _, seconds := range []uint32{512, 65535 * 512} {
		cp, err := (&script.CSVMultisigClosure{MultisigClosure: script.MultisigClosure{PubKeys: []*btcec.PublicKey{fixtureKey(4).secret.PubKey()}}, Locktime: arklib.RelativeLocktime{Type: arklib.LocktimeTypeSecond, Value: seconds}}).Script()
		if err != nil {
			t.Fatal(err)
		}
		ark.info.CheckpointScript = hex.EncodeToString(cp)
		if _, err := New(context.Background(), w.services, w.key, Config{Network: "regtest"}); err != nil {
			t.Fatal("canonical seconds checkpoint", err)
		}
	}
	ark.info = goodArk
	ark.infoErr = errors.New("discovery offline")
	if _, err := New(context.Background(), w.services, w.key, Config{Network: "regtest"}); !errors.Is(err, ark.infoErr) {
		t.Fatal(err)
	}
}

func TestNetworkDiscoveredFromArkd(t *testing.T) {
	w, ark, _, _ := policyFixture(t)
	defer w.key.Destroy()
	for _, test := range []struct{ reported, network, prefix string }{
		{"mutinynet", "mutinynet", "tark1"}, {"regtest", "regtest", "tark1"},
		{"bitcoin", "bitcoin", "ark1"}, {"unknown", "bitcoin", "ark1"},
	} {
		ark.info.Network = test.reported
		fresh, err := New(context.Background(), w.services, w.key, Config{})
		if err != nil {
			t.Fatalf("%s: %v", test.reported, err)
		}
		c, err := fresh.Config()
		if err != nil || c.Network != test.network || !strings.HasPrefix(c.Receive.Address, test.prefix) {
			t.Fatalf("discovered %s: %+v, %v", test.reported, c, err)
		}
		receive, err := DiscoverReceive(context.Background(), ark, nil, w.key)
		if err != nil || !reflect.DeepEqual(receive, c.Receive) {
			t.Fatalf("receive discovery disagrees: %v", err)
		}
		if _, err := New(context.Background(), w.services, w.key, c); err != nil {
			t.Fatalf("restore: %v", err)
		}
		ark.info.Network = "signet"
		if _, err := New(context.Background(), w.services, w.key, c); !errors.Is(err, ErrPolicy) {
			t.Fatalf("restored network changed: %v", err)
		}
	}
}

func TestAgreementOutputPolicy(t *testing.T) {
	w, _, _, _ := policyFixture(t)
	p := covenant.Params{ArkSigningKey: w.config.ArkSigningKey, EmulatorSigningKey: w.config.EmulatorSigningKey, Stake: 1000, Bond: 100, MaxWager: 2000, Players: covenant.PerPlayer[covenant.Participant]{Player1: covenant.Participant{SigningKey: w.key.PublicKey(), PayoutScript: bytes.Clone(w.config.Receive.Script)}, Player2: covenant.Participant{SigningKey: fixtureKey(5).PublicKey(), PayoutScript: p2tr(5)}}}
	if err := w.AdmitAgreement(p); err != nil {
		t.Fatal(err)
	}
	p.Bond = 99
	if err := w.AdmitAgreement(p); err == nil {
		t.Fatal("dust refund")
	}
	p.Bond = 100
	w.config.OutputPolicy.MaxAmount = 6199
	if err := w.AdmitAgreement(p); err == nil {
		t.Fatal("oversized timeout payout")
	}
	p.Stake = btcutil.MaxSatoshi
	if err := w.AdmitAgreement(p); err == nil {
		t.Fatal("overflow")
	}
}

func addFundingCoin(index *evidenceIndexer, script []byte, amount int64, nonce uint32) wire.OutPoint {
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Index: nonce}, nil, nil))
	tx.AddTxOut(wire.NewTxOut(amount, bytes.Clone(script)))
	point := wire.OutPoint{Hash: tx.TxHash()}
	index.txs[point.Hash] = tx
	index.records[point] = ports.Vtxo{Outpoint: point, Script: bytes.Clone(script), Amount: amount, ExpiresAt: 2000}
	return point
}

func TestFundingSelectionAndEvidence(t *testing.T) {
	t.Run("exact first and owned sources", func(t *testing.T) {
		w, _, _, index := policyFixture(t)
		addFundingCoin(index, w.config.Receive.Script, 2000, 1)
		exact := addFundingCoin(index, w.config.Receive.Script, 1000, 2)
		f, err := w.SelectFunding(context.Background(), 1000)
		if err != nil || len(f.Inputs) != 1 || *f.Inputs[0].Vtxo.Outpoint != exact || f.Change != nil {
			t.Fatal(f, err)
		}
		f.Inputs[0].PreviousTx.TxOut[0].Value++
		f.Inputs[0].Vtxo.RevealedTapscripts[0] = "00"
		again, err := w.SelectFunding(context.Background(), 1000)
		if err != nil || again.Inputs[0].PreviousTx.TxOut[0].Value != 1000 || again.Inputs[0].Vtxo.RevealedTapscripts[0] == "00" {
			t.Fatal("aliased funding", err)
		}
	})
	t.Run("change without fee", func(t *testing.T) {
		w, _, _, index := policyFixture(t)
		addFundingCoin(index, w.config.Receive.Script, 1001, 1)
		addFundingCoin(index, w.config.Receive.Script, 99, 2)
		f, err := w.SelectFunding(context.Background(), 1000)
		if err != nil || len(f.Inputs) != 2 || f.Change.Value != 100 {
			t.Fatal(f, err)
		}
	})
	t.Run("skip oversized change", func(t *testing.T) {
		w, _, _, index := policyFixture(t)
		w.config.OutputPolicy.MaxAmount = 500
		addFundingCoin(index, w.config.Receive.Script, 2000, 1)
		smaller := addFundingCoin(index, w.config.Receive.Script, 1100, 2)
		f, err := w.SelectFunding(context.Background(), 1000)
		if err != nil || len(f.Inputs) != 1 || *f.Inputs[0].Vtxo.Outpoint != smaller {
			t.Fatal(f, err)
		}
	})
	t.Run("unusable change", func(t *testing.T) {
		w, _, _, index := policyFixture(t)
		addFundingCoin(index, w.config.Receive.Script, 1001, 1)
		if _, err := w.SelectFunding(context.Background(), 1000); err == nil {
			t.Fatal("dust burned")
		}
	})
	for name, change := range map[string]func(*ports.Vtxo){
		"expired": func(v *ports.Vtxo) { v.ExpiresAt = 1000 }, "spent": func(v *ports.Vtxo) { v.Spent = true }, "swept": func(v *ports.Vtxo) { v.Swept = true }, "unrolled": func(v *ports.Vtxo) { v.Unrolled = true }, "spentBy": func(v *ports.Vtxo) { v.SpentBy = &chainhash.Hash{1} }, "arkTx": func(v *ports.Vtxo) { v.ArkTxID = &chainhash.Hash{1} }, "settled": func(v *ports.Vtxo) { v.SettledBy = &chainhash.Hash{1} }, "asset": func(v *ports.Vtxo) { v.Assets = []ports.Asset{{ID: "asset", Amount: 1}} },
	} {
		t.Run(name, func(t *testing.T) {
			w, _, _, index := policyFixture(t)
			point := addFundingCoin(index, w.config.Receive.Script, 1000, 1)
			v := index.records[point]
			change(&v)
			index.records[point] = v
			_, err := w.SelectFunding(context.Background(), 1000)
			var insufficient InsufficientFunds
			if !errors.As(err, &insufficient) || insufficient.Available != 0 || index.txCalls != 0 {
				t.Fatal(err, index.txCalls)
			}
		})
	}
	for _, kind := range []string{"missing expiry", "amount mismatch", "missing transaction", "query failure"} {
		t.Run(kind, func(t *testing.T) {
			w, _, _, index := policyFixture(t)
			point := addFundingCoin(index, w.config.Receive.Script, 1000, 1)
			v := index.records[point]
			switch kind {
			case "missing expiry":
				v.ExpiresAt = 0
			case "amount mismatch":
				v.Amount++
			case "missing transaction":
				delete(index.txs, point.Hash)
			case "query failure":
				index.err = errors.New("offline")
			}
			index.records[point] = v
			if _, err := w.SelectFunding(context.Background(), 1000); err == nil {
				t.Fatal("accepted bad funding evidence")
			}
		})
	}
}
