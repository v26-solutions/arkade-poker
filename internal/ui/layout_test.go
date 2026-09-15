package ui

import (
	"strings"
	"testing"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/btcsuite/btcd/wire"
	"github.com/charmbracelet/x/ansi"
)

func completedModel() *Model {
	m := playing()
	m.snapshot.Stage = game.StageFinished
	m.snapshot.State.Phase = covenant.Phase{Kind: covenant.ShowdownEvaluation}
	m.snapshot.Cards.HoleCards.Player1 = [2]game.KnownCard{{Known: true, Index: 50}, {Known: true, Index: 24}}
	m.snapshot.Cards.HoleCards.Player2 = [2]game.KnownCard{{Known: true, Index: 51}, {Known: true, Index: 36}}
	m.snapshot.Cards.Flop = [3]game.KnownCard{{Known: true, Index: 49}, {Known: true, Index: 35}, {Known: true, Index: 5}}
	m.snapshot.Cards.Turn = game.KnownCard{Known: true, Index: 20}
	m.snapshot.Cards.River = game.KnownCard{Known: true, Index: 10}
	tx := wire.NewMsgTx(3)
	tx.AddTxOut(wire.NewTxOut(1000, nil))
	tx.AddTxOut(wire.NewTxOut(4000, nil))
	m.snapshot.Outcome = &game.Outcome{Kind: game.Won, Settlement: game.Settlement{Kind: game.SettlementShowdown, Winner: covenant.Player2}, Transaction: tx, Payouts: covenant.PerPlayer[wire.OutPoint]{Player1: wire.OutPoint{Hash: tx.TxHash()}, Player2: wire.OutPoint{Hash: tx.TxHash(), Index: 1}}}
	m.snapshot.Choice = nil
	return m
}

func TestShowdownTableAndPayoutDialog(t *testing.T) {
	m := completedModel()
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"BOARD / SHOWDOWN", "OPPONENT / REVEALED", "THREE OF A KIND · QUEENS", "YOU WIN", "[N] NEW GAME", "[P] PAYOUT DETAILS", "PAYOUT 4,000", "NET +1,600"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q\n%s", want, view)
		}
	}
	press(m, 'p', 0)
	if m.modal != payoutModal || !strings.Contains(ansi.Strip(m.View().Content), m.snapshot.Outcome.Transaction.TxHash().String()) {
		t.Fatal("accepted payout details missing")
	}
	press(m, tea.KeyEscape, 0)
	if strings.Split(ansi.Strip(m.View().Content), "HAND COMPLETE")[0] != strings.Split(view, "HAND COMPLETE")[0] {
		t.Fatal("closing details changed the table")
	}
}

func TestButtonBordersClickAndModalBlocksTable(t *testing.T) {
	m := playing()
	for _, region := range m.layout().hits {
		if region.id != "action:r" {
			continue
		}
		// Click the corner, not the text, to exercise the entire hit rectangle.
		m.Update(tea.MouseClickMsg{X: region.rect.Min.X, Y: region.rect.Min.Y, Button: tea.MouseLeft})
		if m.modal != raiseModal {
			t.Fatal("button border not clickable")
		}
		for _, r := range m.layout().hits {
			if strings.HasPrefix(r.id, "action:") {
				t.Fatal("obscured action clickable")
			}
		}
		return
	}
	t.Fatal("raise region missing")
}

func TestAllInConfirmationAndStalePosition(t *testing.T) {
	m := playing()
	if cmd := press(m, 'a', 0); cmd != nil || m.modal != allInModal || m.busy {
		t.Fatal("all in submitted without review")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "Add 1,600 sats") {
		t.Fatal("incorrect wallet contribution")
	}
	i := submitted(t, m, press(m, tea.KeyEnter, 0))
	if i.Bet.Kind != covenant.RaiseTo || i.Bet.Amount != 2000 {
		t.Fatal("all in did not use driver limit")
	}
	m = playing()
	press(m, 'a', 0)
	next := m.snapshot
	next.Stage = game.StageBettingOpponent
	next.Choice = nil
	m.driverUpdate(driverMsg{update: game.Update{Snapshot: next}})
	if m.modal != noModal || press(m, tea.KeyEnter, 0) != nil {
		t.Fatal("stale all in remained actionable")
	}
	// An exit overlay must not restore a stale confirmation underneath it.
	m = playing()
	press(m, 'a', 0)
	press(m, 'c', tea.ModCtrl)
	m.driverUpdate(driverMsg{update: game.Update{Snapshot: next}})
	press(m, tea.KeyEscape, 0)
	if m.modal != noModal {
		t.Fatal("exit cancellation restored stale all in")
	}
}

func TestLayoutBoundsAndResultPrivacy(t *testing.T) {
	for _, size := range [][2]int{{76, 30}, {80, 32}, {120, 40}, {160, 50}} {
		for _, kind := range []modal{noModal, raiseModal, allInModal, payoutModal, menuModal, exitModal, clearGameModal} {
			m := completedModel()
			m.width, m.height = size[0], size[1]
			if kind == raiseModal || kind == allInModal {
				m = playing()
				m.width, m.height = size[0], size[1]
				m.openRaise()
			}
			m.modal = kind
			v := m.View().Content
			if lipgloss.Width(v) != size[0] || lipgloss.Height(v) != size[1] {
				t.Fatalf("size %v modal %d: %dx%d", size, kind, lipgloss.Width(v), lipgloss.Height(v))
			}
		}
	}
	m := completedModel()
	m.snapshot.Outcome.Kind = game.Lost
	m.snapshot.Outcome.Settlement.Kind = game.SettlementConcession
	m.snapshot.Cards.HoleCards.Player1 = [2]game.KnownCard{}
	v := ansi.Strip(m.View().Content)
	if strings.Contains(v, "OPPONENT / REVEALED") || !strings.Contains(v, "OPPONENT / HIDDEN") || !strings.Contains(v, "YOU LOSE") {
		t.Fatal("folded result fabricated reveal")
	}
}

func TestNegativeSatsFormatting(t *testing.T) {
	for v, want := range map[int64]string{-100: "-100", -1000: "-1,000", -1234567: "-1,234,567"} {
		if got := formatSats(v); got != want {
			t.Fatalf("%d: %s", v, got)
		}
	}
}
