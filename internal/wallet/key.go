// Package wallet owns imported wallet keys and the wallet's spending policy.
package wallet

import (
	"encoding/hex"
	"errors"
	"strings"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	clientlib "github.com/arkade-os/arkd/pkg/client-lib"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/tyler-smith/go-bip39"
)

var (
	ErrKey        = errors.New("invalid wallet key: enter nsec, 64 hexadecimal digits or a BIP39 mnemonic")
	ErrKeyNetwork = errors.New("mnemonic import requires a known Arkd network")
)

// Key deliberately has no exported secret fields or serialization method. The
// imported key must never be part of configuration, session data or diagnostics.
// Session shuffle and transport secrets are generated independently.
type Key struct {
	secret  *btcec.PrivateKey
	network string // Mnemonic derivation network; raw keys are network-independent.
}

func (*Key) String() string               { return "[wallet key]" }
func (*Key) GoString() string             { return "[wallet key]" }
func (*Key) MarshalJSON() ([]byte, error) { return nil, errors.New("wallet keys cannot be serialized") }

// ParseKey uses the same ingress policy for environment and modal input. Decode
// NIP-19 first, then raw hex, then BIP39 on decoding failures. A valid mnemonic
// requires the network reported by Arkd; raw keys do not. SetByteSlice's
// overflow flag is checked BEFORE constructing a key: reducing modulo N would
// turn invalid input into another person's wallet.
func ParseKey(text, network string) (*Key, error) {
	data, err := decodeNsec(text)
	if err != nil {
		data, err = hex.DecodeString(text)
		if err != nil {
			clear(data) // DecodeString may return a partially decoded secret.
			return parseMnemonic(text, network)
		}
	}
	defer clear(data)
	if len(data) != 32 {
		return nil, ErrKey
	}
	var scalar btcec.ModNScalar
	defer scalar.Zero()
	if scalar.SetByteSlice(data) || scalar.IsZero() {
		return nil, ErrKey
	}
	return &Key{secret: btcec.PrivKeyFromScalar(&scalar)}, nil
}

func parseMnemonic(text, network string) (*Key, error) {
	// Normalize word separators before both validation and seed derivation.
	// Never normalize raw keys into accepting surrounding whitespace.
	phrase := strings.Join(strings.Fields(text), " ")
	seed, err := bip39.NewSeedWithErrorChecking(phrase, "")
	if err != nil {
		return nil, ErrKey // Upstream errors may contain words from the input.
	}
	defer clear(seed)
	net := clientlib.NetworkFromString(network)
	if net.Name != network {
		return nil, ErrKeyNetwork // Do not derive mainnet keys via an unknown-name fallback.
	}
	coinType := uint32(1)
	params := &chaincfg.TestNet3Params
	if net.Name == arklib.Bitcoin.Name {
		coinType = 0
		params = &chaincfg.MainNetParams
	}
	key, err := hdkeychain.NewMaster(seed, params)
	if err != nil {
		return nil, ErrKey
	}
	defer func() { key.Zero() }()
	// First BIP86 receiving key, matching Arkade's default mnemonic identity.
	for _, index := range []uint32{86 + hdkeychain.HardenedKeyStart, coinType + hdkeychain.HardenedKeyStart, hdkeychain.HardenedKeyStart, 0, 0} {
		child, err := key.Derive(index)
		if err != nil {
			return nil, ErrKey
		}
		key.Zero()
		key = child
	}
	secret, err := key.ECPrivKey()
	if err != nil {
		return nil, ErrKey
	}
	return &Key{secret: secret, network: network}, nil
}

func decodeNsec(text string) ([]byte, error) {
	if len(text) != 63 {
		return nil, ErrKey
	}
	hrp, words, version, err := bech32.DecodeGeneric(text)
	defer clear(words)
	if err != nil || hrp != "nsec" || version != bech32.Version0 {
		return nil, ErrKey
	}
	data, err := bech32.ConvertBits(words, 5, 8, false)
	if err != nil || len(data) != 32 {
		clear(data)
		return nil, ErrKey
	}
	return data, nil
}

func (k *Key) PublicKey() [32]byte {
	var out [32]byte
	copy(out[:], schnorr.SerializePubKey(k.secret.PubKey()))
	return out
}

// Destroy is called only after the key's owner has stopped all signing work.
func (k *Key) Destroy() {
	if k != nil && k.secret != nil {
		k.secret.Zero()
	}
}
