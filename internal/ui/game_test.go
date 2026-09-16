package ui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"arkade-poker/go/internal/client"
	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/wallet"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/charmbracelet/x/ansi"
)

func playing() *Model {
	m := New(context.Background(), Host{})
	m.session = &client.Session{Inputs: make(chan game.Input), NewGame: make(chan struct{})}
	m.receive.Address = "tark1fixture"
	m.snapshot = game.Snapshot{Stage: game.StageBettingLocal, Role: covenant.Player2,
		Terms:  game.Terms{Stake: 1000, Bond: 1000, MinBet: 100, MaxWager: 2000},
		State:  &covenant.State{Phase: covenant.Phase{Kind: covenant.Betting, Actor: covenant.Player2, Street: covenant.Flop}, Wagers: covenant.PerPlayer[uint64]{Player1: 600, Player2: 400}},
		Choice: &game.Choice{Allowed: []game.InputKind{game.Bet, game.Concede}, CanCall: true, CallAmount: 200, MinRaiseTo: 800, MaxRaiseTo: 2000}}
	m.snapshot.Cards.HoleCards.Player1 = [2]game.KnownCard{{Known: true, Index: 0}, {Known: true, Index: 1}}
	m.snapshot.Cards.HoleCards.Player2 = [2]game.KnownCard{{Known: true, Index: 50}, {Known: true, Index: 51}}
	return m
}
func submitted(t *testing.T, m *Model, cmd tea.Cmd) game.Input {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case i := <-m.session.Inputs:
		m.Update(<-done)
		return i
	case <-time.After(time.Second):
		t.Fatal("driver input not sent")
	}
	return game.Input{}
}

func TestLegalActionsAndRaiseIncrement(t *testing.T) {
	m := playing()
	view := ansi.Strip(m.View().Content)
	for _, text := range []string{"CALL 200", "POT 3,000 SATS", "PLAYER 2", "WAGER 400", "IN THIS HAND 1,600 SATS", "REMAINING 1,600"} {
		if !strings.Contains(view, text) {
			t.Fatalf("missing real table projection %q", text)
		}
	}
	press(m, 'r', 0)
	m.fields[0].SetValue("199")
	if press(m, tea.KeyEnter, 0) != nil || m.modal != raiseModal {
		t.Fatal("out-of-range raise submitted")
	}
	m.fields[0].SetValue("600")
	i := submitted(t, m, press(m, tea.KeyEnter, 0))
	if i.Kind != game.Bet || i.Bet.Kind != covenant.RaiseTo || i.Bet.Amount != 1200 {
		t.Fatal("raise increment did not convert to cumulative target", i)
	}
	if len(m.actions()) != 0 {
		t.Fatal("duplicate command offered before transition")
	}
	m.driverUpdate(driverMsg{update: game.Update{Snapshot: m.snapshot}})
	if len(m.actions()) != 0 {
		t.Fatal("repeated old observation unlocked commands")
	}
	next := m.snapshot
	next.Stage = game.StageTransactionPrepared
	next.Choice = nil
	m.driverUpdate(driverMsg{update: game.Update{Snapshot: next}})
	if len(m.actions()) != 0 {
		t.Fatal("unadmitted betting controls")
	}
}

func TestDriverErrorCannotBecomeNewGame(t *testing.T) {
	m := playing()
	m.driverUpdate(driverMsg{update: game.Update{Err: errors.New("retry failed")}})
	if m.snapshot.State == nil || len(m.actions()) != 0 || !m.stopped {
		t.Fatal("failed game lost table/ownership")
	}
	if cmd := press(m, 'n', 0); cmd != nil {
		t.Fatal("retry failure exposed new game")
	}
	m = playing()
	m.snapshot.Outcome = &game.Outcome{Kind: game.Won}
	m.snapshot.Stage = game.StageFinished
	if a := m.actions(); len(a) != 1 || a[0].key != "n" {
		t.Fatal("terminal outcome did not release next-game UI")
	}
}

