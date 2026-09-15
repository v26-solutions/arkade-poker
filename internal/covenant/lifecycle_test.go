package covenant

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"arkade-poker/go/internal/merkel"
	"arkade-poker/go/internal/shuffle"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
)

type testHand struct {
	t          *testing.T
	c          *Contract
	tx         *wire.MsgTx
	known      map[chainhash.Hash]*wire.MsgTx
	nonce      byte
	covered    map[SpendingIdentity]int
	revealCard func(Player, byte, shuffle.MaskedCard) CardReveal
}

func newTestHand(t *testing.T, covered map[SpendingIdentity]int) *testHand {
	t.Helper()
	_, _, checkpoint := sourceFixture(t, 1, 0)
	c, err := Derive(cryptoParams(t), checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	return &testHand{t: t, c: c, known: map[chainhash.Hash]*wire.MsgTx{}, covered: covered}
}
func (h *testHand) funding(amount int64) Funding {
	if amount == 0 {
		return Funding{}
	}
	h.nonce++
	s, _, _ := sourceFixture(h.t, amount, h.nonce)
	h.known[s.PreviousTx.TxHash()] = s.PreviousTx
	return Funding{Inputs: []Source{s}}
}
func (h *testHand) state() State {
	s, err := ReadState(h.tx)
	if err != nil {
		h.t.Fatal(err)
	}
	return *s
}
func (h *testHand) reveal(kind RevealKind, actor Player) RevealWitness {
	params := h.c.params
	cards := dealtOrder(params.EncryptedDeal)
	slotCard := [18]int{0, 1, 2, 3, 4, 5, 6, 4, 5, 6, 7, 7, 8, 8, 0, 1, 2, 3}
	r := RevealWitness{Kind: kind}
	p := byte(actor - Player1)
	proof := func(slot byte) CardReveal {
		if h.revealCard != nil {
			return h.revealCard(actor, slot, cards[slotCard[slot]])
		}
		return fixtureReveal(h.t, params, h.c.id, actor, slot, cards[slotCard[slot]])
	}
	switch kind {
	case RevealOpponentHoles:
		start := 2 * (1 - p)
		r.Holes = [2]CardReveal{proof(start), proof(start + 1)}
	case RevealFlop:
		for i := range r.Flop {
			r.Flop[i] = proof(4 + 3*p + byte(i))
		}
	case RevealTurn:
		r.Turn = proof(10 + p)
	case RevealRiver:
		r.River = proof(12 + p)
	case RevealOwnHoles:
		r.Holes = [2]CardReveal{proof(14 + 2*p), proof(15 + 2*p)}
	case RevealRemainingFromFlop, RevealRemainingFromTurn, RevealRemainingFromRiver:
		if kind == RevealRemainingFromFlop {
			for i := range r.Flop {
				r.Flop[i] = proof(4 + 3*p + byte(i))
			}
		}
		if kind != RevealRemainingFromRiver {
			r.Turn = proof(10 + p)
		}
		r.River = proof(12 + p)
		r.Holes = [2]CardReveal{proof(14 + 2*p), proof(15 + 2*p)}
	}
	return r
}

// executeKnownBundle authenticates the fixture's independently retained source
// transactions, then delegates program admission and execution to arkade.
// It never evaluates poker transitions, reveals or payouts itself.
func executeKnownBundle(c *Contract, b *Unsigned, known map[chainhash.Hash]*wire.MsgTx) error {
	if b == nil || b.Ark == nil || len(b.Checkpoints) != len(b.Ark.Inputs) {
		return fmt.Errorf("fixture bundle shape")
	}
	main := b.Ark
	base := txscript.NewMultiPrevOutFetcher(nil)
	fetcher := fixtureFetcher{base, map[wire.OutPoint]*wire.MsgTx{}, map[wire.OutPoint]uint32{}}
	for i, cp := range b.Checkpoints {
		op := cp.UnsignedTx.TxIn[0].PreviousOutPoint
		previous := known[op.Hash]
		if previous == nil {
			return fmt.Errorf("fixture unknown logical source")
		}
		fields, err := txutils.GetArkPsbtFields(main, i, arkade.PrevArkTxField)
		if err != nil || len(fields) != 1 {
			return fmt.Errorf("fixture previous field: %v", err)
		}
		var want, got bytes.Buffer
		previous.SerializeNoWitness(&want)
		fields[0].SerializeNoWitness(&got)
		if !bytes.Equal(want.Bytes(), got.Bytes()) {
			return fmt.Errorf("fixture source transaction mismatch")
		}
		cb, err := txscript.ParseControlBlock(cp.Inputs[0].TaprootLeafScript[0].ControlBlock)
		if err != nil {
			return err
		}
		trees, err := txutils.GetArkPsbtFields(cp, 0, txutils.VtxoTaprootTreeField)
		if err != nil || len(trees) != 1 {
			return fmt.Errorf("fixture tree: %v", err)
		}
		source := Source{PreviousTx: previous, Vtxo: offchain.VtxoInput{Outpoint: &op, Amount: previous.TxOut[op.Index].Value, Tapscript: &waddrmgr.Tapscript{ControlBlock: cb, RevealedScript: cp.Inputs[0].TaprootLeafScript[0].Script}, RevealedTapscripts: trees[0]}}
		if _, err := admitSource(source, c.params.ArkSigningKey); err != nil {
			return err
		}
		if err := checkBuiltLink(main, cp, i, source); err != nil {
			return err
		}
		mainOp := main.UnsignedTx.TxIn[i].PreviousOutPoint
		base.AddPrevOut(mainOp, main.Inputs[i].WitnessUtxo)
		fetcher.previous[mainOp] = previous
		fetcher.indices[mainOp] = op.Index
	}
	entries, err := arkade.FindEmulatorPacket(main.UnsignedTx)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		if len(b.Spending) != 0 {
			return fmt.Errorf("fixture missing execution entry")
		}
		return nil
	}
	if len(entries) != 1 || len(b.Spending) != 1 || b.Spending[0].InputIndex != 0 {
		return fmt.Errorf("fixture execution layout")
	}
	emulator, _ := schnorr.ParsePubKey(c.params.EmulatorSigningKey[:])
	path := b.Spending[0].Path
	if !bytes.Equal(path.Program, entries[0].Script) || !bytes.Equal(path.Leaf.ControlBlock, main.Inputs[0].TaprootLeafScript[0].ControlBlock) {
		return fmt.Errorf("fixture spending metadata")
	}
	if err := arkade.VerifyTaprootLeafCommitment(main.Inputs[0].WitnessUtxo.PkScript, path.Leaf); err != nil {
		return err
	}
	program, err := arkade.ReadArkadeScript(main, emulator, entries[0])
	if err != nil {
		return err
	}
	return program.Execute(main.UnsignedTx, fetcher, 0)
}
func (h *testHand) accept(b *Unsigned, err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
	if err := executeKnownBundle(h.c, b, h.known); err != nil {
		h.t.Fatalf("execute %+v: %v", func() any {
			if len(b.Spending) > 0 {
				return b.Spending[0].Path.Identity
			}
			return "initial"
		}(), err)
	}
	if len(b.Spending) > 0 && h.covered != nil {
		h.covered[b.Spending[0].Path.Identity]++
	}
	if next, err := ReadState(b.Ark.UnsignedTx); err == nil && h.tx != nil {
		if want := h.state().Deadline + 60; next.Deadline != want {
			h.t.Fatalf("live deadline: got %d, want %d (one-minute increment)", next.Deadline, want)
		}
	}
	h.tx = b.Ark.UnsignedTx
	h.known[h.tx.TxHash()] = h.tx
}
func (h *testHand) start(opening int64) {
	h.accept(h.c.InitialDeposit(h.funding(h.c.params.Stake + h.c.params.Bond)))
	h.accept(h.c.Player1FundingAndReveal(h.tx, h.funding(h.c.params.Stake+h.c.params.Bond), h.reveal(RevealOpponentHoles, Player1)))
	h.accept(h.c.Player2RevealAndOpening(h.tx, opening, h.funding(opening), h.reveal(RevealOpponentHoles, Player2)))
}
func (h *testHand) bet(action BettingAction) {
	s := h.state()
	a, o := wagers(s, s.Phase.Actor)
	contribution := int64(0)
	if action.Kind == Call {
		contribution = int64(o - a)
	}
	if action.Kind == RaiseTo {
		contribution = action.Amount - int64(a)
	}
	h.accept(h.c.Bet(h.tx, action, h.funding(contribution)))
}
func (h *testHand) board() {
	for i := 0; i < 2; i++ {
		s := h.state()
		h.accept(h.c.BoardReveal(h.tx, h.reveal(boardRevealKind(s.Phase.Street), s.Phase.Actor)))
	}
}
func (h *testHand) ownHoles() {
	for i := 0; i < 2; i++ {
		s := h.state()
		h.accept(h.c.ShowdownReveal(h.tx, h.reveal(RevealOwnHoles, s.Phase.Actor)))
	}
}
func (h *testHand) settle(submitter Player) {
	h.t.Helper()
	p1, err := merkel.Generate(context.Background(), [7]byte{0, 1, 4, 5, 6, 7, 8})
	if err != nil {
		h.t.Fatal(err)
	}
	p2, err := merkel.Generate(context.Background(), [7]byte{2, 3, 4, 5, 6, 7, 8})
	if err != nil {
		h.t.Fatal(err)
	}
	cards := DealtCards[byte]{HoleCards: PerPlayer[[2]byte]{[2]byte{0, 1}, [2]byte{2, 3}}, Flop: [3]byte{4, 5, 6}, Turn: 7, River: 8}
	previous := h.tx
	total := previous.TxOut[0].Value
	h.accept(h.c.Showdown(previous, submitter, ShowdownWitness{Cards: cards, RankProofs: PerPlayer[merkel.HandProof]{p1, p2}}))
	if len(h.tx.TxOut) != 4 || h.tx.TxOut[0].Value+h.tx.TxOut[1].Value != total {
		h.t.Fatal("terminal payout conservation")
	}
	if _, err := ReadState(h.tx); err == nil {
		h.t.Fatal("terminal still has poker state")
	}
}

