package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m *Model) dialog(f *screen) {
	// Opaque, dimmed table backdrop. Discard underlying hit regions so a click
	// outside the dialog can never submit an obscured betting action.
	base := f.content()
	f.layers = []*lipgloss.Layer{lipgloss.NewLayer(dimStyle.Render(ansi.Strip(base)))}
	f.hits = nil
	if m.helpOpen {
		m.helpDialog(f)
		return
	}
	title, body, primary := "", "", ""
	if m.modal == setupWarningModal {
		title, body, primary = "ALL-IN BALANCE WARNING", m.setupWarning(), "[ENTER] CREATE ANYWAY"
		if m.pendingSetup != nil && m.pendingSetup.Invitation != nil {
			primary = "[ENTER] JOIN ANYWAY"
		}
	} else if m.modal == allInModal {
		title, primary = "ALL IN?", "[ENTER] ALL IN"
		if r, ok := m.raiseRange(); ok {
			target := r.theirs + r.max
			body = fmt.Sprintf("Add %s sats from your wallet.\n\nYour total wager: %s sats\nAlready wagered:  %s sats\n\nThis reaches the agreed wager limit for this hand.", formatSats(target-r.mine), formatSats(target), formatSats(r.mine))
		} else {
			body = "All in is no longer available."
		}
	} else if m.modal == payoutModal {
		title = "PAYOUT DETAILS"
		body = m.payoutDetails()
	} else if m.modal == menuModal {
		title, primary = "GAME MENU", "[ENTER] BACK TO TABLE"
		body = "Your session is saved on this device.\nGame deadlines continue while this menu is open."
		if m.snapshot.Terms.Stake > 0 {
			body += "\n\n" + termsText(m.snapshot.Terms)
		}
	} else {
		full := m.modalBody()
		parts := strings.SplitN(full, "\n\n", 2)
		title = parts[0]
		if len(parts) > 1 {
			body = parts[1]
		}
		// The shared form body remains useful to textual hosts/tests; its help
		// paragraph is replaced here by actual full-width controls.
		if i := strings.LastIndex(body, "\n\n"); i >= 0 {
			tail := body[i+2:]
			if strings.Contains(tail, "Escape:") || strings.Contains(tail, "Escape to") {
				body = body[:i]
			}
		}
		switch m.modal {
		case walletModal:
			primary = "[ENTER] IMPORT WALLET"
		case createModal:
			primary = "[ENTER] CREATE SESSION"
			if input, errText := m.createInput(); errText == "" {
				body += fmt.Sprintf("\n\nDEPOSIT %s SATS PER PLAYER", formatSats(m.inputFunding(input)))
			}
		case joinModal:
			primary = "[ENTER] REVIEW TERMS"
		case joinConfirmModal:
			primary = "[ENTER] DEPOSIT & JOIN"
			if m.joinInvitation != nil {
				body += fmt.Sprintf("\n\nDEPOSIT %s SATS PER PLAYER", formatSats(m.joinInvitation.Terms.Stake+m.joinInvitation.Terms.Bond))
			}
		case raiseModal:
			primary = "[ENTER] RAISE"
		case exitModal:
			primary = "Confirm exit"
			body = strings.Split(body, "\n\n[ Cancel ]")[0]
			body = strings.Split(body, "\n\nCancel")[0]
		case clearGameModal:
			primary = "Confirm clear"
			body = strings.Split(body, "\n\n[ Cancel ]")[0]
			body = strings.Split(body, "\n\nCancel")[0]
		case abortSetupModal:
			primary = "Confirm abort"
		}
	}
	w := min(82, f.w-4)
	if f.w < 30 {
		f.put(0, 0, textStyle.Render(fit(title+"\nEscape: Cancel", f.w, f.h, false)))
		return
	}
	inner := w - 6
	// Wrap prose without wrapping ANSI input/cursor sequences. Inputs already
	// have a bounded viewport; overflow on those rows is safely clipped.
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "\x1b") {
			lines = append(lines, ansi.Truncate(line, inner, "…"))
		} else {
			lines = append(lines, strings.Split(ansi.Wrap(line, inner, " "), "\n")...)
		}
	}
	maxH := max(10, f.h-9)
	if f.w < 76 || f.h < 30 {
		maxH = f.h - 3
	}
	// Keep error feedback and both controls visible, even on a small grid.
	limit := max(1, maxH-7)
	funding := m.fundingStatus(m.modalFunding())
	if funding != "" {
		limit -= 2
	}
	if m.errorText != "" {
		limit -= 2
	}
	if len(lines) > limit {
		lines = lines[:max(1, limit)]
	}
	if funding != "" {
		lines = append(lines, "", ansi.Truncate(funding, inner, "…"))
	}
	if m.errorText != "" {
		lines = append(lines, "", ansi.Truncate("ERROR / "+clean(m.errorText), inner, "…"))
	}
	h := len(lines) + 7
	x, y := (f.w-w)/2, max(0, (f.h-8-h)/2)
	if f.h < 30 {
		y = max(0, (f.h-3-h)/2)
	}
	f.put(x, y, panel(title, w, h))
	f.put(x+3, y+2, textStyle.Render(fit(strings.Join(lines, "\n"), inner, len(lines), false)))
	if o := m.snapshot.Outcome; m.modal == payoutModal && o != nil && o.Transaction != nil {
		txid := o.Transaction.TxHash().String()
		for i, line := range lines {
			if line == txid {
				f.put(x+3, y+2+i, textStyle.Underline(true).Render(txid))
				f.region("payout-copy", x+3, y+2+i, len(txid), 1)
			}
		}
	}
	cancel := "[ESC] CANCEL"
	if m.modal == setupWarningModal {
		cancel = "[ESC] BACK"
	}
	if m.modal == payoutModal || m.modal == menuModal {
		cancel = "[ESC] CLOSE"
	}
	selected := true
	if m.modal == exitModal {
		selected = m.exitConfirm
		cancel = "Cancel"
		if !selected {
			cancel = "[ Cancel ]"
		}
	}
	if m.modal == clearGameModal || m.modal == abortSetupModal {
		selected = m.clearConfirm
		cancel = "Cancel"
		if !selected {
			cancel = "[ Cancel ]"
		}
	}
	buttons := []screenButton{{id: "cancel", label: cancel, selected: !selected}}
	if primary != "" {
		buttons = append(buttons, screenButton{id: "confirm", label: primary, selected: selected, disabled: funding != ""})
	} else {
		buttons[0].selected = true
	}
	if m.modal == menuModal {
		buttons = []screenButton{{id: "confirm", label: primary, selected: true}}
		if m.canAbortSetup() {
			buttons = append(buttons, screenButton{id: "abort", label: "[B] ABORT SETUP"})
		}
		if m.canClearGame() {
			buttons = append(buttons, screenButton{id: "clear", label: "[X] CLEAR SAVED GAME"})
		}
		if !m.host.Browser {
			buttons = append(buttons, screenButton{id: "menu-exit", label: "[Q] LEAVE"})
		}
	}
	f.buttons(buttons, x+2, y+h-4, w-4)
	// Each field's entire input viewport can receive focus.
	if m.modal == createModal {
		for i := range m.fields {
			f.region(fmt.Sprintf("field:%d", i), x+18, y+4+i, max(1, w-21), 1)
		}
	}
}

func (m *Model) payoutDetails() string {
	o := m.snapshot.Outcome
	if o == nil || o.Transaction == nil {
		return "No accepted payout transaction is available."
	}
	body := "ACCEPTED PAYOUT TRANSACTION / CLICK ID TO COPY\n" + o.Transaction.TxHash().String()
	if value, ok := m.payout(); ok {
		mine, _ := m.wagers()
		t := m.snapshot.Terms
		body += fmt.Sprintf("\n\nYour payout: %s sats\nYour stake:  %s sats\nYour bond:   %s sats\nYour wagers: %s sats\nNet result:  %s sats", formatSats(value), formatSats(t.Stake), formatSats(t.Bond), formatSats(int64(mine)), formatSats(value-t.Stake-t.Bond-int64(mine)))
	}
	return body
}