func TestCreateFormPasteAndExitPreserveInput(t *testing.T) {
	m := playing()
	m.snapshot = game.Snapshot{Stage: game.StageInit, Choice: &game.Choice{Allowed: []game.InputKind{game.StartSession, game.JoinSession}}}
	press(m, 'c', 0)
	if m.modal != createModal || len(m.fields) != 5 {
		t.Fatal("create form missing")
	}
	m.fields[0].SetValue("12000")
	press(m, 'c', tea.ModCtrl)
	press(m, tea.KeyEscape, 0)
	if m.modal != createModal || m.fields[0].Value() != "12000" {
		t.Fatal("exit cancellation lost form")
	}
	press(m, tea.KeyEscape, 0)
	press(m, 'j', 0)
	m.Update(tea.PasteMsg{Content: "not a token"})
	if press(m, tea.KeyEnter, 0) != nil || m.errorText == "" {
		t.Fatal("bad invitation accepted")
	}
	press(m, tea.KeyEscape, 0)
	if len(m.fields) != 0 {
		t.Fatal("cancel retained pasted input")
	}
}

func TestCreateFormDefaultsAndOverrides(t *testing.T) {
	for _, test := range []struct {
		name  string
		host  Host
		want  game.Terms
		relay string
	}{
		{"defaults", Host{}, game.Terms{Stake: 5000, Bond: 5000, MinBet: 500, MaxWager: 100000}, "wss://nos.lol"},
		{"overrides", Host{DefaultTerms: game.Terms{Stake: 7000, Bond: 8000, MinBet: 600, MaxWager: 120000}, RelayURL: "wss://relay.example"}, game.Terms{Stake: 7000, Bond: 8000, MinBet: 600, MaxWager: 120000}, "wss://relay.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := New(context.Background(), test.host)
			m.session = &client.Session{Inputs: make(chan game.Input)}
			m.snapshot = game.Snapshot{Stage: game.StageInit, Choice: &game.Choice{Allowed: []game.InputKind{game.StartSession}}}
			press(m, 'c', 0)
			if m.modal != createModal {
				t.Fatal("create form missing")
			}
			for i, value := range []int64{test.want.Stake, test.want.Bond, test.want.MinBet, test.want.MaxWager} {
				if m.fields[i].Value() != fmt.Sprint(value) {
					t.Fatalf("field %d: %q", i, m.fields[i].Value())
				}
			}
			if m.fields[4].Value() != test.relay {
				t.Fatal("relay default changed")
			}
			m.fields[0].SetValue("9000")
			input := submitted(t, m, m.submitForm())
			test.want.Stake = 9000
			if input.Kind != game.StartSession || input.Terms != test.want || input.RelayURL != test.relay {
				t.Fatalf("form values not submitted: %+v", input)
			}
		})
	}
}

func TestTableAndFormsFitMinimumGridAndActionsClick(t *testing.T) {
	m := playing()
	m.Update(tea.WindowSizeMsg{Width: 76, Height: 30})
	m.errorText = strings.Repeat("long service error ", 100)
	for _, setup := range []func(){func() {}, func() { m.openForm(createModal, "10000", "10000", "1000", "50000", "ws://localhost:7777") }, func() { m.openForm(raiseModal, "800") }} {
		setup()
		content := m.View().Content
		if lipgloss.Height(content) > 30 || lipgloss.Width(content) > 76 {
			t.Fatalf("layout overflows 76x30: %dx%d", lipgloss.Width(content), lipgloss.Height(content))
		}
	}
	m.modal = noModal
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	for y, line := range lines {
		if i := strings.Index(line, "[R] RAISE"); i >= 0 {
			m.Update(tea.MouseClickMsg{X: lipgloss.Width(line[:i]) + 2, Y: y, Button: tea.MouseLeft})
			if m.modal != raiseModal {
				t.Fatal("raise button did not click")
			}
			return
		}
	}
	t.Fatal("raise button missing")
}

func TestExitVisibleBelowMinimumGrid(t *testing.T) {
	m := New(context.Background(), Host{})
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 24})
	press(m, 'c', tea.ModCtrl)
	if !strings.Contains(m.View().Content, "[ Cancel ]") {
		t.Fatal("exit hidden behind resize warning")
	}
}

