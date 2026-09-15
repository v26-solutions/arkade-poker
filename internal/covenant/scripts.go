package covenant

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
)

// These are semantic identities, not assigned protocol bytes. Compilation will
// reject combinations that do not correspond to a poker spending specialization.
type SpendingKind uint8

const (
	SpendPlayer1Funding SpendingKind = iota + 1
	SpendPlayer2Opening
	SpendBetting
	SpendBoardReveal
	SpendShowdownReveal
	SpendAllInCall
	SpendAllInReveal
	SpendConcession
	SpendTimeout
	SpendShowdown
)

type SpendingIdentity struct {
	Kind   SpendingKind
	Actor  Player
	Street Street
	Pass   RevealPass
}

// One board/showdown reveal path covers both passes. Settlement shares a single
// program between two participant leaves. Initial Player2 deposit executes none.
// Enumeration order is stable; the service tree is sorted by TapLeafHash.
func spendingIdentities() []SpendingIdentity {
	all := []SpendingIdentity{{Kind: SpendPlayer1Funding, Actor: Player1}, {Kind: SpendPlayer2Opening, Actor: Player2}}
	for _, kind := range []SpendingKind{SpendBetting, SpendBoardReveal, SpendShowdownReveal, SpendAllInCall, SpendAllInReveal, SpendConcession, SpendTimeout, SpendShowdown} {
		streets := []Street{0}
		switch kind {
		case SpendBetting, SpendAllInCall:
			streets = []Street{PreFlop, Flop, Turn, River}
		case SpendBoardReveal, SpendAllInReveal:
			streets = []Street{Flop, Turn, River}
		}
		for _, street := range streets {
			for _, actor := range []Player{Player1, Player2} {
				all = append(all, SpendingIdentity{Kind: kind, Actor: actor, Street: street})
			}
		}
	}
	return all
}
func compileSpendingPaths(params Params, id ContractID) ([]byte, []SpendingPath, error) {
	showdown, err := emitShowdown(params, id)
	if err != nil {
		return nil, nil, err
	}
	var paths []SpendingPath
	for _, identity := range spendingIdentities() {
		var program []byte
		switch identity.Kind {
		case SpendPlayer1Funding:
			program, err = emitPlayer1Funding(params, id)
		case SpendPlayer2Opening:
			program, err = emitPlayer2Opening(params, id)
		case SpendBetting:
			program, err = emitBetting(params, id, identity.Street, identity.Actor)
		case SpendBoardReveal:
			program, err = emitBoardReveal(params, id, identity.Street, identity.Actor)
		case SpendShowdownReveal:
			program, err = emitShowdownReveal(params, id, identity.Actor)
		case SpendAllInCall:
			program, err = emitAllInCall(params, id, identity.Street, identity.Actor)
		case SpendAllInReveal:
			program, err = emitAllInReveal(params, id, identity.Street, identity.Actor)
		case SpendConcession:
			program, err = emitConcession(params, id, identity.Actor)
		case SpendTimeout:
			program, err = emitTimeout(params, id, identity.Actor)
		case SpendShowdown:
			program = bytes.Clone(showdown)
		default:
			return nil, nil, fmt.Errorf("covenant: unknown spending identity")
		}
		if err != nil {
			return nil, nil, fmt.Errorf("covenant: compile %+v: %w", identity, err)
		}
		outer, key, err := outerLeaf(params, identity.Actor, program)
		if err != nil {
			return nil, nil, err
		}
		paths = append(paths, SpendingPath{Identity: identity, Program: program, TweakedEmulatorKey: key, Leaf: &psbt.TaprootTapLeafScript{Script: outer, LeafVersion: txscript.BaseLeafVersion}})
	}
	sorted := append([]SpendingPath(nil), paths...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a := txscript.NewBaseTapLeaf(sorted[i].Leaf.Script).TapHash()
		b := txscript.NewBaseTapLeaf(sorted[j].Leaf.Script).TapHash()
		return bytes.Compare(a[:], b[:]) < 0
	})
	tree := &script.TapscriptsVtxoScript{}
	var scripts txutils.TapTree
	seen := make(map[string]bool)
	for _, path := range sorted {
		// Equal participant signing keys can produce identical settlement leaves.
		// Keep both semantic paths but commit that identical spend predicate once;
		// btcd's indexed proof builder cannot represent duplicate leaf hashes.
		encoded := hex.EncodeToString(path.Leaf.Script)
		if seen[encoded] {
			continue
		}
		seen[encoded] = true
		closure, err := script.DecodeClosure(path.Leaf.Script)
		if err != nil {
			return nil, nil, err
		}
		tree.Closures = append(tree.Closures, closure)
		scripts = append(scripts, encoded)
	}
	outputKey, proofs, err := tree.TapTree()
	if err != nil {
		return nil, nil, err
	}
	output, err := script.P2TRScript(outputKey)
	if err != nil {
		return nil, nil, err
	}
	for i := range paths {
		hash := txscript.NewBaseTapLeaf(paths[i].Leaf.Script).TapHash()
		proof, err := proofs.GetTaprootMerkleProof(hash)
		if err != nil {
			return nil, nil, err
		}
		paths[i].Leaf.ControlBlock = bytes.Clone(proof.ControlBlock)
		paths[i].Tree = append(txutils.TapTree(nil), scripts...)
		cb, err := txscript.ParseControlBlock(proof.ControlBlock)
		if err != nil {
			return nil, nil, err
		}
		if err := txscript.VerifyTaprootLeafCommitment(cb, output[2:], paths[i].Leaf.Script); err != nil {
			return nil, nil, err
		}
	}
	return output, paths, nil
}
