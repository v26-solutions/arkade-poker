package covenant

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
)

const maxMoney int64 = 21_000_000 * 100_000_000

func addAmount(total, amount int64) (int64, error) {
	if amount < 0 || amount > maxMoney {
		return 0, fmt.Errorf("covenant: amount %d outside [0, MAX_MONEY]", amount)
	}
	if total < 0 || total > maxMoney-amount {
		return 0, fmt.Errorf("covenant: amount sum exceeds MAX_MONEY")
	}
	return total + amount, nil
}

func validateFunding(f Funding, required int64) error {
	if _, err := addAmount(0, required); err != nil {
		return err
	}
	if required == 0 {
		if len(f.Inputs) != 0 || f.Change != nil {
			return fmt.Errorf("covenant: zero contribution permits no funding inputs or change")
		}
		return nil
	}
	if len(f.Inputs) == 0 {
		return fmt.Errorf("covenant: contribution requires funding inputs")
	}
	var total int64
	for i, s := range f.Inputs {
		out, err := sourceOutput(s)
		if err != nil {
			return fmt.Errorf("covenant: funding input %d: %w", i, err)
		}
		total, err = addAmount(total, out.Value)
		if err != nil {
			return fmt.Errorf("covenant: funding input %d: %w", i, err)
		}
	}
	if total < required {
		return fmt.Errorf("covenant: funding requires %d sats, available %d", required, total)
	}
	remainder := total - required
	if remainder == 0 && f.Change == nil {
		return nil
	}
	if remainder > 0 && f.Change != nil && f.Change.Value == remainder {
		return nil
	}
	return fmt.Errorf("covenant: change must be exactly %d sats, omitted when zero", remainder)
}

func sourceOutput(s Source) (*wire.TxOut, error) {
	if s.Vtxo.Outpoint == nil || s.PreviousTx == nil {
		return nil, fmt.Errorf("missing source outpoint or transaction")
	}
	// wire's hashing/copy methods assume structurally non-nil inputs/outputs.
	for _, in := range s.PreviousTx.TxIn {
		if in == nil {
			return nil, fmt.Errorf("nil source transaction input")
		}
	}
	for _, out := range s.PreviousTx.TxOut {
		if out == nil {
			return nil, fmt.Errorf("nil source transaction output")
		}
	}
	if s.PreviousTx.TxHash() != s.Vtxo.Outpoint.Hash {
		return nil, fmt.Errorf("source transaction id mismatch")
	}
	if uint64(s.Vtxo.Outpoint.Index) >= uint64(len(s.PreviousTx.TxOut)) {
		return nil, fmt.Errorf("source output index out of range")
	}
	out := s.PreviousTx.TxOut[s.Vtxo.Outpoint.Index]
	if s.Vtxo.Amount != out.Value {
		return nil, fmt.Errorf("source amount disagrees with transaction")
	}
	if _, err := addAmount(0, out.Value); err != nil {
		return nil, err
	}
	return out, nil
}

// Source contains only unsigned construction data, so there is no caller PSBT
// unknown/signature map to sanitize or accidentally copy. Authenticate the full
// ordered tree and selected proof against the actual output before BuildTxs.
// This proves linkage, not acceptance or unspentness; those require the indexer.
func admitSource(s Source, server [32]byte) (Source, error) {
	out, err := sourceOutput(s)
	if err != nil {
		return Source{}, err
	}
	v := s.Vtxo
	if v.Tapscript == nil || v.Tapscript.ControlBlock == nil {
		return Source{}, fmt.Errorf("missing selected Taproot proof")
	}
	control := v.Tapscript.ControlBlock
	if control.InternalKey == nil || !control.InternalKey.IsEqual(script.UnspendableKey()) || control.LeafVersion != txscript.BaseLeafVersion {
		return Source{}, fmt.Errorf("selected proof requires TapScript and the NUMS internal key")
	}
	encodedControl, err := control.ToBytes()
	if err != nil {
		return Source{}, err
	}
	control, err = txscript.ParseControlBlock(encodedControl)
	if err != nil {
		return Source{}, fmt.Errorf("invalid selected control block: %w", err)
	}
	selected := v.Tapscript.RevealedScript
	if !collaborativeLeaf(selected, server) {
		return Source{}, fmt.Errorf("selected path must be canonical collaborative n-of-n containing the server")
	}
	if len(v.RevealedTapscripts) == 0 || len(v.RevealedTapscripts) > txutils.MaxLeaves {
		return Source{}, fmt.Errorf("missing or oversized source tree")
	}
	leaves := make([]txscript.TapLeaf, len(v.RevealedTapscripts))
	found := false
	for i, encoded := range v.RevealedTapscripts {
		leaf, err := hex.DecodeString(encoded)
		if err != nil || len(leaf) > txscript.MaxScriptSize {
			return Source{}, fmt.Errorf("invalid source tree leaf %d", i)
		}
		found = found || bytes.Equal(leaf, selected)
		leaves[i] = txscript.NewBaseTapLeaf(leaf)
	}
	if !found {
		return Source{}, fmt.Errorf("selected leaf absent from source tree")
	}
	tree := txscript.AssembleTaprootScriptTree(leaves...)
	root := tree.RootNode.TapHash()
	key := txscript.ComputeTaprootOutputKey(script.UnspendableKey(), root[:])
	expected, err := script.P2TRScript(key)
	if err != nil {
		return Source{}, err
	}
	if !bytes.Equal(expected, out.PkScript) {
		return Source{}, fmt.Errorf("source tree does not commit to source output")
	}
	if err := txscript.VerifyTaprootLeafCommitment(control, schnorr.SerializePubKey(key), selected); err != nil {
		return Source{}, fmt.Errorf("selected source proof: %w", err)
	}
	// Keep only the selected path fields consumed by upstream. Other waddrmgr
	// modes are not alternate authorities for the explicitly supplied proof.
	cb := *control
	cb.InclusionProof = bytes.Clone(control.InclusionProof)
	outpoint := *v.Outpoint
	v.Outpoint = &outpoint
	v.Tapscript = &waddrmgr.Tapscript{
		Type: waddrmgr.TapscriptTypePartialReveal, ControlBlock: &cb,
		RevealedScript: bytes.Clone(selected),
	}
	v.RevealedTapscripts = append([]string(nil), v.RevealedTapscripts...)
	return Source{Vtxo: v, PreviousTx: s.PreviousTx.Copy()}, nil
}

