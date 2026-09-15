package wallet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
)

type InsufficientFunds struct{ Required, Available int64 }

func (e InsufficientFunds) Error() string {
	return fmt.Sprintf("wallet: need %d sats, available %d", e.Required, e.Available)
}

// SelectFunding follows the reference's exact-single-coin, then descending-value
// greedy selection with outpoint tie-breaking. It never burns small change or
// reserves coins, and it is never invoked by exact saved-spend retries.
func (w *Wallet) SelectFunding(ctx context.Context, amount int64) (covenant.Funding, error) {
	if err := ctx.Err(); err != nil {
		return covenant.Funding{}, err
	}
	if amount < 0 || amount > btcutil.MaxSatoshi {
		return covenant.Funding{}, ErrPolicy
	}
	if amount == 0 {
		return covenant.Funding{}, nil
	}
	if w == nil || w.services.Indexer == nil || w.services.Now == nil {
		return covenant.Funding{}, ErrPolicy
	}
	records, err := w.services.Indexer.Vtxos(ctx, ports.VtxoQuery{Script: bytes.Clone(w.config.Receive.Script)})
	if err != nil {
		return covenant.Funding{}, err
	}
	now := w.services.Now().Unix()
	if now < 0 {
		return covenant.Funding{}, ErrPolicy
	}
	if len(records) > 10000 {
		return covenant.Funding{}, errors.New("wallet: indexed record limit")
	}
	tree, leaf, err := w.fundingTree()
	if err != nil {
		return covenant.Funding{}, ErrPolicy
	}
	_, proofs, err := tree.TapTree()
	if err != nil {
		return covenant.Funding{}, ErrPolicy
	}
	proof, err := proofs.GetTaprootMerkleProof(txscript.NewBaseTapLeaf(leaf).TapHash())
	if err != nil {
		return covenant.Funding{}, ErrPolicy
	}
	seen := make(map[wire.OutPoint]bool)
	transactions := make(map[chainhash.Hash]*wire.MsgTx)
	var candidates []covenant.Source
	var available int64
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return covenant.Funding{}, err
		}
		if !bytes.Equal(record.Script, w.config.Receive.Script) || seen[record.Outpoint] || record.ExpiresAt <= 0 {
			return covenant.Funding{}, ErrPolicy
		}
		seen[record.Outpoint] = true
		if record.ExpiresAt <= now || record.Spent || record.Swept || record.Unrolled || record.SpentBy != nil || record.ArkTxID != nil || record.SettledBy != nil {
			continue
		}
		if record.Amount < 0 || record.Amount > btcutil.MaxSatoshi {
			return covenant.Funding{}, ErrPolicy
		}
		if record.Amount == 0 || len(record.Assets) > 0 {
			continue
		}
		previous := transactions[record.Outpoint.Hash]
		if previous == nil {
			previous, err = w.services.Indexer.Transaction(ctx, record.Outpoint.Hash)
			if err != nil {
				return covenant.Funding{}, err
			}
			if previous == nil {
				return covenant.Funding{}, errors.New("wallet: indexed source transaction missing")
			}
			transactions[record.Outpoint.Hash] = previous
		}
		if err := checkOutput(record, previous); err != nil {
			return covenant.Funding{}, err
		}
		if record.Amount > btcutil.MaxSatoshi-available {
			return covenant.Funding{}, ErrPolicy
		}
		available += record.Amount
		control, err := txscript.ParseControlBlock(bytes.Clone(proof.ControlBlock))
		if err != nil {
			return covenant.Funding{}, ErrPolicy
		}
		point := record.Outpoint
		candidates = append(candidates, covenant.Source{PreviousTx: previous.Copy(), Vtxo: offchain.VtxoInput{Outpoint: &point, Amount: record.Amount, RevealedTapscripts: slices.Clone(w.config.Receive.Tapscripts), Tapscript: &waddrmgr.Tapscript{ControlBlock: control, RevealedScript: bytes.Clone(proof.Script)}}})
	}
	if available < amount {
		return covenant.Funding{}, InsufficientFunds{amount, available}
	}
	slices.SortFunc(candidates, func(a, b covenant.Source) int {
		av, bv := a.Vtxo.Amount, b.Vtxo.Amount
		if (av == amount) != (bv == amount) {
			if av == amount {
				return -1
			}
			return 1
		}
		if av > bv {
			return -1
		}
		if av < bv {
			return 1
		}
		if n := bytes.Compare(a.Vtxo.Outpoint.Hash[:], b.Vtxo.Outpoint.Hash[:]); n != 0 {
			return n
		}
		if a.Vtxo.Outpoint.Index < b.Vtxo.Outpoint.Index {
			return -1
		}
		if a.Vtxo.Outpoint.Index > b.Vtxo.Outpoint.Index {
			return 1
		}
		return 0
	})
	var funding covenant.Funding
	var selected int64
	for _, candidate := range candidates {
		total := selected + candidate.Vtxo.Amount
		if total-amount > w.config.OutputPolicy.MaxAmount {
			continue
		}
		selected = total
		funding.Inputs = append(funding.Inputs, candidate)
		if len(funding.Inputs) > 255 {
			return covenant.Funding{}, errors.New("wallet: funding input limit")
		}
		if selected < amount {
			continue
		}
		change := selected - amount
		if change == 0 {
			return funding, nil
		}
		if change >= w.config.OutputPolicy.MinAmount {
			funding.Change = wire.NewTxOut(change, bytes.Clone(w.config.Receive.Script))
			return funding, nil
		}
	}
	return covenant.Funding{}, errors.New("wallet: funding cannot make service-admissible change")
}

// Select the owner + Arkd path explicitly. Delegated receive trees also have
// an owner + delegate + Arkd path; poker funding never needs a delegate signature.
func (w *Wallet) fundingTree() (script.VtxoScript, []byte, error) {
	tree, err := script.ParseVtxoScript(w.config.Receive.Tapscripts)
	if err != nil {
		return nil, nil, ErrPolicy
	}
	server, err := schnorr.ParsePubKey(w.config.ArkSigningKey[:])
	if err != nil {
		return nil, nil, ErrPolicy
	}
	owner, err := schnorr.ParsePubKey(w.config.WalletPublicKey[:])
	if err != nil {
		return nil, nil, ErrPolicy
	}
	leaf, err := (&script.MultisigClosure{PubKeys: []*btcec.PublicKey{owner, server}}).Script()
	if err != nil {
		return nil, nil, err
	}
	matches := 0
	for _, closure := range tree.ForfeitClosures() {
		candidate, err := closure.Script()
		if err != nil {
			return nil, nil, err
		}
		if bytes.Equal(candidate, leaf) {
			matches++
		}
	}
	if matches != 1 {
		return nil, nil, ErrPolicy
	}
	return tree, leaf, nil
}
