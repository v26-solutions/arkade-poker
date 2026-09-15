package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"arkade-poker/go/internal/client"
	"arkade-poker/go/internal/wallet"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func press(m *Model, code rune, mod tea.KeyMod) tea.Cmd {
	_, cmd := m.Update(tea.KeyPressMsg{Code: code, Mod: mod})
	return cmd
}

func TestNativeExitConfirmation(t *testing.T) {
	m := New(context.Background(), Host{})
	press(m, 'a', 0)
	if m.modal != walletModal {
		t.Fatal("wallet did not open")
	}
	m.input.SetValue("unfinished")
	press(m, 'c', tea.ModCtrl)
	if m.modal != exitModal || m.exitConfirm {
		t.Fatal("exit must default to Cancel")
	}
	press(m, 'c', tea.ModCtrl)
	if cmd := press(m, tea.KeyEnter, 0); cmd != nil || m.modal != walletModal {
		t.Fatal("repeated Ctrl+C bypassed Cancel")
	}
	if m.input.Value() != "unfinished" {
		t.Fatal("cancel did not restore prior UI")
	}
	press(m, 'c', tea.ModCtrl)
	press(m, tea.KeyEscape, 0)
	if m.modal != walletModal {
		t.Fatal("Escape did not dismiss exit modal")
	}
	press(m, 'c', tea.ModCtrl)
	press(m, tea.KeyRight, 0)
	cmd := press(m, tea.KeyEnter, 0)
	if cmd == nil {
		t.Fatal("confirmed exit did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("wrong exit command")
	}
}

func TestWalletModalPrivacyAndImport(t *testing.T) {
	m := New(context.Background(), Host{ConnectWallet: func(context.Context, *wallet.Key) (wallet.Receive, error) {
		return wallet.Receive{Address: "tark1test"}, nil
	}})
	press(m, 'a', 0)
	const entered = "123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0"
	m.Update(tea.PasteMsg{Content: entered})
	if strings.Contains(m.View().Content, entered) {
		t.Fatal("wallet input shown in plaintext")
	}
	cmd := press(m, tea.KeyEnter, 0)
	if cmd == nil || m.input.Value() != "" || m.modal != noModal {
		t.Fatal("successful import did not clear input")
	}
	m.Update(cmd())
	if strings.Contains(m.View().Content, "Add Wallet") || !strings.Contains(m.View().Content, "tark1test") {
		t.Fatal("address did not replace button")
	}
	m.key.Destroy()
}

func TestMnemonicModalImport(t *testing.T) {
	const phrase = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	for _, network := range []string{"bitcoin", "regtest", "mutinynet"} {
		t.Run(network, func(t *testing.T) {
			want, err := wallet.ParseKey(phrase, network)
			if err != nil {
				t.Fatal(err)
			}
			defer want.Destroy()
			m := New(t.Context(), Host{
				DiscoverNetwork: func(context.Context) (string, error) { return network, nil },
				ConnectWallet: func(_ context.Context, key *wallet.Key) (wallet.Receive, error) {
					if key.PublicKey() != want.PublicKey() {
						t.Fatal("modal ignored Arkd network")
					}
					return wallet.Receive{Address: "tark1test"}, nil
				},
			})
			press(m, 'a', 0)
			m.Update(tea.PasteMsg{Content: phrase})
			if strings.Contains(m.View().Content, "abandon") {
				t.Fatal("mnemonic shown in plaintext")
			}
			cmd := press(m, tea.KeyEnter, 0)
			if m.key != nil || cmd == nil || m.modal != walletModal {
				t.Fatal("import did not wait for network")
			}
			m.Update(cmd())
			cmd = press(m, tea.KeyEnter, 0)
			if cmd == nil || m.input.Value() != "" || m.modal != noModal {
				t.Fatal("mnemonic import did not clear input")
			}
			m.Update(cmd())
			if m.receive.Address == "" || m.errorText != "" {
				t.Fatal("mnemonic wallet did not connect")
			}
			m.key.Destroy()
		})
	}
}

func TestInvalidAndCancelledWallet(t *testing.T) {
	m := New(context.Background(), Host{})
	press(m, 'a', 0)
	m.input.SetValue("invalid-secret")
	press(m, tea.KeyEnter, 0)
	if m.key != nil || m.errorText == "" || strings.Contains(m.errorText, "invalid-secret") {
		t.Fatal("invalid ingress handling")
	}
	press(m, tea.KeyEscape, 0)
	if m.input.Value() != "" || m.modal != noModal {
		t.Fatal("cancel retained input")
	}
	m = New(context.Background(), Host{ConnectWallet: func(context.Context, *wallet.Key) (wallet.Receive, error) {
		return wallet.Receive{}, errors.New("offline")
	}})
	press(m, 'a', 0)
	m.input.SetValue(strings.Repeat("0", 63) + "1")
	cmd := press(m, tea.KeyEnter, 0)
	m.Update(cmd())
	if m.key != nil || m.connecting || !strings.Contains(m.View().Content, "Add Wallet") {
		t.Fatal("failed connection retained loaded state")
	}
}

func TestBrowserExitAndProgress(t *testing.T) {
	m := New(context.Background(), Host{Browser: true})
	for _, code := range []rune{'q', tea.KeyEscape} {
		if cmd := press(m, code, 0); cmd != nil {
			t.Fatal("unexpected exit action")
		}
	}
	press(m, 'c', tea.ModCtrl)
	if m.modal != noModal {
		t.Fatal("browser Ctrl+C opened exit")
	}
	m.Update(ProgressMsg{Shuffling: true})
	before := m.View().Content
	m.Update(tickMsg{})
	after := m.View().Content
	if before == after || !strings.Contains(after, "shuffling...") {
		t.Fatal("shuffle status did not animate")
	}
}

func TestShortAddressCopiesFullValue(t *testing.T) {
	const full = "tark1qr340xg400jtxat9hdd0ungyu6s05zjtdf85uj9smyzxshf98ndakzxfwqsmxu49gv0urp0werken2ush9yw243v0savv4swnjw9fuat0d4yfn"
	var copied string
	m := New(context.Background(), Host{CopyText: func(text string) error { copied = text; return nil }})
	m.receive.Address = full
	m.Update(balanceMsg{update: client.BalanceUpdate{Sats: 123456}})
	view := m.View().Content
	if strings.Contains(view, full) || strings.Contains(view, "Y: Copy") || (!strings.Contains(view, "tark1qr34...4yfn") || !strings.Contains(view, "AVAILABLE BALANCE 123,456 SATS")) {
		t.Fatal("incorrect shortened address")
	}
	if cmd := press(m, 'y', 0); cmd != nil {
		t.Fatal("unexpected address keyboard shortcut")
	}
	header := strings.Split(ansi.Strip(view), "\n")[2]
	addressX := lipgloss.Width(header[:strings.Index(header, "tark1qr34")])
	balanceX := lipgloss.Width(header[:strings.Index(header, "123,456")])
	if _, cmd := m.Update(tea.MouseClickMsg{X: balanceX, Y: 2, Button: tea.MouseLeft}); cmd != nil {
		t.Fatal("balance click copied address")
	}
	click := tea.MouseClickMsg{X: addressX, Y: 2, Button: tea.MouseLeft}
	_, cmd := m.Update(click)
	if cmd == nil {
		t.Fatal("copy button did not respond")
	}
	m.Update(cmd())
	if copied != full || !strings.Contains(m.View().Content, "Address copied") {
		t.Fatal("clipboard did not receive full address")
	}
	m.host.CopyText = func(string) error { return errors.New("denied") }
	_, cmd = m.Update(click)
	m.Update(cmd())
	if m.errorText == "" {
		t.Fatal("clipboard failure hidden")
	}
}

func TestWalletBalanceFormattingAndRefresh(t *testing.T) {
	balances := make(chan client.BalanceUpdate, 1)
	m := New(context.Background(), Host{})
	m.Update(connectedMsg{receive: wallet.Receive{Address: "tark1test"}, session: &client.Session{Balances: balances}})
	if !strings.Contains(m.walletHeader(), "... sats") {
		t.Fatal("unknown balance displayed as zero")
	}
	for sats, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", 2100000000000000: "2,100,000,000,000,000"} {
		balances <- client.BalanceUpdate{Sats: sats}
		m.Update(m.waitBalance()())
		if m.walletHeader() != "tark1test  "+want+" sats" {
			t.Fatal(m.walletHeader())
		}
	}
	m.Update(balanceMsg{update: client.BalanceUpdate{Err: errors.New("offline")}})
	if !strings.Contains(m.walletHeader(), "Balance unavailable") || m.stopped || m.errorText != "" {
		t.Fatal("balance failure affected game or showed stale amount")
	}
	m.Update(balanceMsg{update: client.BalanceUpdate{Sats: 4567}})
	if !strings.Contains(m.walletHeader(), "4,567 sats") {
		t.Fatal("balance did not recover")
	}
	close(balances)
	if _, cmd := m.Update(m.waitBalance()()); cmd != nil {
		t.Fatal("closed balance stream kept waiting")
	}
}
