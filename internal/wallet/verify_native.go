//go:build !js

package wallet

import (
	"encoding/hex"

	"arkade-poker/go/internal/ports"
	offchaintx "github.com/arkade-os/arkd/pkg/client-lib/offchain-tx"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// The pinned client library verifies the Arkd signatures on every returned
// input. Arkd/emulator enforce participant and covenant signatures remotely.
func verifyServiceSignatures(original, signed ports.Bundle, server [32]byte) error {
	key, err := schnorr.ParsePubKey(server[:])
	if err != nil {
		return err
	}
	signers := map[string]*btcec.PublicKey{hex.EncodeToString(server[:]): key}
	if err := offchaintx.VerifySignedTx(original.Ark, signed.Ark, signers); err != nil {
		return err
	}
	return offchaintx.VerifySignedCheckpointTxs(original.Checkpoints, signed.Checkpoints, signers)
}
