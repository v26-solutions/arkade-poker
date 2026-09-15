package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
)

type walletDelegator struct {
	info  ports.DelegatorInfo
	err   error
	calls int
}

func (d *walletDelegator) Info(ctx context.Context) (ports.DelegatorInfo, error) {
	d.calls++
	if err := ctx.Err(); err != nil {
		return ports.DelegatorInfo{}, err
	}
	return d.info, d.err
}
func (*walletDelegator) Close() error { return nil }

func TestDelegatedReceiveMatchesWebWallet(t *testing.T) {
	// Public metadata from mutinynet.arkade.money, using the SDK's default
	// delegated tree. No mnemonic or private key is needed for this regression.
	ownerBytes, _ := hex.DecodeString("fb277adfe078c4f6f27db2860f10014d5e09d4a893d241606aab679d6f077a1e")
	owner, err := schnorr.ParsePubKey(ownerBytes)
	if err != nil {
		t.Fatal(err)
	}
	server, err := compressedServiceKey("03301078808e4f7bc0dadfe29e34b1df8eaf0108ef06b1722274075ebc107a127a")
	if err != nil {
		t.Fatal(err)
	}
	delegate, err := compressedServiceKey("032903b15efe236d9609da10e536fb32cdf1d144778797bbf32a9b94e86601be6a")
	if err != nil {
		t.Fatal(err)
	}
	receive, err := receiveForPublicKey(owner, server, delegate, arklib.RelativeLocktime{Type: arklib.LocktimeTypeSecond, Value: 2048}, "mutinynet")
	if err != nil {
		t.Fatal(err)
	}
	const expected = "tark1qqcpq7yq3e8hhsx6ml3fud93m7827qggaurtzu3zwsr4a0qs0gf848r7x6ltk872jvnp4a3jwcrp84aywz8nj69eq9ecv5wc600ld7xe25p29y"
	if receive.Address != expected {
		t.Fatalf("web wallet address mismatch: %s", receive.Address)
	}
	address, err := arklib.DecodeAddressV0(expected)
	if err != nil {
		t.Fatal(err)
	}
	wantScript, err := address.GetPkScript()
	if err != nil || !bytes.Equal(receive.Script, wantScript) || len(receive.Tapscripts) != 3 {
		t.Fatal("default VTXO script differs from web wallet", err)
	}
}

