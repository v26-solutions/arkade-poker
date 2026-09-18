//go:build uipreview

package ui

import (
	"context"
	"runtime"
	"strconv"
	"strings"

	"arkade-poker/go/internal/client"
	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	tea "charm.land/bubbletea/v2"
	"github.com/btcsuite/btcd/wire"
)

// Preview is an isolated, deterministic UI qualification harness. It has no
// wallet keys, service clients, storage or driver. Commands returned by the UI
// are never run, so reviewing a form cannot submit a financial action.
type Preview struct {
	inner       *Model
	scene, w, h int
}

const previewScenes = 23

func NewPreview() *Preview {
	p := &Preview{scene: 8, w: 120, h: 44}
	p.load()
	return p
}
func (p *Preview) Init() tea.Cmd  { return nil }
func (p *Preview) View() tea.View { return p.inner.View() }
func (p *Preview) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.w, p.h = msg.Width, msg.Height
	case tea.KeyPressMsg:
		if p.inner.modal == walletModal && msg.String() == "enter" {
			p.inner.input.Reset()
			p.inner.errorText = "Wallet import is disabled in the preview."
			return p, nil
		}
		if msg.String() == "f2" {
			p.scene = (p.scene + 1) % previewScenes
			p.load()
			return p, nil
		}
		if msg.String() == "f1" {
			p.scene = (p.scene + previewScenes - 1) % previewScenes
			p.load()
			return p, nil
		}
		if msg.String() == "ctrl+c" && p.inner.modal == exitModal {
			return p, tea.Quit
		}
	case tea.PasteMsg:
		if strings.HasPrefix(msg.Content, "preview:") {
			i, err := strconv.Atoi(strings.TrimPrefix(msg.Content, "preview:"))
			if err == nil && i >= 0 && i < previewScenes {
				p.scene = i
				p.load()
			}
			return p, nil
		}
	}
	p.inner.Update(msg)
	return p, nil
}

