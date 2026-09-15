package game

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"

	"arkade-poker/go/internal/covenant"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
)

type handHarness struct {
	t     *testing.T
	games [2]*Game
	logs  [2][][]byte
	known map[chainhash.Hash]*wire.MsgTx
	nonce uint32
	at    covenant.UnixSeconds
	paths map[covenant.SpendingIdentity]int
}

func newHandHarness(t *testing.T) *handHarness {
	t.Helper()
	f := completeSetup(t)
	h := &handHarness{t: t, known: map[chainhash.Hash]*wire.MsgTx{}, at: 1000, paths: map[covenant.SpendingIdentity]int{}}
	for i := range h.games {
		h.logs[i] = append([][]byte(nil), f.logs[i]...)
		h.games[i] = replayBytes(t, f.configs[i], h.logs[i])
	}
	return h
}
func (h *handHarness) record(i int, e Event) {
	h.t.Helper()
	applyRecord(h.t, h.games[i], e, &h.logs[i])
}
func (h *handHarness) funding(i int, amount int64) covenant.Funding {
	h.t.Helper()
	if amount == 0 {
		return covenant.Funding{}
	}
	c := h.games[i].config
	owner, err := schnorr.ParsePubKey(c.Wallet.WalletPublicKey[:])
	if err != nil {
		h.t.Fatal(err)
	}
	server, err := schnorr.ParsePubKey(c.Wallet.ArkSigningKey[:])
	if err != nil {
		h.t.Fatal(err)
	}
	tree := script.NewDefaultVtxoScript(owner, server, arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 144})
	key, proofs, err := tree.TapTree()
	if err != nil {
		h.t.Fatal(err)
	}
	leaves, err := tree.Encode()
	if err != nil {
		h.t.Fatal(err)
	}
	leaf, err := tree.Closures[1].Script()
	if err != nil {
		h.t.Fatal(err)
	}
	proof, err := proofs.GetTaprootMerkleProof(txscript.NewBaseTapLeaf(leaf).TapHash())
	if err != nil {
		h.t.Fatal(err)
	}
	cb, err := txscript.ParseControlBlock(proof.ControlBlock)
	if err != nil {
		h.t.Fatal(err)
	}
	pk, err := script.P2TRScript(key)
	if err != nil {
		h.t.Fatal(err)
	}
	h.nonce++
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Index: h.nonce}, Sequence: wire.MaxTxInSequenceNum})
	tx.AddTxOut(wire.NewTxOut(amount, pk))
	h.known[tx.TxHash()] = tx
	source := covenant.Source{PreviousTx: tx, Vtxo: offchain.VtxoInput{Outpoint: &wire.OutPoint{Hash: tx.TxHash()}, Amount: amount, Tapscript: &waddrmgr.Tapscript{ControlBlock: cb, RevealedScript: proof.Script}, RevealedTapscripts: leaves}}
	return covenant.Funding{Inputs: []covenant.Source{source}}
}

type handFetcher struct {
	txscript.PrevOutputFetcher
	previous map[wire.OutPoint]*wire.MsgTx
	indices  map[wire.OutPoint]uint32
}

func (f handFetcher) FetchPrevOutArkTx(op wire.OutPoint) *wire.MsgTx { return f.previous[op] }
func (f handFetcher) FetchVtxoPrevOutPkScript(op wire.OutPoint) []byte {
	tx := f.previous[op]
	if tx == nil {
		return nil
	}
	return tx.TxOut[f.indices[op]].PkScript
}