func collaborativeLeaf(leaf []byte, server [32]byte) bool {
	if len(leaf) == 0 || len(leaf)%34 != 0 {
		return false
	}
	found := false
	for offset := 0; offset < len(leaf); offset += 34 {
		key := leaf[offset+1 : offset+33]
		op := byte(txscript.OP_CHECKSIGVERIFY)
		if offset+34 == len(leaf) {
			op = txscript.OP_CHECKSIG
		}
		if leaf[offset] != 32 || leaf[offset+33] != op {
			return false
		}
		if _, err := schnorr.ParsePubKey(key); err != nil {
			return false
		}
		found = found || bytes.Equal(key, server[:])
	}
	return found
}

// buildUnsigned is the poker admission adapter around upstream construction.
// Outputs already have the action's extension and do not have an anchor.
// Spending, if supplied, describes only the covenant source at input zero.
func buildUnsigned(sources []Source, outputs []*wire.TxOut, checkpointScript []byte, server [32]byte, spending *SpendingPath) (*Unsigned, error) {
	if len(sources) == 0 || len(outputs) == 0 {
		return nil, fmt.Errorf("covenant: inputs and action outputs required")
	}
	admitted := make([]Source, len(sources))
	vtxos := make([]offchain.VtxoInput, len(sources))
	seen := make(map[wire.OutPoint]bool, len(sources))
	var inputTotal, outputTotal int64
	for i, source := range sources {
		s, err := admitSource(source, server)
		if err != nil {
			return nil, fmt.Errorf("covenant: input %d: %w", i, err)
		}
		if seen[*s.Vtxo.Outpoint] {
			return nil, fmt.Errorf("covenant: input %d duplicates source %s", i, s.Vtxo.Outpoint)
		}
		seen[*s.Vtxo.Outpoint] = true
		inputTotal, err = addAmount(inputTotal, s.Vtxo.Amount)
		if err != nil {
			return nil, err
		}
		admitted[i], vtxos[i] = s, s.Vtxo
	}
	if spending != nil {
		if spending.Leaf == nil || spending.Leaf.LeafVersion != txscript.BaseLeafVersion {
			return nil, fmt.Errorf("covenant: invalid spending metadata")
		}
		control, err := admitted[0].Vtxo.Tapscript.ControlBlock.ToBytes()
		if err != nil || !bytes.Equal(control, spending.Leaf.ControlBlock) || !bytes.Equal(admitted[0].Vtxo.Tapscript.RevealedScript, spending.Leaf.Script) {
			return nil, fmt.Errorf("covenant: spending metadata differs from selected source proof")
		}
	}
	clonedOutputs := make([]*wire.TxOut, len(outputs))
	for i, out := range outputs {
		if out == nil || bytes.Equal(out.PkScript, txutils.AnchorOutput().PkScript) {
			return nil, fmt.Errorf("covenant: nil or premature anchor at output %d", i)
		}
		var err error
		outputTotal, err = addAmount(outputTotal, out.Value)
		if err != nil {
			return nil, fmt.Errorf("covenant: output %d: %w", i, err)
		}
		clonedOutputs[i] = wire.NewTxOut(out.Value, bytes.Clone(out.PkScript))
	}
	if inputTotal != outputTotal {
		return nil, fmt.Errorf("covenant: input total %d does not equal output total %d", inputTotal, outputTotal)
	}
	main, checkpoints, err := offchain.BuildTxs(vtxos, clonedOutputs, bytes.Clone(checkpointScript))
	if err != nil {
		return nil, fmt.Errorf("covenant: upstream transaction construction: %w", err)
	}
	result := &Unsigned{Ark: main, Checkpoints: checkpoints}
	for i, s := range admitted {
		cp := checkpoints[i]
		if err := checkBuiltLink(main, cp, i, s); err != nil {
			return nil, err
		}
		cp.Inputs[0].NonWitnessUtxo = s.PreviousTx.Copy()
		main.Inputs[i].NonWitnessUtxo = cp.UnsignedTx.Copy()
		// Ark mains expose the logical source before the checkpoint. PrevoutTx
		// is the separate onchain API field and must not replace PrevArkTx.
		if s.PreviousTx.SerializeSize() > arkade.MaxPrevoutTxLength {
			return nil, fmt.Errorf("covenant: input %d previous transaction exceeds emulator field limit", i)
		}
		if err := txutils.SetArkPsbtField(main, i, arkade.PrevArkTxField, *s.PreviousTx); err != nil {
			return nil, err
		}
		if i == 0 && spending != nil {
			path := *spending
			path.Program = bytes.Clone(path.Program)
			path.Leaf = cloneLeaf(main.Inputs[0].TaprootLeafScript[0])
			trees, err := txutils.GetArkPsbtFields(main, 0, txutils.VtxoTaprootTreeField)
			if err != nil || len(trees) != 1 {
				return nil, fmt.Errorf("covenant: missing constructed checkpoint tree")
			}
			path.Tree = append(txutils.TapTree(nil), trees[0]...)
			result.Spending = []InputSpending{{InputIndex: 0, Path: path}}
		}
	}
	return result, nil
}

