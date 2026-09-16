// Package covenant owns poker agreement derivation, scripts and action assembly.
// Contract derivation, script compilation, codecs and source assembly are local
// operations. They establish neither accepted history nor service acceptance.
package covenant

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/wire"

	"arkade-poker/go/internal/shuffle"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	"github.com/btcsuite/btcd/btcutil/psbt"
)

type Player uint8

const (
	Player1 Player = iota + 1 // Creator; shuffles last and funds second.
	Player2                   // Joiner; shuffles first and funds first.
)

type PerPlayer[T any] struct {
	Player1 T
	Player2 T
}

type ContractID [32]byte
type UnixSeconds uint64

// DeadlineInterval is each live deadline increment, in seconds. The initial
// deadline is negotiated during setup and supplied in Params.
const DeadlineInterval = 60

type Street uint8

const (
	PreFlop Street = iota + 1
	Flop
	Turn
	River
)

type DealtCards[T any] struct {
	HoleCards PerPlayer[[2]T]
	Flop      [3]T
	Turn      T
	River     T
}

type Participant struct {
	SigningKey    [32]byte // BIP-340 x-only key, validated during derivation.
	EncryptionKey shuffle.PublicKey
	PayoutScript  []byte // Retain the caller's exact script.
}

// Params contains immutable agreement terms. Amounts are satoshis; derivation
// must check positivity, wager bounds and checked total value before assembly.
// The service checkpoint tapscript is supplied separately, outside the ID.
type Params struct {
	EmulatorSigningKey [32]byte
	ArkSigningKey      [32]byte
	Players            PerPlayer[Participant]
	Stake, Bond        int64
	MinBet, MaxWager   int64
	InitialDeadline    UnixSeconds
	EncryptedDeal      DealtCards[shuffle.MaskedCard]
}

// SpendingPath uses upstream PSBT leaf and TapTree representations. The emulator
// program must commit to the tweaked signing key in the outer spending leaf.
type SpendingPath struct {
	Identity           SpendingIdentity
	Leaf               *psbt.TaprootTapLeafScript
	Tree               txutils.TapTree
	Program            []byte
	TweakedEmulatorKey [32]byte
}

// Contract retains validated terms, the checkpoint script and derived tree.
// A zero value is unusable; Derive is the sole construction entry point.
type Contract struct {
	params           Params
	checkpointScript []byte
	id               ContractID
	script           []byte
	paths            []SpendingPath
	valid            bool
}

// Derive validates the agreement and compiles every poker spending path. The
// checkpoint script is caller-trusted service configuration, retained verbatim
// outside the agreement ID. Derivation never reads the clock or reserves future
// deadline increments. Key ownership and shuffle/deal provenance belong to the
// client; canonical crypto encodings alone establish neither.
func Derive(params Params, checkpointScript []byte) (*Contract, error) {
	if err := validateParams(params); err != nil {
		return nil, err
	}
	params.Players.Player1.PayoutScript = bytes.Clone(params.Players.Player1.PayoutScript)
	params.Players.Player2.PayoutScript = bytes.Clone(params.Players.Player2.PayoutScript)
	id, err := deriveID(params)
	if err != nil {
		return nil, err
	}
	output, paths, err := compileSpendingPaths(params, id)
	if err != nil {
		return nil, err
	}
	return &Contract{params: params, checkpointScript: bytes.Clone(checkpointScript), id: id, script: output, paths: paths, valid: true}, nil
}

var ErrInvalidContract = errors.New("covenant: contract was not derived")

func (c *Contract) check() error {
	if c == nil || !c.valid {
		return ErrInvalidContract
	}
	return nil
}
func (c *Contract) ID() (ContractID, error) {
	if err := c.check(); err != nil {
		return ContractID{}, err
	}
	return c.id, nil
}
func (c *Contract) ScriptPubKey() ([]byte, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	return bytes.Clone(c.script), nil
}
func clonePath(path SpendingPath) SpendingPath {
	path.Leaf = cloneLeaf(path.Leaf)
	path.Tree = append(txutils.TapTree(nil), path.Tree...)
	path.Program = bytes.Clone(path.Program)
	return path
}
func (c *Contract) SpendingPaths() ([]SpendingPath, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	paths := make([]SpendingPath, len(c.paths))
	for i, p := range c.paths {
		paths[i] = clonePath(p)
	}
	return paths, nil
}
func validateParams(params Params) error {
	if err := validateAmounts(params); err != nil {
		return err
	}
	for _, key := range [][32]byte{params.EmulatorSigningKey, params.ArkSigningKey, params.Players.Player1.SigningKey, params.Players.Player2.SigningKey} {
		if _, err := schnorr.ParsePubKey(key[:]); err != nil {
			return fmt.Errorf("covenant: invalid signing key: %w", err)
		}
	}
	var keys [2]btcec.JacobianPoint
	for i, key := range []shuffle.PublicKey{params.Players.Player1.EncryptionKey, params.Players.Player2.EncryptionKey} {
		data, err := key.MarshalBinary()
		if err != nil {
			return fmt.Errorf("covenant: encryption key %d: %w", i+1, err)
		}
		pk, err := btcec.ParsePubKey(data)
		if err != nil {
			return err
		}
		pk.AsJacobian(&keys[i])
	}
	var sum btcec.JacobianPoint
	btcec.AddNonConst(&keys[0], &keys[1], &sum)
	if sum.Z.IsZero() {
		return fmt.Errorf("covenant: encryption keys cancel to infinity")
	}
	for i, card := range dealtOrder(params.EncryptedDeal) {
		if _, err := card.AffineBytes(); err != nil {
			return fmt.Errorf("covenant: encrypted card %d: %w", i, err)
		}
	}
	return nil
}

// Agreement commitment v1 is SHA256("arkade-poker/contract\x00" || u16le(1)
// || emulator xonly || Ark xonly || participant1 || participant2 || stake_u64le
// || bond_u64le || min_bet_u64le || max_wager_u64le || deadline_u64le || deal).
// Each participant is signing_xonly32 || encryption_affine64 || CompactSize
// payout_length || exact payout_bytes. Deal is nine affine128 ciphertexts in
// dealtOrder. All fields, including the initial deadline, are committed exactly;
// output key, derived programs and checkpoint configuration are excluded.
func deriveID(params Params) (ContractID, error) {
	h := sha256.New()
	h.Write([]byte("arkade-poker/contract\x00"))
	h.Write([]byte{1, 0})
	h.Write(params.EmulatorSigningKey[:])
	h.Write(params.ArkSigningKey[:])
	for _, p := range []Participant{params.Players.Player1, params.Players.Player2} {
		h.Write(p.SigningKey[:])
		affine, err := p.EncryptionKey.AffineBytes()
		if err != nil {
			return ContractID{}, err
		}
		h.Write(affine[:])
		if err := wire.WriteVarBytes(h, 0, p.PayoutScript); err != nil {
			return ContractID{}, err
		}
	}
	for _, n := range []uint64{uint64(params.Stake), uint64(params.Bond), uint64(params.MinBet), uint64(params.MaxWager), uint64(params.InitialDeadline)} {
		if err := binary.Write(h, binary.LittleEndian, n); err != nil {
			return ContractID{}, err
		}
	}
	for _, card := range dealtOrder(params.EncryptedDeal) {
		affine, err := card.AffineBytes()
		if err != nil {
			return ContractID{}, err
		}
		h.Write(affine[:])
	}
	return ContractID(h.Sum(nil)), nil
}