func TestCompleteOrdinaryHandsRealEmulator(t *testing.T) {
	for _, opening := range []int64{0, 10} {
		t.Run(fmt.Sprintf("opening_%d", opening), func(t *testing.T) {
			h := newTestHand(t, nil)
			h.start(opening)
			if opening == 0 {
				h.bet(BettingAction{Kind: Check})
			} else {
				h.bet(BettingAction{Kind: Call})
			}
			for street := Flop; street <= River; street++ {
				s := h.state()
				if s.Phase != (Phase{Kind: BoardReveal, Actor: Player2, Street: street, Pass: FirstPass}) {
					t.Fatalf("next board: %+v", s.Phase)
				}
				h.board()
				s = h.state()
				if s.Phase != (Phase{Kind: Betting, Actor: Player2, Street: street}) {
					t.Fatalf("first revealer must open betting: %+v", s.Phase)
				}
				h.bet(BettingAction{Kind: Check})
				h.bet(BettingAction{Kind: Check})
			}
			if s := h.state(); s.Phase != (Phase{Kind: ShowdownReveal, Actor: Player2, Pass: FirstPass}) {
				t.Fatalf("showdown obligation: %+v", s.Phase)
			}
			h.ownHoles()
			if s := h.state(); s.Phase != (Phase{Kind: ShowdownEvaluation}) {
				t.Fatalf("evaluation: %+v", s.Phase)
			}
			h.settle(Player1)
		})
	}
}

