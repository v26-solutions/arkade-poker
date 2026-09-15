package game

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"arkade-poker/go/internal/covenant"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

func (h *handHarness) fork() *handHarness {
	h.t.Helper()
	next := &handHarness{t: h.t, nonce: h.nonce, at: h.at, known: map[chainhash.Hash]*wire.MsgTx{}, paths: map[covenant.SpendingIdentity]int{}}
	for id, tx := range h.known {
		next.known[id] = tx
	}
	for i := range h.games {
		next.logs[i] = append([][]byte(nil), h.logs[i]...)
		next.games[i] = replayBytes(h.t, h.games[i].config, next.logs[i])
	}
	return next
}
func (h *handHarness) terminalBranches() {
	h.t.Helper()
	if h.games[0].hand == nil {
		return
	}
	state, _ := covenant.ReadState(h.games[0].hand.accepted.Transaction)
	actor, _ := state.Phase.RequiredActor()
	if actor == 0 {
		return
	}
	if h.games[int(other(actor))-1].actionPermitted(ActionTimeout) {
		branch := h.fork()
		branch.at = state.Deadline
		branch.action(int(other(actor))-1, Input{Kind: ClaimTimeout})
		for i, g := range branch.games {
			want := Won
			if i == int(actor)-1 {
				want = Lost
			}
			if g.outcome.Kind != want || g.outcome.Settlement.Kind != SettlementTimeout {
				h.t.Fatal("timeout beneficiary")
			}
		}
	}
	if h.games[int(actor)-1].actionPermitted(ActionConcession) {
		branch := h.fork()
		branch.action(int(actor)-1, Input{Kind: Concede})
		for i, g := range branch.games {
			want := Won
			if i == int(actor)-1 {
				want = Lost
			}
			if g.outcome.Kind != want || g.outcome.Settlement.Kind != SettlementConcession {
				h.t.Fatal("concession beneficiary")
			}
			before, err := h.games[i].Snapshot()
			if err != nil {
				h.t.Fatal(err)
			}
			after, err := g.Snapshot()
			if err != nil || after.State == nil || after.Cards != before.Cards || after.Choice != nil {
				h.t.Fatal("concession lost the table or exposed unrevealed cards", err)
			}
		}
	}
}
func TestOrdinaryConcessionsAndTimeoutsRealEmulator(t *testing.T) {
	h := newHandHarness(t)
	h.action(1, Input{Kind: Progress})
	h.terminalBranches()
	h.action(0, Input{Kind: Progress})
	h.private()
	h.terminalBranches()
	h.action(1, Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Check}})
	h.private()
	for {
		h.terminalBranches()
		state, _ := covenant.ReadState(h.games[0].hand.accepted.Transaction)
		switch state.Phase.Kind {
		case covenant.Betting:
			h.action(h.actor(), Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Check}})
		case covenant.BoardReveal:
			h.action(h.actor(), Input{Kind: Progress})
		case covenant.ShowdownReveal:
			h.action(h.actor(), Input{Kind: RevealShowdown})
		case covenant.ShowdownEvaluation:
			return
		default:
			t.Fatalf("unexpected phase %v", state.Phase)
		}
	}
}
func TestShortCapRaiseAndCompetingTimeout(t *testing.T) {
	h := newHandHarness(t)
	h.start(900)
	choice, err := h.games[0].handChoice()
	if err != nil || choice.CallAmount != 900 || choice.MinRaiseTo != 1000 || choice.MaxRaiseTo != 1000 {
		t.Fatal("short cap raise choice", choice, err)
	}
	if _, err := h.games[0].RequiredFunding(Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.RaiseTo, Amount: 999}}); err == nil {
		t.Fatal("non-cap short raise")
	}
	h.action(0, Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.RaiseTo, Amount: 1000}})
	h.terminalBranches()
	h.action(1, Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Call}})
	h.terminalBranches()
	h.action(0, Input{Kind: RevealShowdown})
	h.evaluateAndSettle(-1)

	// A service-accepted competing timeout supersedes already prepared and signed
	// local work. Neither local signatures nor intent is acceptance.
	h = newHandHarness(t)
	h.start(0)
	g := h.games[0]
	e, err := g.PrepareAction(context.Background(), nil, Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Check}}, covenant.Funding{}, h.at)
	if err != nil {
		t.Fatal(err)
	}
	h.record(0, e)
	saved, _ := g.PendingSpend()
	signed := saved.Prepared
	saved.Signed = &signed
	se := g.NewEvent(SpendSigned)
	se.Spend = &saved
	h.record(0, se)
	state, _ := covenant.ReadState(g.hand.accepted.Transaction)
	h.at = state.Deadline
	h.action(1, Input{Kind: ClaimTimeout})
	if g.prepared != nil || g.stage != StageFinished || g.outcome.Kind != Lost {
		t.Fatal("competing accepted spend did not supersede local work")
	}
	h.replay()
}
func mutateSuccessor(t *testing.T, a *AcceptedTransaction, change func(*covenant.State, *arkade.EmulatorEntry)) {
	t.Helper()
	state, err := covenant.ReadState(a.Transaction)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := arkade.FindEmulatorPacket(a.Transaction)
	if err != nil || len(entries) != 1 {
		t.Fatal(err)
	}
	change(state, &entries[0])
	ext, err := covenant.ActionExtension(state, entries...)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ext.TxOut()
	if err != nil {
		t.Fatal(err)
	}
	a.Transaction.TxOut[len(a.Transaction.TxOut)-2] = out
}
func TestAcceptedSpendEvidenceRejectionIsAtomic(t *testing.T) {
	h := newHandHarness(t)
	h.action(1, Input{Kind: Progress})
	g := h.games[0]
	input := Input{Kind: Progress}
	f := h.funding(0, testTerms.Stake+testTerms.Bond)
	prepared, err := g.PrepareAction(context.Background(), nil, input, f, h.at)
	if err != nil {
		t.Fatal(err)
	}
	built, err := g.buildAction(prepared.Spend.Action)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.execute(0, built); err != nil {
		t.Fatal("incomplete passing fixture", err)
	}
	original := g.NewEvent(SpendObserved)
	a := acceptedBuild(built)
	original.Accepted = &a
	original.ObservedAt = h.at
	encoded, err := EncodeEvent(original)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, want string
		mutate     func(*Event)
	}{
		{"checkpoint count", "checkpoint count", func(e *Event) { e.Accepted.Checkpoints = e.Accepted.Checkpoints[:1] }},
		{"checkpoint source", "covenant source continuity", func(e *Event) {
			cp := e.Accepted.Checkpoints[0]
			cp.TxIn[0].PreviousOutPoint.Index++
			e.Accepted.Transaction.TxIn[0].PreviousOutPoint.Hash = cp.TxHash()
		}},
		{"checkpoint source value", "covenant source continuity", func(e *Event) {
			cp := e.Accepted.Checkpoints[0]
			cp.TxOut[0].Value++
			e.Accepted.Transaction.TxIn[0].PreviousOutPoint.Hash = cp.TxHash()
			e.Accepted.Transaction.TxOut[0].Value++
		}},
		{"wrong checkpoint script", "checkpoint program or script", func(e *Event) {
			cp := e.Accepted.Checkpoints[0]
			cp.TxOut[0].PkScript[2] ^= 1
			e.Accepted.Transaction.TxIn[0].PreviousOutPoint.Hash = cp.TxHash()
		}},
		{"balance", "accepted transaction balance", func(e *Event) { e.Accepted.Transaction.TxOut[0].Value++ }},
		{"header version", "transaction layout", func(e *Event) { e.Accepted.Transaction.Version = 2 }},
		{"wrong phase", "successor phase", func(e *Event) {
			mutateSuccessor(t, e.Accepted, func(s *covenant.State, _ *arkade.EmulatorEntry) {
				s.Phase = covenant.Phase{Kind: covenant.Betting, Street: covenant.PreFlop, Actor: covenant.Player1}
			})
		}},
		{"wrong wager", "successor phase", func(e *Event) {
			mutateSuccessor(t, e.Accepted, func(s *covenant.State, _ *arkade.EmulatorEntry) { s.Wagers.Player1++ })
		}},
		{"wrong deadline", "successor phase", func(e *Event) {
			mutateSuccessor(t, e.Accepted, func(s *covenant.State, _ *arkade.EmulatorEntry) { s.Deadline++ })
		}},
		{"changed proof", "shuffle: invalid proof", func(e *Event) {
			mutateSuccessor(t, e.Accepted, func(_ *covenant.State, p *arkade.EmulatorEntry) { p.Witness[0][128] ^= 1 })
		}},
		{"extra proof", "new reveal proof count", func(e *Event) {
			mutateSuccessor(t, e.Accepted, func(_ *covenant.State, p *arkade.EmulatorEntry) {
				p.Witness = append(p.Witness, bytes.Clone(p.Witness[0]))
			})
		}},
		{"reordered proof", "shuffle: invalid proof", func(e *Event) {
			mutateSuccessor(t, e.Accepted, func(_ *covenant.State, p *arkade.EmulatorEntry) {
				p.Witness[0], p.Witness[1] = p.Witness[1], p.Witness[0]
			})
		}},
		{"wrong output value with balanced funding", "successor covenant value", func(e *Event) {
			e.Accepted.Transaction.TxOut[0].Value++
			cp := e.Accepted.Checkpoints[1]
			cp.TxOut[0].Value++
			e.Accepted.Transaction.TxIn[1].PreviousOutPoint.Hash = cp.TxHash()
		}},
		{"wrong output script", "successor covenant value", func(e *Event) { e.Accepted.Transaction.TxOut[0].PkScript[2] ^= 1 }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			candidate, err := DecodeEvent(encoded)
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(&candidate)
			before, _ := g.Snapshot()
			cursor, previous := g.nextEvent, g.previous
			err = g.Apply(candidate)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
			after, _ := g.Snapshot()
			if cursor != g.nextEvent || previous != g.previous || !reflect.DeepEqual(before, after) {
				t.Fatal("rejected evidence mutated reducer")
			}
		})
	}
	if err := g.Apply(original); err != nil {
		t.Fatal("prior rejection changed hidden state", err)
	}
}
func TestPreparedActionAdmissionAndSigningRecoveryRecords(t *testing.T) {
	h := newHandHarness(t)
	h.start(20)
	g := h.games[0]
	input := Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.RaiseTo, Amount: 40}}
	e, err := g.PrepareAction(context.Background(), nil, input, h.funding(0, 40), h.at)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := EncodeEvent(e)
	for _, tt := range []struct {
		name   string
		mutate func(*Event)
	}{
		{"different action", func(e *Event) { e.Spend.Action.Bet.Amount++ }},
		{"different funding", func(e *Event) { e.Spend.Action.Funding.Inputs[0].Vtxo.Amount++ }},
		{"wrong route", func(e *Event) { e.Spend.Route = 1 }},
		{"wrong source", func(e *Event) { e.Spend.Source.Index++ }},
		{"wrong wallet sources", func(e *Event) { e.Spend.WalletSources[0].Index++ }},
		{"wrong owner", func(e *Event) { e.Spend.Action.Funding.Inputs[0].Vtxo.Tapscript.RevealedScript[1] ^= 1 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bad, _ := DecodeEvent(encoded)
			tt.mutate(&bad)
			cursor := g.nextEvent
			if err := g.Apply(bad); err == nil {
				t.Fatal("changed prepared action accepted")
			}
			if g.nextEvent != cursor {
				t.Fatal("cursor changed")
			}
		})
	}
	h.record(0, e)
	s, _ := g.PendingSpend()
	s.Signed = &s.Prepared
	se := g.NewEvent(SpendSigned)
	se.Spend = &s
	h.record(0, se)
	attempt := g.NewEvent(SubmissionAttempted)
	attempt.Receipt = &SubmissionReceipt{}
	attempt.ObservedAt = 1000
	h.record(0, attempt)
	retry := g.NewEvent(SubmissionAttempted)
	retry.Receipt = &SubmissionReceipt{}
	retry.ObservedAt = 1001
	if err := g.Apply(retry); err == nil {
		t.Fatal("ordinary retry interval")
	}
	// The approved recovery rule allows an immediate exact retry after an explicit
	// unspent query. The future driver owns the once-per-attempt bound.
	retry.Receipt.Recovery = true
	h.record(0, retry)
	failure := g.NewEvent(SubmissionFailed)
	failure.FailureCode = "retry_failed"
	h.record(0, failure)
	if g.outcome != nil || g.prepared == nil || g.hand == nil {
		t.Fatal("retry failure discarded active game")
	}
	restored := replayBytes(t, h.games[0].config, h.logs[0])
	saved, _ := restored.PendingSpend()
	if saved.Signed == nil || !sameBundle(*saved.Signed, s.Prepared) {
		t.Fatal("failed recovery lost exact signed work")
	}
	if _, err := g.PrepareOpening(context.Background(), nil, AcceptedTransaction{}, 0); !errors.Is(err, ErrInput) {
		t.Fatal("wrong opening phase", err)
	}
	if _, err := g.Decide(Input{Kind: StartSession, Terms: testTerms, RelayURL: "ws://host"}); err == nil {
		t.Fatal("second game on active wallet")
	}
	_ = fmt.Sprintf("%v", failure) // Formatting must remain payload-redacted.
}