func invitation(t *testing.T, relay string) game.Invitation {
	t.Helper()
	key, err := wallet.ParseKey(fmt.Sprintf("%064x", 40), "")
	if err != nil {
		t.Fatal(err)
	}
	defer key.Destroy()
	server, _ := btcec.PrivKeyFromBytes([]byte{41})
	defer server.Zero()
	emulator, _ := btcec.PrivKeyFromBytes([]byte{42})
	defer emulator.Zero()
	delay := arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 5}
	receive, err := key.Receive(server.PubKey(), nil, delay, "regtest")
	if err != nil {
		t.Fatal(err)
	}
	cp, err := (&script.CSVMultisigClosure{MultisigClosure: script.MultisigClosure{PubKeys: []*btcec.PublicKey{server.PubKey()}}, Locktime: delay}).Script()
	if err != nil {
		t.Fatal(err)
	}
	cfg := game.Config{ArkdURL: "http://ark.invalid", EmulatorURL: "http://emulator.invalid", IndexerURL: "http://index.invalid",
		Wallet: wallet.Config{Network: "regtest", WalletPublicKey: key.PublicKey(), ArkSigningKey: [32]byte(schnorr.SerializePubKey(server.PubKey())), EmulatorSigningKey: [32]byte(schnorr.SerializePubKey(emulator.PubKey())), CheckpointScript: cp, Receive: receive, OutputPolicy: wallet.OutputPolicy{MinAmount: 330, MaxAmount: 21_000_000 * 100_000_000}}}
	inv, secret, err := game.CreateInvitation(context.Background(), nil, cfg, game.Terms{Stake: 1000, Bond: 1000, MinBet: 100, MaxWager: 1000}, relay)
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	return inv
}

func TestInvitationReviewAndCopyPreserveCompletePayload(t *testing.T) {
	inv := invitation(t, "ws://localhost:7777/"+strings.Repeat("path", 490))
	token, err := game.EncodeInvitation(inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) <= 2048 {
		t.Fatal("fixture must exercise long valid invitation")
	}
	m := playing()
	m.Update(tea.WindowSizeMsg{Width: 76, Height: 30})
	m.snapshot = game.Snapshot{Stage: game.StageInit, Choice: &game.Choice{Allowed: []game.InputKind{game.StartSession, game.JoinSession}}}
	press(m, 'j', 0)
	m.Update(tea.PasteMsg{Content: token})
	press(m, tea.KeyEnter, 0)
	if m.modal != joinConfirmModal || !reflect.DeepEqual(m.joinInvitation, &inv) {
		t.Fatal("valid pasted invitation changed or was rejected")
	}
	if content := m.View().Content; lipgloss.Width(content) > 76 || lipgloss.Height(content) > 30 {
		t.Fatal("invitation review overflowed minimum terminal size")
	}
	view := m.View().Content
	if !strings.Contains(view, "DEPOSIT 2,000 SATS PER PLAYER") {
		t.Fatal("join confirmation did not show the stake plus bond deposit")
	}
	press(m, tea.KeyEnd, 0)
	m.Update(tea.PasteMsg{Content: "changed relay"})
	if m.View().Content != view || !reflect.DeepEqual(m.joinInvitation, &inv) {
		t.Fatal("read-only invitation review was modified")
	}
	i := submitted(t, m, press(m, tea.KeyEnter, 0))
	if i.Kind != game.JoinSession || !reflect.DeepEqual(i.Invitation, &inv) {
		t.Fatal("confirmed join changed invitation")
	}
	m.busy = false
	m.snapshot = game.Snapshot{Stage: game.StageSessionPrepared, Invitation: &inv, Terms: inv.Terms, Role: covenant.Player1}
	var copied string
	m.host.CopyText = func(text string) error { copied = text; return nil }
	press(m, 't', 0)
	if content := m.View().Content; lipgloss.Width(content) > 76 || lipgloss.Height(content) > 30 {
		t.Fatal("invitation preview overflowed minimum terminal size")
	}
	cmd := press(m, 'y', 0)
	if cmd == nil {
		t.Fatal("missing token copy")
	}
	m.Update(cmd())
	if copied != token {
		t.Fatal("clipboard received a truncated invitation")
	}
}
