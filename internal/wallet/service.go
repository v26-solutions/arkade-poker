package wallet

import (
	"context"
	"encoding/hex"
	"errors"
	"math"

	"arkade-poker/go/internal/ports"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/btcsuite/btcd/btcec/v2"
)

// DiscoverReceive uses actual service metadata to derive the default Ark payout
// address. Full game admission also needs emulator/checkpoint/output-policy
// validation; deriving a receive address alone does not admit a funded game.
func DiscoverReceive(ctx context.Context, ark ports.Arkd, delegator ports.Delegator, key *Key) (Receive, error) {
	info, err := ark.Info(ctx)
	if err != nil {
		return Receive{}, err
	}
	bytes, err := hex.DecodeString(info.Signer)
	if err != nil || len(bytes) != 33 {
		return Receive{}, errors.New("invalid Ark signing key")
	}
	server, err := btcec.ParsePubKey(bytes)
	if err != nil {
		return Receive{}, errors.New("invalid Ark signing key")
	}
	if info.ExitDelay <= 0 || info.ExitDelay > math.MaxUint32 {
		return Receive{}, errors.New("invalid Ark exit delay")
	}
	delay, rounded := arklib.ParseRelativeLocktime(uint32(info.ExitDelay))
	if rounded {
		return Receive{}, errors.New("noncanonical Ark exit delay")
	}
	delegate, err := discoverDelegate(ctx, delegator)
	if err != nil {
		return Receive{}, err
	}
	return key.Receive(server, delegate, delay, info.Network)
}

func discoverDelegate(ctx context.Context, delegator ports.Delegator) (*btcec.PublicKey, error) {
	if delegator == nil {
		return nil, nil
	}
	info, err := delegator.Info(ctx)
	if err != nil {
		return nil, err
	}
	return compressedServiceKey(info.PubKey)
}