func TestAllInPathsAndEveryProgramRealEmulator(t *testing.T) {
	covered := map[SpendingIdentity]int{}
	for street := PreFlop; street <= River; street++ {
		for _, bettor := range []Player{Player1, Player2} {
			t.Run(fmt.Sprintf("street_%d_bettor_%d", street, bettor), func(t *testing.T) {
				h := newTestHand(t, covered)
				// Reach pre-flop P1 ordinary betting, then visit every preceding board.
				h.start(10)
				if street > PreFlop {
					h.bet(BettingAction{Kind: Call})
					for current := Flop; current <= street; current++ {
						h.board()
						if current < street {
							h.bet(BettingAction{Kind: Check})
							h.bet(BettingAction{Kind: Check})
						}
					}
				}
				if h.state().Phase.Actor != bettor {
					if street == PreFlop {
						h.bet(BettingAction{Kind: RaiseTo, Amount: 20})
					} else {
						h.bet(BettingAction{Kind: Check})
					}
				}
				h.bet(BettingAction{Kind: RaiseTo, Amount: h.c.params.MaxWager})
				s := h.state()
				if s.Phase != (Phase{Kind: AllIn, Actor: otherPlayer(bettor), Street: street}) {
					t.Fatalf("all-in obligation: %+v", s.Phase)
				}
				beforeCall := h.tx
				// Both eligible terminal exits execute against an actual all-in predecessor.
				b, err := h.c.Concession(beforeCall)
				if err != nil {
					t.Fatal(err)
				}
				if err := executeKnownBundle(h.c, b, h.known); err != nil {
					t.Fatal(err)
				}
				covered[b.Spending[0].Path.Identity]++
				b, err = h.c.Timeout(beforeCall)
				if err != nil {
					t.Fatal(err)
				}
				if err := executeKnownBundle(h.c, b, h.known); err != nil {
					t.Fatal(err)
				}
				covered[b.Spending[0].Path.Identity]++
				a, _ := wagers(s, s.Phase.Actor)
				h.accept(h.c.AllInCall(h.tx, h.funding(h.c.params.MaxWager-int64(a)), h.reveal(remainingRevealKind(street), s.Phase.Actor)))
				s = h.state()
				if street == River {
					if s.Phase != (Phase{Kind: ShowdownReveal, Pass: SecondPass, Actor: bettor}) {
						t.Fatalf("river call phase: %+v", s.Phase)
					}
					h.accept(h.c.ShowdownReveal(h.tx, h.reveal(RevealOwnHoles, bettor)))
				} else {
					if s.Phase != (Phase{Kind: AllInReveal, Street: street + 1, Actor: bettor}) {
						t.Fatalf("remaining phase: %+v", s.Phase)
					}
					h.accept(h.c.AllInReveal(h.tx, h.reveal(remainingRevealKind(street), bettor)))
				}
				if s := h.state(); s.Wagers != (PerPlayer[uint64]{500, 500}) || s.Phase.Kind != ShowdownEvaluation {
					t.Fatalf("all-in final state: %+v", s)
				}
				h.settle(bettor)
			})
		}
	}
	// Ordinary pre-flop response by P2 and both showdown reveal programs.
	h := newTestHand(t, covered)
	h.start(10)
	h.bet(BettingAction{Kind: RaiseTo, Amount: 20})
	h.bet(BettingAction{Kind: Call})
	for street := Flop; street <= River; street++ {
		h.board()
		h.bet(BettingAction{Kind: Check})
		h.bet(BettingAction{Kind: Check})
	}
	h.ownHoles()
	h.settle(Player2)
	for _, identity := range spendingIdentities() {
		if covered[identity] == 0 {
			t.Errorf("unexecuted program path %+v", identity)
		}
	}
}