func delegatedFixture(t testing.TB) (*Wallet, *walletArkd, *walletEmulator, *evidenceIndexer, *walletDelegator) {
	t.Helper()
	plain, ark, emu, index := policyFixture(t)
	d := &walletDelegator{info: ports.DelegatorInfo{PubKey: hex.EncodeToString(fixtureKey(5).secret.PubKey().SerializeCompressed())}}
	services := plain.services
	services.Delegator = d
	w, err := New(context.Background(), services, plain.key, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(w.config.Receive, plain.config.Receive) || d.calls != 1 {
		t.Fatal("delegator was not used for wallet discovery")
	}
	return w, ark, emu, index, d
}

func TestDelegatorDiscoveryAndSavedIdentity(t *testing.T) {
	w, ark, _, _, d := delegatedFixture(t)
	receive, err := DiscoverReceive(context.Background(), ark, d, w.key)
	if err != nil || !reflect.DeepEqual(receive, w.config.Receive) {
		t.Fatal("receive discovery differs from wallet", err)
	}
	if _, err := New(context.Background(), w.services, w.key, w.config); err != nil {
		t.Fatal("saved delegated wallet", err)
	}
	good := d.info
	for name, key := range map[string]string{
		"missing": "", "malformed": "zz" + strings.Repeat("00", 32),
		"xonly": good.PubKey[2:], "invalid point": "02" + strings.Repeat("ff", 32),
		"uncompressed": "04" + good.PubKey[2:],
	} {
		t.Run(name, func(t *testing.T) {
			d.info.PubKey = key
			if _, err := New(context.Background(), w.services, w.key, Config{}); !errors.Is(err, ErrPolicy) {
				t.Fatal("invalid delegate accepted", err)
			}
		})
	}
	d.info = good
	d.err = errors.New("delegator unavailable")
	if _, err := New(context.Background(), w.services, w.key, Config{}); !errors.Is(err, d.err) {
		t.Fatal("silently ignored delegator failure", err)
	}
	if _, err := DiscoverReceive(context.Background(), ark, d, w.key); !errors.Is(err, d.err) {
		t.Fatal("receive ignored delegator failure", err)
	}
	d.err = nil
	d.info.PubKey = hex.EncodeToString(fixtureKey(6).secret.PubKey().SerializeCompressed())
	if _, err := New(context.Background(), w.services, w.key, w.config); !errors.Is(err, ErrPolicy) {
		t.Fatal("silently rotated saved delegate key", err)
	}
	services := w.services
	services.Delegator = nil
	if _, err := New(context.Background(), services, w.key, w.config); !errors.Is(err, ErrPolicy) {
		t.Fatal("silently removed saved delegate", err)
	}
}

func TestDelegatedFundingSignAndSubmit(t *testing.T) {
	w, ark, emu, index, _ := delegatedFixture(t)
	addFundingCoin(index, w.config.Receive.Script, 1400, 1)
	funding, err := w.SelectFunding(context.Background(), 1200)
	if err != nil {
		t.Fatal(err)
	}
	if len(funding.Inputs) != 1 || funding.Change == nil || funding.Change.Value != 200 || !bytes.Equal(funding.Change.PkScript, w.config.Receive.Script) {
		t.Fatal("incorrect delegated funding/change")
	}
	input := funding.Inputs[0].Vtxo
	closure := new(script.MultisigClosure)
	ok, err := closure.Decode(input.Tapscript.RevealedScript)
	if err != nil || !ok || len(closure.PubKeys) != 2 || len(input.RevealedTapscripts) != 3 {
		t.Fatal("funding did not select owner + Arkd leaf", err)
	}
	for i, pub := range []*btcec.PublicKey{w.key.secret.PubKey(), fixtureKey(2).secret.PubKey()} {
		if !bytes.Equal(schnorr.SerializePubKey(closure.PubKeys[i]), schnorr.SerializePubKey(pub)) {
			t.Fatal("incorrect funding signer")
		}
	}
	main, checkpoints, err := offchain.BuildTxs([]offchain.VtxoInput{input}, []*wire.TxOut{wire.NewTxOut(1200, w.config.Receive.Script), funding.Change}, w.config.CheckpointScript)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := w.Prepare(&covenant.Unsigned{Ark: main, Checkpoints: checkpoints})
	if err != nil {
		t.Fatal(err)
	}
	signed, err := w.Sign(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	ark.submit = func(_ context.Context, b ports.Bundle) (ports.Submitted, error) {
		calls = append(calls, "submit")
		if !reflect.DeepEqual(b, signed) {
			t.Fatal("changed submission")
		}
		return ports.Submitted{TxID: main.UnsignedTx.TxHash().String(), Bundle: signAsServer(t, b)}, nil
	}
	ark.finalize = func(_ context.Context, id string, cps []string) error {
		calls = append(calls, "finalize")
		if id != main.UnsignedTx.TxHash().String() || len(cps) != len(checkpoints) {
			t.Fatal("incorrect finalization")
		}
		return nil
	}
	emu.sign = func(context.Context, ports.Bundle) (ports.Bundle, error) {
		t.Fatal("delegated funding was routed to emulator")
		return ports.Bundle{}, nil
	}
	result, err := w.Submit(context.Background(), ArkdRoute, signed)
	if err != nil || !reflect.DeepEqual(calls, []string{"submit", "finalize"}) {
		t.Fatal("delegated funding submission", calls, err)
	}
	for _, encoded := range append([]string{result.Bundle.Ark}, result.Bundle.Checkpoints...) {
		p, err := psbt.NewFromRawBytes(strings.NewReader(encoded), true)
		if err != nil {
			t.Fatal(err)
		}
		verified, err := script.VerifyTapscriptSigs(p, signingPrevouts(p))
		if err != nil || len(verified) != len(p.Inputs) {
			t.Fatal("delegated tree signatures or proofs invalid", err)
		}
	}
}
