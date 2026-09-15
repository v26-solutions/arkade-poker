// Replenish the external regtest faucet using a credit note. No wallet keys
// enter this process; upstream handles note proofs and batch tree signing.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/intent"
	"github.com/arkade-os/arkd/pkg/ark-lib/note"
	"github.com/arkade-os/arkd/pkg/ark-lib/tree"
	clientlib "github.com/arkade-os/arkd/pkg/client-lib"
	batchsession "github.com/arkade-os/arkd/pkg/client-lib/batch-session"
	"github.com/arkade-os/arkd/pkg/client-lib/client"
	"github.com/btcsuite/btcd/btcutil/psbt"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var request struct {
		Port    uint16
		Address string
		Note    string
	}
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 4096)).Decode(&request); err != nil {
		return fmt.Errorf("invalid refill request")
	}
	address, err := arklib.DecodeAddressV0(request.Address)
	if err != nil || address.HRP != "tark" || request.Port == 0 {
		return fmt.Errorf("refill requires a regtest address and local server port")
	}
	n, err := note.NewNoteFromString(request.Note)
	if err != nil || n.Value == 0 || n.Value > 1000000 {
		return fmt.Errorf("refill requires a note of at most 1,000,000 test satoshis")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	serverURL := fmt.Sprintf("http://127.0.0.1:%d", request.Port)
	c, err := client.NewClient(serverURL, "poker-regtest-faucet")
	if err != nil {
		return err
	}
	defer c.Close()
	info, err := c.GetInfo(ctx)
	if err != nil {
		return err
	}
	if info.Network != "regtest" {
		return fmt.Errorf("refill requires the local regtest server")
	}
	params, err := info.ServerParams(serverURL, "")
	if err != nil {
		return err
	}
	signer, err := tree.NewVtxoTreeSigner()
	if err != nil {
		return err
	}
	base := batchsession.BaseArgs{
		Notes:   []string{request.Note},
		Outputs: []clientlib.Receiver{{To: request.Address, Amount: uint64(n.Value)}},
		SignTx: func(context.Context, string) (string, error) {
			return "", fmt.Errorf("note refill must not request wallet signatures")
		},
	}
	// Requote after subtracting the fee, since output amounts may affect it.
	// Register only once the server quote and the exact output value agree.
	for attempt := 0; attempt < 8; attempt++ {
		proof, message, _, err := batchsession.BuildAndSignRegisterIntent(ctx, batchsession.IntentArgs{
			BaseArgs: base, Cosigners: []string{signer.GetPublicKey()},
		})
		if err != nil {
			return err
		}
		estimateProof, estimateMessage, err := feeProof(proof, message)
		if err != nil {
			return err
		}
		fee, err := c.EstimateIntentFee(ctx, estimateProof, estimateMessage)
		if err != nil {
			return fmt.Errorf("quote refill fee: %w", err)
		}
		if fee < 0 || fee >= int64(n.Value) {
			return fmt.Errorf("refill fee %d exceeds available test funds", fee)
		}
		amount := uint64(n.Value) - uint64(fee)
		if amount != base.Outputs[0].Amount {
			base.Outputs[0].Amount = amount
			continue
		}
		id, err := c.RegisterIntent(ctx, proof, message)
		if err != nil {
			return fmt.Errorf("register refill: %w", err)
		}
		result, err := batchsession.JoinBatch(ctx, batchsession.JoinBatchArgs{
			BaseArgs: base, Client: c, ServerParams: *params,
			TreeSigners: []tree.SignerSession{signer}, IntentId: id,
		})
		if err != nil {
			return fmt.Errorf("join refill batch (intent %s): %w", id, err)
		}
		fmt.Printf("Faucet replenished: %d test sats; fee: %d; commitment: %s\n", amount, fee, result.CommitmentTxid)
		return nil
	}
	return fmt.Errorf("refill fee did not converge")
}

// Use the upstream proof constructor to bind the same note input and outputs
// to an estimate message. Notes use a hash preimage, so no re-signing is needed.
func feeProof(proof, message string) (string, string, error) {
	var register intent.RegisterMessage
	if err := register.Decode(message); err != nil {
		return "", "", err
	}
	register.Type = intent.IntentMessageTypeEstimateFee
	estimate, err := intent.EstimateIntentFeeMessage(register).Encode()
	if err != nil {
		return "", "", err
	}
	packet, err := psbt.NewFromRawBytes(strings.NewReader(proof), true)
	if err != nil {
		return "", "", err
	}
	if len(packet.Inputs) != 2 {
		return "", "", fmt.Errorf("expected exactly one note input")
	}
	input := packet.UnsignedTx.TxIn[1]
	quoted, err := intent.New(estimate, []intent.Input{{
		OutPoint: &input.PreviousOutPoint, Sequence: input.Sequence,
		WitnessUtxo: packet.Inputs[1].WitnessUtxo,
	}}, packet.UnsignedTx.TxOut)
	if err != nil {
		return "", "", err
	}
	for i := range quoted.Inputs {
		quoted.Inputs[i].TaprootLeafScript = packet.Inputs[i].TaprootLeafScript
		quoted.Inputs[i].Unknowns = packet.Inputs[i].Unknowns
	}
	encoded, err := quoted.B64Encode()
	return encoded, estimate, err
}
