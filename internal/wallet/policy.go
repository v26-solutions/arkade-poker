package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"math"
	"reflect"
	"slices"
	"time"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	clientlib "github.com/arkade-os/arkd/pkg/client-lib"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/txscript"
)

var ErrPolicy = errors.New("wallet: service or output policy mismatch")

// New discovers a fresh wallet, including its network, from service metadata.
// A complete saved configuration must match discovery exactly; partial
// overrides never silently rotate a session's identity, tree or output policy.
func New(ctx context.Context, services Services, key *Key, config Config) (*Wallet, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if key == nil || key.secret == nil || key.secret.Key.IsZero() {
		return nil, ErrKey
	}
	if services.Arkd == nil || services.Emulator == nil || services.Indexer == nil {
		return nil, ErrPolicy
	}
	saved := cloneConfig(config)
	ark, err := services.Arkd.Info(ctx)
	if err != nil {
		return nil, err
	}
	emu, err := services.Emulator.Info(ctx)
	if err != nil {
		return nil, err
	}
	delegate, err := discoverDelegate(ctx, services.Delegator)
	if err != nil {
		return nil, err
	}
	discovered, err := admitServices(ark, emu, key, delegate)
	if err != nil {
		return nil, err
	}
	if saved.Network != "" && saved.Network != discovered.Network {
		return nil, ErrPolicy
	}
	if !reflect.DeepEqual(saved, Config{Network: saved.Network}) && !reflect.DeepEqual(saved, discovered) {
		return nil, ErrPolicy
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if services.Now == nil {
		services.Now = time.Now
	}
	return &Wallet{key: key, config: discovered, services: services}, nil
}

func cloneConfig(c Config) Config {
	c.CheckpointScript = bytes.Clone(c.CheckpointScript)
	c.Receive.Script = bytes.Clone(c.Receive.Script)
	c.Receive.Tapscripts = slices.Clone(c.Receive.Tapscripts)
	return c
}

func (w *Wallet) Config() (Config, error) {
	if w == nil || w.key == nil || w.key.secret == nil || w.key.secret.Key.IsZero() {
		return Config{}, ErrKey
	}
	return cloneConfig(w.config), nil
}

func compressedServiceKey(encoded string) (*btcec.PublicKey, error) {
	data, err := hex.DecodeString(encoded)
	if err != nil || len(data) != 33 || (data[0] != 2 && data[0] != 3) {
		return nil, ErrPolicy
	}
	key, err := btcec.ParsePubKey(data)
	if err != nil {
		return nil, ErrPolicy
	}
	return key, nil
}

func admitServices(ark ports.ArkInfo, emu ports.EmulatorInfo, key *Key, delegate *btcec.PublicKey) (Config, error) {
	c := Config{Network: clientlib.NetworkFromString(ark.Network).Name, WalletPublicKey: key.PublicKey()}
	server, err := compressedServiceKey(ark.Signer)
	if err != nil {
		return Config{}, err
	}
	forfeit, err := compressedServiceKey(ark.Forfeit)
	if err != nil {
		return Config{}, err
	}
	emulator, err := compressedServiceKey(emu.Signer)
	if err != nil {
		return Config{}, err
	}
	copy(c.ArkSigningKey[:], schnorr.SerializePubKey(server))
	copy(c.EmulatorSigningKey[:], schnorr.SerializePubKey(emulator))
	if c.ArkSigningKey == c.EmulatorSigningKey {
		return Config{}, ErrPolicy
	}
	if ark.ExitDelay <= 0 || ark.ExitDelay > math.MaxUint32 {
		return Config{}, ErrPolicy
	}
	delay, rounded := arklib.ParseRelativeLocktime(uint32(ark.ExitDelay))
	if rounded {
		return Config{}, ErrPolicy
	}
	c.Receive, err = key.Receive(server, delegate, delay, c.Network)
	if err != nil {
		return Config{}, ErrPolicy
	}
	if len(ark.CheckpointScript) == 0 || len(ark.CheckpointScript) > 2*txscript.MaxScriptSize {
		return Config{}, ErrPolicy
	}
	c.CheckpointScript, err = hex.DecodeString(ark.CheckpointScript)
	if err != nil {
		return Config{}, ErrPolicy
	}
	closure := new(script.CSVMultisigClosure)
	ok, err := closure.Decode(c.CheckpointScript)
	if err != nil || !ok || len(closure.PubKeys) != 1 || closure.Type != script.MultisigTypeChecksig || closure.Locktime.Value == 0 || !bytes.Equal(schnorr.SerializePubKey(closure.PubKeys[0]), schnorr.SerializePubKey(forfeit)) {
		return Config{}, ErrPolicy
	}
	// DecodeClosure masks BIP68 fields; rebuilding rejects reserved bits and
	// nonminimal script numbers instead of silently accepting their masked form.
	rebuilt, err := closure.Script()
	if err != nil || !bytes.Equal(rebuilt, c.CheckpointScript) {
		return Config{}, ErrPolicy
	}
	maximum := ark.MaxVtxo
	if maximum == -1 {
		maximum = btcutil.MaxSatoshi
	}
	if ark.Dust <= 0 || ark.Dust > btcutil.MaxSatoshi || ark.MinVtxo < 0 || ark.MinVtxo > btcutil.MaxSatoshi || maximum <= 0 || maximum > btcutil.MaxSatoshi {
		return Config{}, ErrPolicy
	}
	c.OutputPolicy = OutputPolicy{MinAmount: max(ark.Dust, ark.MinVtxo), MaxAmount: maximum}
	if c.OutputPolicy.MinAmount > maximum {
		return Config{}, ErrPolicy
	}
	return c, nil
}

// AdmitAgreement preserves the reference's smallest bond refund and largest
// timeout-payout checks for both players before any funding is selected.
func (w *Wallet) AdmitAgreement(p covenant.Params) error {
	if w == nil || p.ArkSigningKey != w.config.ArkSigningKey || p.EmulatorSigningKey != w.config.EmulatorSigningKey {
		return ErrPolicy
	}
	if p.Stake <= 0 || p.Bond <= 0 || p.MaxWager < 0 || p.Stake > btcutil.MaxSatoshi-p.Bond || p.MaxWager > btcutil.MaxSatoshi-p.Stake-p.Bond {
		return ErrPolicy
	}
	maximum := p.Stake + p.Bond + p.MaxWager
	if maximum > btcutil.MaxSatoshi/2 {
		return ErrPolicy
	}
	maximum *= 2
	local := false
	for _, player := range []covenant.Participant{p.Players.Player1, p.Players.Player2} {
		if !txscript.IsPayToTaproot(player.PayoutScript) || p.Bond < w.config.OutputPolicy.MinAmount || maximum > w.config.OutputPolicy.MaxAmount {
			return ErrPolicy
		}
		if player.SigningKey == w.config.WalletPublicKey {
			if !bytes.Equal(player.PayoutScript, w.config.Receive.Script) {
				return ErrPolicy
			}
			local = true
		}
	}
	if !local {
		return ErrPolicy
	}
	return nil
}