func (p *Preview) load() {
	m := New(context.Background(), Host{Browser: runtime.GOOS == "js"})
	m.width, m.height = p.w, p.h
	m.network = "PREVIEW"
	m.receive.Address = "tark1qqcp000000000000vvst"
	m.balance, m.balanceKnown = 125000, true
	m.session = &client.Session{Inputs: make(chan game.Input), NewGame: make(chan struct{})}
	m.snapshot = game.Snapshot{Role: covenant.Player1, Stage: game.StageBettingLocal,
		Terms:  game.Terms{Stake: 1000, Bond: 1000, MinBet: 500, MaxWager: 10000},
		State:  &covenant.State{Phase: covenant.Phase{Kind: covenant.Betting, Street: covenant.Turn, Actor: covenant.Player1}, Wagers: covenant.PerPlayer[uint64]{Player1: 1000, Player2: 1500}},
		Choice: &game.Choice{Allowed: []game.InputKind{game.Concede, game.Bet}, CanCall: true, CallAmount: 500, MinRaiseTo: 2000, MaxRaiseTo: 10000}}
	k := func(i byte) game.KnownCard { return game.KnownCard{Known: true, Index: i} }
	m.snapshot.Cards.HoleCards.Player1 = [2]game.KnownCard{k(51), k(36)}
	m.snapshot.Cards.Flop = [3]game.KnownCard{k(49), k(35), k(5)}
	m.snapshot.Cards.Turn = k(20)
	m.status = "Your turn / Wallet-funded wagers / [ESC] Menu"
	switch p.scene {
	case 0:
		m.session = nil
		m.receive.Address = ""
		m.snapshot = game.Snapshot{}
		m.status = "Add a funded wallet to begin"
	case 1:
		m.snapshot = game.Snapshot{Choice: &game.Choice{Allowed: []game.InputKind{game.StartSession, game.JoinSession}}}
		m.gameKey("c")
	case 2:
		m.snapshot = game.Snapshot{Choice: &game.Choice{Allowed: []game.InputKind{game.StartSession, game.JoinSession}}}
		m.gameKey("j")
	case 3:
		m.snapshot.State.Phase.Street = covenant.PreFlop
		m.snapshot.Cards.Flop = [3]game.KnownCard{}
		m.snapshot.Cards.Turn = game.KnownCard{}
	case 4:
		m.snapshot.State.Phase.Street = covenant.Flop
		m.snapshot.Cards.Turn = game.KnownCard{}
	case 5:
	case 6:
		m.openRaise()
	case 7:
		m.modal = allInModal
	case 8, 9, 10, 11, 12, 13:
		m.snapshot.Cards.River = k(10)
		m.snapshot.Cards.HoleCards.Player2 = [2]game.KnownCard{k(50), k(24)}
		m.snapshot.State.Phase = covenant.Phase{Kind: covenant.ShowdownEvaluation}
		m.snapshot.State.Wagers = covenant.PerPlayer[uint64]{Player1: 1500, Player2: 1500}
		m.snapshot.Choice = nil
		m.snapshot.Stage = game.StageFinished
		kind := game.Won
		winner := covenant.Player1
		if p.scene == 9 {
			kind = game.Lost
			winner = covenant.Player2
			m.snapshot.Cards.HoleCards.Player1, m.snapshot.Cards.HoleCards.Player2 = m.snapshot.Cards.HoleCards.Player2, m.snapshot.Cards.HoleCards.Player1
		}
		if p.scene == 10 {
			kind = game.Tied
			winner = 0
			m.snapshot.Cards.Flop = [3]game.KnownCard{k(8), k(9), k(10)}
			m.snapshot.Cards.Turn = k(11)
			m.snapshot.Cards.River = k(12)
		}
		tx := wire.NewMsgTx(3)
		mine, theirs := int64(6000), int64(1000)
		if kind == game.Lost {
			mine, theirs = theirs, mine
		}
		if kind == game.Tied {
			mine, theirs = 3500, 3500
		}
		tx.AddTxOut(wire.NewTxOut(mine, nil))
		tx.AddTxOut(wire.NewTxOut(theirs, nil))
		m.snapshot.Outcome = &game.Outcome{Kind: kind, Settlement: game.Settlement{Kind: game.SettlementShowdown, Winner: winner}, Transaction: tx, Payouts: covenant.PerPlayer[wire.OutPoint]{Player1: wire.OutPoint{Hash: tx.TxHash()}, Player2: wire.OutPoint{Hash: tx.TxHash(), Index: 1}}}
		if p.scene == 11 {
			m.modal = payoutModal
		}
		if p.scene == 12 {
			m.snapshot.Stage = game.StageAwaitTransactionAcceptance
			m.snapshot.Outcome = nil
			m.busy = true
			m.status = "Cards revealed / Payout awaiting acceptance"
		}
		if p.scene == 13 {
			m.snapshot.Cards.HoleCards.Player2 = [2]game.KnownCard{}
			m.snapshot.Outcome.Settlement.Kind = game.SettlementConcession
		}
	case 14:
		m.snapshot.Stage = game.StageBettingOpponent
		m.snapshot.Choice = nil
		m.status = "Opponent's turn"
	case 15:
		m.modal = menuModal
	case 16:
		m.snapshot.Cards.River = k(10)
		m.snapshot.State.Phase.Street = covenant.River
	case 17:
		m.snapshot.State = nil
		m.snapshot.Choice = nil
		m.shuffling = true
		m.status = "Preparing the encrypted deck"
	case 19:
		m.balance = 250
	case 20:
		m.snapshot = game.Snapshot{Choice: &game.Choice{Allowed: []game.InputKind{game.StartSession, game.JoinSession}}}
		m.balance = 25000
		m.gameKey("c")
		m.submitForm()
	case 18, 21, 22:
		// Public codec fixture; no wallet or live session is involved.
		inv, err := game.DecodeInvitation("arkpg1:iCeIJ_QDoI0GAAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh95vmZ--dy7rFWgYpXOhwsHApv82y3OKNlZ8oFbFvgXmCAhIiMkJSYnKCkqKywtLi8wMTIzNDU2Nzg5Ojs8PT4_DXdzczovL25vcy5sb2w4Wfq0YppFfA")
		if err != nil {
			panic(err)
		}
		m.snapshot = game.Snapshot{Stage: game.StageAwaitOpponentKeys, Role: covenant.Player1, Terms: inv.Terms, Invitation: &inv}
		m.clearPublic = [32]byte{1}
		m.host.AbortSetup = func(context.Context, [32]byte) (*client.Session, error) { return nil, nil }
		m.status = m.gameStatus()
		if p.scene != 18 {
			m.snapshot = game.Snapshot{Choice: &game.Choice{Allowed: []game.InputKind{game.JoinSession}}}
			m.modal, m.joinInvitation = joinConfirmModal, &inv
			m.balance = inv.Terms.Stake + inv.Terms.Bond
			if p.scene == 21 {
				m.balance /= 2
			} else {
				m.submit(game.Input{Kind: game.JoinSession, Invitation: &inv}, false)
			}
		}
	}
	p.inner = m
}
