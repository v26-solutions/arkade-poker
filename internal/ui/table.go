package ui

import (
	"fmt"
	"strings"

	"arkade-poker/go/internal/game"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func termsText(t game.Terms) string {
	return fmt.Sprintf("STAKE %s SATS   /   BOND %s SATS\nMINIMUM BET %s   /   MAXIMUM WAGER %s", formatSats(t.Stake), formatSats(t.Bond), formatSats(t.MinBet), formatSats(t.MaxWager))
}

func (m *Model) modalBody() string {
	switch m.modal {
	case walletModal:
		return "ADD WALLET\n\nEnter nsec, a hexadecimal key or a BIP39 mnemonic.\n\n" + m.input.View() + "\n\nEnter: Import    Escape: Cancel"
	case clearGameModal:
		buttons := "[ Cancel ]    Confirm clear"
		if m.clearConfirm {
			buttons = "Cancel    [ Confirm clear ]"
		}
		walletStatus := ""
		if m.key != nil {
			walletStatus = "\nYour wallet will stay loaded."
		}
		return fmt.Sprintf("CLEAR SAVED GAME?\n\nRemove all saved games for wallet %x…%x\nfrom this device?\n\nThis deletes recovery data and does not refund funds in a hand.%s\n\n%s\n\n←/→: Select    Enter: Activate    Escape: Cancel", m.clearPublic[:4], m.clearPublic[28:], walletStatus, buttons)
	case abortSetupModal:
		return "ABORT SETUP?\n\nStop setup and delete this wallet's saved game files\nfrom this device. Your wallet will stay loaded.\n\nYour opponent must abort on their device too.\nCreate a new invitation to try again.\n\nIf covenant funding has begun, the saved game is retained.\n\n←/→: Select    Enter: Activate    Escape: Cancel"
	case exitModal:
		buttons := "[ Cancel ]    Confirm exit"
		if m.exitConfirm {
			buttons = "Cancel    [ Confirm exit ]"
		}
		return "LEAVE POKER?\n\nYour saved session will remain on this device.\nGame deadlines continue while you are away.\n\n" + buttons + "\n\n←/→: Select    Enter: Activate    Escape: Cancel"
	case createModal:
		rows := make([]string, 0, len(m.fields))
		labelStyle := textStyle.Width(15).Align(lipgloss.Left)
		for i, label := range []string{"Stake", "Bond", "Minimum bet", "Maximum wager", "Nostr relay"} {
			rows = append(rows, labelStyle.Render(label)+m.fields[i].View())
		}
		// Pad rows as one block so the surrounding centered panel preserves
		// the label and input columns, including when focus changes.
		form := lipgloss.JoinVertical(lipgloss.Left, rows...)
		return "CREATE SESSION\n\nAmounts are per player, in satoshis.\n\n" + form +
			"\n\nTab: Next field   Enter: Create   Escape: Cancel"
	case joinModal:
		return "JOIN SESSION\n\nPaste a Go poker invitation.\n\n" + m.fields[0].View() + "\n\nEnter: Review terms    Escape: Cancel"
	case joinConfirmModal:
		if m.joinInvitation == nil {
			return "Invitation unavailable"
		}
		t := m.joinInvitation.Terms
		rows := []string{}
		for _, param := range []struct{ label, value string }{
			{"Stake:", formatSats(t.Stake) + " sats"},
			{"Bond:", formatSats(t.Bond) + " sats"},
			{"Minimum bet:", formatSats(t.MinBet) + " sats"},
			{"Maximum wager:", formatSats(t.MaxWager) + " sats"},
			{"Relay:", m.joinInvitation.RelayURL},
		} {
			row := fmt.Sprintf("%14s  %s", param.label, param.value)
			rows = append(rows, ansi.Truncate(row, max(1, min(68, m.width-12)), "…"))
		}
		params := lipgloss.JoinVertical(lipgloss.Left, rows...)
		return "JOIN THIS GAME?\n\n" + params + fmt.Sprintf("\n\nPress Enter to deposit %s sats and join\nEscape to cancel", formatSats(t.Stake+t.Bond))
	case tokenModal:
		if m.snapshot.Invitation == nil {
			return "Invitation unavailable"
		}
		token, err := game.EncodeInvitation(*m.snapshot.Invitation)
		if err != nil {
			return "Invitation unavailable"
		}
		rows := strings.Split(wrapToken(token, max(10, min(68, m.width-12))), "\n")
		limit := max(3, m.height-22)
		if len(rows) > limit {
			rows = rows[:limit]
			rows[limit-1] = "… (copy for the complete invitation)"
		}
		return "SESSION INVITATION\n\nShare this invitation with your opponent.\n\n" + strings.Join(rows, "\n") + "\n\nY / Enter: Copy invitation    Escape: Close"
	case raiseModal:
		r, ok := m.raiseRange()
		if !ok {
			return "Raise unavailable. Escape: Close"
		}
		preview := "Enter a raise amount within the displayed range."
		if n, err := amount(m.fields[0].Value()); err == nil && n >= r.min && n <= r.max {
			target := r.theirs + n
			preview = fmt.Sprintf("Call %s + raise %s = add %s sats\nYour total bet after this move: %s sats",
				formatSats(r.theirs-r.mine), formatSats(n), formatSats(target-r.mine), formatSats(target))
		}
		return fmt.Sprintf("RAISE BY\n\nAmount above opponent's bet, in sats.\nMinimum %s   Maximum %s\n\n%s\nUp/Down: Adjust by %s sats\n\n%s\n\nEnter: Raise    Escape: Cancel",
			formatSats(r.min), formatSats(r.max), m.fields[0].View(), formatSats(m.snapshot.Terms.MinBet), preview)
	}
	return ""
}
func wrapToken(text string, width int) string {
	var rows []string
	for len(text) > width {
		rows = append(rows, text[:width])
		text = text[width:]
	}
	return strings.Join(append(rows, text), "\n")
}