func cloneLeaf(leaf *psbt.TaprootTapLeafScript) *psbt.TaprootTapLeafScript {
	if leaf == nil {
		return nil
	}
	return &psbt.TaprootTapLeafScript{ControlBlock: bytes.Clone(leaf.ControlBlock), Script: bytes.Clone(leaf.Script), LeafVersion: leaf.LeafVersion}
}

func checkBuiltLink(main, cp *psbt.Packet, i int, source Source) error {
	if len(cp.Inputs) != 1 || len(cp.UnsignedTx.TxIn) != 1 || len(cp.UnsignedTx.TxOut) != 2 || cp.UnsignedTx.TxIn[0].PreviousOutPoint != *source.Vtxo.Outpoint || main.UnsignedTx.TxIn[i].PreviousOutPoint != (wire.OutPoint{Hash: cp.UnsignedTx.TxHash(), Index: 0}) {
		return fmt.Errorf("covenant: upstream checkpoint linkage mismatch at input %d", i)
	}
	prev, _ := sourceOutput(source) // admitted before construction
	if !equalOutput(cp.Inputs[0].WitnessUtxo, prev) || !equalOutput(main.Inputs[i].WitnessUtxo, cp.UnsignedTx.TxOut[0]) || cp.UnsignedTx.TxOut[0].Value != prev.Value || !equalOutput(cp.UnsignedTx.TxOut[1], txutils.AnchorOutput()) || !equalOutput(main.UnsignedTx.TxOut[len(main.UnsignedTx.TxOut)-1], txutils.AnchorOutput()) {
		return fmt.Errorf("covenant: upstream source value or anchor mismatch at input %d", i)
	}
	for _, item := range []struct {
		input psbt.PInput
		out   *wire.TxOut
	}{{cp.Inputs[0], prev}, {main.Inputs[i], cp.UnsignedTx.TxOut[0]}} {
		if len(item.input.TaprootLeafScript) != 1 || item.input.TaprootLeafScript[0] == nil {
			return fmt.Errorf("covenant: upstream selected leaf missing at input %d", i)
		}
		leaf := item.input.TaprootLeafScript[0]
		control, err := txscript.ParseControlBlock(leaf.ControlBlock)
		if err != nil || leaf.LeafVersion != txscript.BaseLeafVersion || !bytes.Equal(leaf.Script, source.Vtxo.Tapscript.RevealedScript) || !txscript.IsPayToTaproot(item.out.PkScript) {
			return fmt.Errorf("covenant: upstream selected path changed at input %d", i)
		}
		if err := txscript.VerifyTaprootLeafCommitment(control, item.out.PkScript[2:], leaf.Script); err != nil {
			return fmt.Errorf("covenant: upstream selected proof at input %d: %w", i, err)
		}
	}
	return nil
}

func equalOutput(a, b *wire.TxOut) bool {
	return a != nil && b != nil && a.Value == b.Value && bytes.Equal(a.PkScript, b.PkScript)
}