func malformedScriptSignature(t *testing.T, encoded string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(b[5:])
	for maps := 0; maps < 2; maps++ {
		for {
			n, err := wire.ReadVarInt(r, 0)
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				break
			}
			if n > uint64(r.Len()) {
				t.Fatal("key bound")
			}
			_, _ = r.Seek(int64(n), 1)
			n, err = wire.ReadVarInt(r, 0)
			if err != nil || n > uint64(r.Len()) {
				t.Fatal("value bound")
			}
			_, _ = r.Seek(int64(n), 1)
		}
	}
	offset := len(b) - r.Len() - 1
	data := append(bytes.Clone(b[:offset]), 1, 0x14, 1, 0xff)
	data = append(data, b[offset:]...)
	return base64.StdEncoding.EncodeToString(data)
}
func TestReplayDoesNotValidateTransactionSignatures(t *testing.T) {
	h := newHandHarness(t)
	h.start(0)
	g := h.games[0]
	e, err := g.PrepareAction(context.Background(), nil, Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Check}}, covenant.Funding{}, h.at)
	if err != nil {
		t.Fatal(err)
	}
	h.record(0, e)
	saved, _ := g.PendingSpend()
	signed := saved.Prepared
	signed.Ark = malformedScriptSignature(t, signed.Ark)
	signed.Checkpoints = append([]string(nil), signed.Checkpoints...)
	for i := range signed.Checkpoints {
		signed.Checkpoints[i] = malformedScriptSignature(t, signed.Checkpoints[i])
	}
	saved.Signed = &signed
	event := g.NewEvent(SpendSigned)
	event.Spend = &saved
	h.record(0, event)
	restored := replayBytes(t, h.games[0].config, h.logs[0])
	actual, err := restored.PendingSpend()
	if err != nil || actual.Signed == nil || !sameBundle(*actual.Signed, signed) {
		t.Fatal("replay validated or replaced signature fields", err)
	}
}

