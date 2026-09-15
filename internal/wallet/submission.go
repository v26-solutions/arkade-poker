package wallet

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"arkade-poker/go/internal/ports"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
)

type Route uint8

const (
	ArkdRoute     Route = iota + 1 // Initial ordinary-wallet funding.
	EmulatorRoute                  // A spend containing a poker covenant input.
)

// Submit obtains emulator signatures for covenant inputs, then submits and
// finalizes with Arkd directly. Covenant leaves require the emulator before the
// participant so the emulator endpoint returns signatures without finalizing.
// Every response is merged against the exact saved work. Signature checks stay
// in upstream libraries or the browser Arkd/emulator boundary.
// A service response still needs accepted indexer evidence before game applies
// a spend. Errors propagate; this method does not automatically rebuild or retry.
func (w *Wallet) Submit(ctx context.Context, route Route, signed ports.Bundle) (ports.Submitted, error) {
	if err := ctx.Err(); err != nil {
		return ports.Submitted{}, err
	}
	if _, err := w.Config(); err != nil {
		return ports.Submitted{}, err
	}
	main, _, err := BundleTransactions(signed)
	if err != nil {
		return ports.Submitted{}, err
	}
	p, _, err := unsignedPSBT(signed.Ark)
	if err != nil {
		return ports.Submitted{}, err
	}
	if len(p.Inputs[0].TaprootLeafScript) != 1 {
		return ports.Submitted{}, errors.New("wallet: submission requires selected leaf")
	}
	_, ordinary, err := w.fundingTree()
	if err != nil {
		return ports.Submitted{}, err
	}
	selected := p.Inputs[0].TaprootLeafScript[0]
	expected := EmulatorRoute
	if selected.LeafVersion == txscript.BaseLeafVersion && bytes.Equal(selected.Script, ordinary) {
		expected = ArkdRoute
	}
	if route != expected {
		return ports.Submitted{}, errors.New("wallet: saved route does not match selected leaf")
	}
	if route == EmulatorRoute && !clientFinalizationLeaf(selected.Script, w.config.WalletPublicKey, w.config.ArkSigningKey) {
		return ports.Submitted{}, errors.New("wallet: covenant leaf requires client finalization")
	}
	id := main.TxHash().String()
	merged := signed
	if route == EmulatorRoute {
		// Each adapter owns its slice; merging always uses untouched saved maps.
		request := ports.Bundle{Ark: signed.Ark, Checkpoints: append([]string(nil), signed.Checkpoints...)}
		remote, err := w.services.Emulator.Sign(ctx, request)
		if err != nil {
			return ports.Submitted{}, fmt.Errorf("wallet: emulator sign: %w", err)
		}
		merged, err = mergeBundles(signed, remote, false)
		if err != nil {
			return ports.Submitted{}, fmt.Errorf("wallet: emulator response: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return ports.Submitted{}, err
	}
	request := ports.Bundle{Ark: merged.Ark, Checkpoints: append([]string(nil), merged.Checkpoints...)}
	response, err := w.services.Arkd.Submit(ctx, request)
	if err != nil {
		return ports.Submitted{}, fmt.Errorf("wallet: Arkd submit: %w", err)
	}
	if response.TxID != id {
		return ports.Submitted{}, errors.New("wallet: Arkd submit: service transaction identity mismatch")
	}
	merged, err = mergeBundles(merged, response.Bundle, true)
	if err != nil {
		return ports.Submitted{}, fmt.Errorf("wallet: Arkd response: %w", err)
	}
	if err := verifyServiceSignatures(signed, merged, w.config.ArkSigningKey); err != nil {
		return ports.Submitted{}, fmt.Errorf("wallet: verify Arkd signatures: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return ports.Submitted{}, err
	}
	if err := w.services.Arkd.Finalize(ctx, id, append([]string(nil), merged.Checkpoints...)); err != nil {
		return ports.Submitted{}, fmt.Errorf("wallet: Arkd finalize: %w", err)
	}
	return ports.Submitted{TxID: id, Bundle: merged}, nil
}

// Routing follows the committed canonical three-signer leaf, never a mutable
// runtime option. The participant follows the emulator and precedes Arkd.
func clientFinalizationLeaf(leaf []byte, participant, server [32]byte) bool {
	closure, err := script.DecodeClosure(leaf)
	if err != nil {
		return false
	}
	multi, ok := closure.(*script.MultisigClosure)
	return ok && len(multi.PubKeys) == 3 &&
		bytes.Equal(schnorr.SerializePubKey(multi.PubKeys[1]), participant[:]) &&
		bytes.Equal(schnorr.SerializePubKey(multi.PubKeys[2]), server[:])
}

// RetrySaved submits only exact durable work once. The driver must first observe
// the source explicitly unspent; an unavailable query never authorizes a retry.
func (w *Wallet) RetrySaved(ctx context.Context, route Route, signed ports.Bundle) (ports.Submitted, error) {
	return w.Submit(ctx, route, signed)
}
