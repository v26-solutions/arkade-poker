package wallet

import (
	"errors"
	"time"

	"arkade-poker/go/internal/ports"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	clientlib "github.com/arkade-os/arkd/pkg/client-lib"
	"github.com/btcsuite/btcd/btcec/v2"
)

type OutputPolicy struct {
	MinAmount, MaxAmount int64
}

// Config contains admitted public service/wallet metadata only. Poker uses the
// value-conserving offchain route; RegisterIntent fee settings do not apply.
type Config struct {
	Network            string
	WalletPublicKey    [32]byte
	ArkSigningKey      [32]byte
	EmulatorSigningKey [32]byte
	CheckpointScript   []byte
	Receive            Receive
	OutputPolicy       OutputPolicy
}

type Services struct {
	Arkd      ports.Arkd
	Emulator  ports.Emulator
	Delegator ports.Delegator // nil only for nondelegated configurations.
	Indexer   ports.Indexer
	Now       func() time.Time // nil selects the host clock; used after funding queries.
}

// Wallet holds the already imported key in memory. It has no serialization API.
// The caller retains key lifetime ownership and allows only one active game.
type Wallet struct {
	key      *Key
	config   Config
	services Services
}

// Receive is public wallet metadata. It is safe to save with a session.
type Receive struct {
	Address    string
	Script     []byte
	Tapscripts []string
}

func (k *Key) Receive(server, delegate *btcec.PublicKey, delay arklib.RelativeLocktime, network string) (Receive, error) {
	if k == nil || k.secret == nil || k.secret.Key.IsZero() {
		return Receive{}, ErrKey
	}
	if k.network != "" && k.network != network {
		return Receive{}, ErrKeyNetwork
	}
	return receiveForPublicKey(k.secret.PubKey(), server, delegate, delay, network)
}

func receiveForPublicKey(owner, server, delegate *btcec.PublicKey, delay arklib.RelativeLocktime, network string) (Receive, error) {
	if owner == nil || server == nil || delay.Value == 0 {
		return Receive{}, errors.New("invalid receive configuration")
	}
	if delay.Type != arklib.LocktimeTypeBlock && delay.Type != arklib.LocktimeTypeSecond {
		return Receive{}, errors.New("invalid exit delay type")
	}
	if delay.Type == arklib.LocktimeTypeBlock && delay.Value > 65535 {
		return Receive{}, errors.New("exit delay exceeds BIP68 bounds")
	}
	if _, err := arklib.BIP68Sequence(delay); err != nil {
		return Receive{}, err
	}
	net := clientlib.NetworkFromString(network)
	tree := script.NewDefaultVtxoScript(owner, server, delay)
	if delegate != nil {
		tree.Closures = append(tree.Closures, &script.MultisigClosure{
			PubKeys: []*btcec.PublicKey{owner, delegate, server},
		})
	}
	output, _, err := tree.TapTree()
	if err != nil {
		return Receive{}, err
	}
	address := arklib.Address{Version: 0, HRP: net.Addr, Signer: server, VtxoTapKey: output}
	encoded, err := address.EncodeV0()
	if err != nil {
		return Receive{}, err
	}
	pkScript, err := address.GetPkScript()
	if err != nil {
		return Receive{}, err
	}
	leaves, err := tree.Encode()
	return Receive{Address: encoded, Script: pkScript, Tapscripts: leaves}, err
}