type unexpectedEntropy struct{}

func (unexpectedEntropy) Read([]byte) (int, error) { panic("entropy read before opening admission") }
func TestPrivateOpeningAndBoardVisibility(t *testing.T) {
	h := newHandHarness(t)
	h.action(1, Input{Kind: Progress})
	for _, g := range h.games {
		snap, _ := g.Snapshot()
		if snap.Cards != (covenant.DealtCards[KnownCard]{}) {
			t.Fatal("deposit disclosed cards")
		}
	}
	// P2 has no opponent opening yet. Invalid evidence must fail before generating
	// local private shares, even though the source is a valid accepted deposit.
	g2 := h.games[1]
	if _, err := g2.PrepareOpening(context.Background(), unexpectedEntropy{}, g2.hand.accepted, h.at); err == nil {
		t.Fatal("unfunded opening admitted")
	}
	h.action(0, Input{Kind: Progress})
	snap, _ := g2.Snapshot()
	if snap.Cards != (covenant.DealtCards[KnownCard]{}) {
		t.Fatal("received opening alone disclosed private holes")
	}
	e, err := g2.PrepareOpening(context.Background(), nil, g2.hand.accepted, g2.hand.observedAt)
	if err != nil {
		t.Fatal(err)
	}
	snap, _ = g2.Snapshot()
	if snap.Cards != (covenant.DealtCards[KnownCard]{}) {
		t.Fatal("unrecorded private shares changed snapshot")
	}
	h.record(1, e)
	snap, _ = g2.Snapshot()
	if !snap.Cards.HoleCards.Player2[0].Known || !snap.Cards.HoleCards.Player2[1].Known || snap.Cards.HoleCards.Player1[0].Known {
		t.Fatal("private opening entitlement")
	}
	h.action(1, Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Check}})
	g1 := h.games[0]
	snap, _ = g1.Snapshot()
	if snap.Cards.HoleCards.Player1[0].Known {
		t.Fatal("P1 skipped private opening event")
	}
	h.private()
	h.action(0, Input{Kind: Bet, Bet: covenant.BettingAction{Kind: covenant.Check}})
	h.action(h.actor(), Input{Kind: Progress})
	for _, g := range h.games {
		snap, _ := g.Snapshot()
		if snap.Cards.Flop != ([3]KnownCard{}) || snap.Cards.Turn.Known || snap.Cards.River.Known {
			t.Fatal("one board share disclosed a card")
		}
	}
	h.action(h.actor(), Input{Kind: Progress})
	for _, g := range h.games {
		snap, _ := g.Snapshot()
		for _, c := range snap.Cards.Flop {
			if !c.Known {
				t.Fatal("complete board remains hidden")
			}
		}
		if snap.Cards.Turn.Known || snap.Cards.River.Known {
			t.Fatal("future board disclosed")
		}
	}
}