// VM authority is the actual emulator consuming real upstream-built PSBTs and
// independently retained source transactions. No game transition checker is used
// as the expected execution result.
func (h *handHarness) execute(i int, b *covenant.Unsigned) error {
	base := txscript.NewMultiPrevOutFetcher(nil)
	fetcher := handFetcher{base, map[wire.OutPoint]*wire.MsgTx{}, map[wire.OutPoint]uint32{}}
	for index, cp := range b.Checkpoints {
		op := cp.UnsignedTx.TxIn[0].PreviousOutPoint
		source := h.known[op.Hash]
		if source == nil || uint64(op.Index) >= uint64(len(source.TxOut)) {
			return fmt.Errorf("unknown fixture source")
		}
		fields, err := txutils.GetArkPsbtFields(b.Ark, index, arkade.PrevArkTxField)
		if err != nil || len(fields) != 1 {
			return fmt.Errorf("source field: %v", err)
		}
		var a, z bytes.Buffer
		_ = source.Serialize(&a)
		_ = fields[0].Serialize(&z)
		if !bytes.Equal(a.Bytes(), z.Bytes()) {
			return fmt.Errorf("source transaction changed")
		}
		if !equalOutput(cp.Inputs[0].WitnessUtxo, source.TxOut[op.Index]) || !equalOutput(b.Ark.Inputs[index].WitnessUtxo, cp.UnsignedTx.TxOut[0]) {
			return fmt.Errorf("fixture source value")
		}
		if err := arkade.VerifyTaprootLeafCommitment(source.TxOut[op.Index].PkScript, cp.Inputs[0].TaprootLeafScript[0]); err != nil {
			return err
		}
		if err := arkade.VerifyTaprootLeafCommitment(cp.UnsignedTx.TxOut[0].PkScript, b.Ark.Inputs[index].TaprootLeafScript[0]); err != nil {
			return err
		}
		trees, err := txutils.GetArkPsbtFields(cp, 0, txutils.VtxoTaprootTreeField)
		if err != nil || len(trees) != 1 {
			return fmt.Errorf("source tree")
		}
		var leaves []txscript.TapLeaf
		for _, encoded := range trees[0] {
			leaf, err := hex.DecodeString(encoded)
			if err != nil {
				return err
			}
			leaves = append(leaves, txscript.NewBaseTapLeaf(leaf))
		}
		tree := txscript.AssembleTaprootScriptTree(leaves...)
		root := tree.RootNode.TapHash()
		key := txscript.ComputeTaprootOutputKey(script.UnspendableKey(), root[:])
		pk, err := script.P2TRScript(key)
		if err != nil {
			return err
		}
		if !bytes.Equal(pk, source.TxOut[op.Index].PkScript) {
			return fmt.Errorf("source tree commitment")
		}
		mainOp := b.Ark.UnsignedTx.TxIn[index].PreviousOutPoint
		base.AddPrevOut(mainOp, b.Ark.Inputs[index].WitnessUtxo)
		fetcher.previous[mainOp] = source
		fetcher.indices[mainOp] = op.Index
	}
	entries, err := arkade.FindEmulatorPacket(b.Ark.UnsignedTx)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		if len(b.Spending) != 0 {
			return fmt.Errorf("missing program")
		}
		return nil
	}
	if len(entries) != 1 || len(b.Spending) != 1 {
		return fmt.Errorf("program count")
	}
	key := h.games[i].config.Wallet.EmulatorSigningKey
	emulator, err := schnorr.ParsePubKey(key[:])
	if err != nil {
		return err
	}
	program, err := arkade.ReadArkadeScript(b.Ark, emulator, entries[0])
	if err != nil {
		return err
	}
	return program.Execute(b.Ark.UnsignedTx, fetcher, 0)
}
func (h *handHarness) action(i int, input Input) {
	h.t.Helper()
	g := h.games[i]
	amount, err := g.RequiredFunding(input)
	if err != nil {
		h.t.Fatalf("funding at %v: %v", g.stage, err)
	}
	e, err := g.PrepareAction(context.Background(), nil, input, h.funding(i, amount), h.at)
	if err != nil {
		h.t.Fatalf("prepare at %v: %v", g.stage, err)
	}
	h.record(i, e)
	built := g.prepared.built
	if err := h.execute(i, built); err != nil {
		h.t.Fatalf("real VM for action %d: %v", e.Spend.Action.Kind, err)
	}
	if len(built.Spending) > 0 {
		h.paths[built.Spending[0].Path.Identity]++
	}
	saved, err := g.PendingSpend()
	if err != nil {
		h.t.Fatal(err)
	}
	signed := saved.Prepared
	saved.Signed = &signed
	signEvent := g.NewEvent(SpendSigned)
	signEvent.Spend = &saved
	h.record(i, signEvent)
	receipt := g.NewEvent(SubmissionAttempted)
	id := built.Ark.UnsignedTx.TxHash()
	receipt.Receipt = &SubmissionReceipt{TxID: &id}
	receipt.ObservedAt = h.at
	h.record(i, receipt)
	accepted := acceptedBuild(built)
	h.known[id] = accepted.Transaction
	kind := SpendObserved
	if e.Spend.Action.Kind == ActionInitialDeposit {
		kind = DepositObserved
	}
	for player := range h.games {
		event := h.games[player].NewEvent(kind)
		event.Accepted = &accepted
		event.ObservedAt = h.at
		h.record(player, event)
	}
	h.at++
}
func (h *handHarness) private() {
	h.t.Helper()
	for i, g := range h.games {
		if g.openingReady() {
			e, err := g.PrepareOpening(context.Background(), nil, g.hand.accepted, g.hand.observedAt)
			if err != nil {
				h.t.Fatal(err)
			}
			h.record(i, e)
		}
	}
}
func (h *handHarness) start(opening int64) {
	h.t.Helper()
	h.action(1, Input{Kind: Progress})
	h.action(0, Input{Kind: Progress})
	h.private()
	bet := covenant.BettingAction{Kind: covenant.Check}
	if opening != 0 {
		bet = covenant.BettingAction{Kind: covenant.RaiseTo, Amount: opening}
	}
	h.action(1, Input{Kind: Bet, Bet: bet})
	h.private()
}
func (h *handHarness) actor() int {
	h.t.Helper()
	state, err := covenant.ReadState(h.games[0].hand.accepted.Transaction)
	if err != nil {
		h.t.Fatal(err)
	}
	actor, err := state.Phase.RequiredActor()
	if err != nil || actor == 0 {
		h.t.Fatal("missing actor", err)
	}
	return int(actor) - 1
}
func (h *handHarness) automaticBoard() {
	h.t.Helper()
	for h.games[0].hand != nil {
		state, _ := covenant.ReadState(h.games[0].hand.accepted.Transaction)
		if state.Phase.Kind != covenant.BoardReveal {
			return
		}
		h.action(h.actor(), Input{Kind: Progress})
	}
}
func (h *handHarness) evaluateAndSettle(submitter int) {
	h.t.Helper()
	for i, g := range h.games {
		if g.stage != StageEvaluateShowdown {
			h.t.Fatalf("not evaluation: %v", g.stage)
		}
		e, err := g.PrepareEvaluation(context.Background())
		if err != nil {
			h.t.Fatal(err)
		}
		h.record(i, e)
	}
	if submitter < 0 {
		submitter = 0
		if h.games[0].evaluated.winner == covenant.Player2 {
			submitter = 1
		}
	}
	h.at += 30
	h.action(submitter, Input{Kind: Progress})
	for _, g := range h.games {
		if g.stage != StageFinished || g.outcome == nil || g.prepared != nil || g.hand != nil {
			h.t.Fatal("missing accepted terminal outcome")
		}
		snap, err := g.Snapshot()
		if err != nil || snap.State == nil || snap.Choice != nil {
			h.t.Fatal("settled table lost or still actionable", err)
		}
		for _, card := range dealOrder(snap.Cards) {
			if !card.Known {
				h.t.Fatal("settlement hid a revealed card")
			}
		}
		// Snapshot callers cannot mutate the saved final display state.
		snap.State.Wagers.Player1++
		again, _ := g.Snapshot()
		if again.State.Wagers.Player1 == snap.State.Wagers.Player1 {
			h.t.Fatal("shared final state pointer")
		}
	}
}
func (h *handHarness) replay() {
	h.t.Helper()
	for i, log := range h.logs {
		events := make([]Event, 0, len(log))
		for j, b := range log {
			e, err := DecodeEvent(b)
			if err != nil {
				h.t.Fatal(err)
			}
			events = append(events, e)
			// Reopen key decision/preparation/acceptance prefixes independently. All
			// records also pass full replay, including every intermediate event.
			if e.Kind == SpendPrepared || e.Kind == OpeningObserved || j == len(log)-1 {
				g, err := Replay(h.games[i].config, events)
				if err != nil {
					h.t.Fatalf("replay player %d prefix %d: %v", i+1, j+1, err)
				}
				if e.Kind == SpendPrepared {
					s, err := g.PendingSpend()
					if err != nil || !sameSaved(s, *e.Spend) {
						h.t.Fatal("replay changed exact work", err)
					}
				}
				if j == len(log)-1 {
					want, _ := h.games[i].Snapshot()
					got, _ := g.Snapshot()
					if !reflect.DeepEqual(want, got) {
						h.t.Fatal("replayed outcome changed")
					}
				}
				if g.setup != nil {
					_ = g.setup.secrets.Destroy()
				}
			}
		}
		for _, e := range events {
			if e.Secrets != nil {
				_ = e.Secrets.Destroy()
			}
		}
	}
}
func TestOrdinaryHandRealEmulatorAndReplay(t *testing.T) {
	h := newHandHarness(t)
	h.start(0)
	// Complete four streets with checks. First preflop check closes the street;
	// later streets start with LastStart and require both actors to check.
	for street := covenant.PreFlop; street <= covenant.River; street++ {
		for {
			state, _ := covenant.ReadState(h.games[0].hand.accepted.Transaction)
			if state.Phase.Kind != covenant.Betting {
				break
			}
			h.action(h.actor(), Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Check}})
		}
		if street < covenant.River {
			h.automaticBoard()
		}
	}
	first := h.actor()
	for i, g := range h.games {
		snap, err := g.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		holes := snap.Cards.HoleCards.Player2
		if i == 1 {
			holes = snap.Cards.HoleCards.Player1
		}
		if holes[0].Known || holes[1].Known {
			t.Fatal("opponent exposed before showdown reveal")
		}
	}
	h.action(first, Input{Kind: RevealShowdown})
	for i, g := range h.games {
		snap, err := g.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		holes := snap.Cards.HoleCards.Player2
		if i == 1 {
			holes = snap.Cards.HoleCards.Player1
		}
		want := i != first
		if holes[0].Known != want || holes[1].Known != want {
			t.Fatal("accepted reveal not reflected for the right opponent")
		}
	}
	h.action(h.actor(), Input{Kind: RevealShowdown})
	h.evaluateAndSettle(-1)
	h.replay()
}
func TestAllInEveryStreetEitherBettorRealEmulator(t *testing.T) {
	for street := covenant.PreFlop; street <= covenant.River; street++ {
		for bettor := 0; bettor < 2; bettor++ {
			t.Run(fmt.Sprintf("street_%d_player_%d", street, bettor+1), func(t *testing.T) {
				h := newHandHarness(t)
				opening := int64(0)
				if street == covenant.PreFlop && bettor == 1 {
					opening = testTerms.MaxWager
				}
				h.start(opening)
				for s := covenant.PreFlop; s < street; s++ {
					for {
						state, _ := covenant.ReadState(h.games[0].hand.accepted.Transaction)
						if state.Phase.Kind != covenant.Betting {
							break
						}
						h.action(h.actor(), Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Check}})
					}
					h.automaticBoard()
				}
				state, _ := covenant.ReadState(h.games[0].hand.accepted.Transaction)
				if state.Phase.Kind != covenant.AllIn {
					if h.actor() != bettor {
						// A small raise followed by a call would end preflop; instead let the
						// first actor make the minimum raise, then the selected bettor caps it.
						h.action(h.actor(), Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.RaiseTo, Amount: testTerms.MinBet}})
					}
					h.action(bettor, Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.RaiseTo, Amount: testTerms.MaxWager}})
				}
				h.action(h.actor(), Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Call}})
				h.action(h.actor(), Input{Kind: RevealShowdown})
				h.evaluateAndSettle(bettor)
				if h.games[0].outcome.Settlement.Kind != SettlementShowdown {
					t.Fatal("not showdown")
				}
			})
		}
	}
}
