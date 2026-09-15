package wallet

import (
	"bytes"
	"context"
	"errors"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
)

// Prepare serializes the already admitted upstream PSBTs for persistence. Poker
// layout, sources, output policy and timing are covenant/driver responsibilities.
func (w *Wallet) Prepare(unsigned *covenant.Unsigned) (ports.Bundle, error) {
	if unsigned == nil || unsigned.Ark == nil || unsigned.Ark.UnsignedTx == nil || len(unsigned.Checkpoints) != len(unsigned.Ark.Inputs) {
		return ports.Bundle{}, errors.New("wallet: prepared bundle shape")
	}
	var result ports.Bundle
	var err error
	result.Ark, err = unsigned.Ark.B64Encode()
	if err != nil {
		return ports.Bundle{}, err
	}
	for _, cp := range unsigned.Checkpoints {
		if cp == nil || cp.UnsignedTx == nil {
			return ports.Bundle{}, errors.New("wallet: missing checkpoint")
		}
		encoded, err := cp.B64Encode()
		if err != nil {
			return ports.Bundle{}, err
		}
		result.Checkpoints = append(result.Checkpoints, encoded)
	}
	if _, _, err := BundleTransactions(result); err != nil {
		return ports.Bundle{}, err
	}
	return result, nil
}

// Sign signs the exact prepared bundle using upstream signing/PSBT primitives.
// Its result must be durably saved before external submission.
func (w *Wallet) Sign(ctx context.Context, prepared ports.Bundle) (ports.Bundle, error) {
	if err := ctx.Err(); err != nil {
		return ports.Bundle{}, err
	}
	if _, err := w.Config(); err != nil {
		return ports.Bundle{}, err
	}
	if _, _, err := BundleTransactions(prepared); err != nil {
		return ports.Bundle{}, err
	}
	all := append([]string{prepared.Ark}, prepared.Checkpoints...)
	for i, encoded := range all {
		if err := ctx.Err(); err != nil {
			return ports.Bundle{}, err
		}
		p, maps, err := packetMaps(encoded)
		if err != nil {
			return ports.Bundle{}, err
		}
		prevouts := txscript.NewMultiPrevOutFetcher(nil)
		for j, input := range p.Inputs {
			if input.WitnessUtxo == nil || len(input.TaprootLeafScript) != 1 || input.SighashType != txscript.SigHashDefault {
				return ports.Bundle{}, errors.New("wallet: signature requires prevout, one leaf and default sighash")
			}
			point := p.UnsignedTx.TxIn[j].PreviousOutPoint
			if prevouts.FetchPrevOutput(point) != nil {
				return ports.Bundle{}, errors.New("wallet: duplicate signing input")
			}
			prevouts.AddPrevOut(point, input.WitnessUtxo)
		}
		hashes := txscript.NewTxSigHashes(p.UnsignedTx, prevouts)
		public := w.key.PublicKey()
		for j, input := range p.Inputs {
			if err := ctx.Err(); err != nil {
				return ports.Bundle{}, err
			}
			selected := input.TaprootLeafScript[0]
			leaf := txscript.NewTapLeaf(selected.LeafVersion, selected.Script)
			digest, err := txscript.CalcTapscriptSignaturehash(hashes, txscript.SigHashDefault, p.UnsignedTx, j, prevouts, leaf)
			if err != nil {
				return ports.Bundle{}, err
			}
			sig, err := schnorr.Sign(w.key.secret, digest)
			if err != nil {
				return ports.Bundle{}, err
			}
			leafHash := leaf.TapHash()
			key := append([]byte{byte(psbt.TaprootScriptSpendSignatureType)}, public[:]...)
			key = append(key, leafHash[:]...)
			fields := maps[j+1]
			found := false
			for n := range fields {
				if bytes.Equal(fields[n].key, key) {
					fields[n].value = sig.Serialize()
					found = true
				}
			}
			if !found {
				fields = append(fields, psbtField{key: key, value: sig.Serialize()})
			}
			maps[j+1] = fields
		}
		all[i] = encodeMaps(maps)
	}
	result := ports.Bundle{Ark: all[0], Checkpoints: all[1:]}
	if err := ctx.Err(); err != nil {
		return ports.Bundle{}, err
	}
	if err := CompareBundles(prepared, result); err != nil {
		return ports.Bundle{}, err
	}
	return result, nil
}
