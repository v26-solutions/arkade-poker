package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const helpInstructions = `GET STARTED
[A] Add a wallet using an nsec, hex key or BIP39 mnemonic.
Click its address to copy it, then fund it on the shown network.

[C] Create a session and share its invitation with your opponent,
or [J] Join with their invitation and review the terms.

PLAY A HAND
Each player deposits a stake and bond. Bets use your wallet balance.
Click an action, press its letter, or use arrows then Enter.
Raise adds the entered amount above your opponent's total wager.
All in reaches the agreed maximum wager for the hand.
At the end, [P] shows payout details and [N] starts a new game.

CONTROLS & DEADLINES
Tab moves between form fields. Esc closes a dialog or opens the menu.
Game deadlines keep running while Help or any other dialog is open.
Watch the countdown: a timeout can forfeit your stake, wagers and bond.

RESUME & TROUBLESHOOT
Resume saved play on this device with the same wallet and network.
If setup stalls before funding, [B] Abort setup when offered;
both players must abort and use a new invitation.
Clearing saved games removes recovery data; it does not refund funds.
Copy Logs above copies recent diagnostic logs with secrets redacted.`

func (m *Model) toggleHelp() tea.Cmd {
	m.helpOpen = !m.helpOpen
	if m.helpOpen {
		m.helpScroll = 0
	} else if m.modal == walletModal {
		return m.input.Focus()
	} else if len(m.fields) > 0 && (m.modal == createModal || m.modal == joinModal || m.modal == raiseModal) {
		return m.fields[m.field].Focus()
	}
	return nil
}

func (m *Model) helpWidth() int { return max(1, min(82, m.width-4)) }

func (m *Model) helpLines() []string {
	return strings.Split(ansi.Wrap(helpInstructions, max(1, m.helpWidth()-6), " "), "\n")
}

func (m *Model) helpPageSize() int {
	// Match the other dialogs, leaving the action countdown and status visible.
	height := m.height - 9
	if m.width < 76 || m.height < 30 {
		height = m.height - 3
	}
	return max(1, height-8)
}

func (m *Model) scrollHelp(delta int) {
	m.helpScroll = max(0, min(m.helpScroll+delta, len(m.helpLines())-m.helpPageSize()))
}

func (m *Model) helpDialog(f *screen) {
	lines := m.helpLines()
	page := min(m.helpPageSize(), len(lines))
	start := min(m.helpScroll, len(lines)-page)
	visible := strings.Join(lines[start:start+page], "\n")
	notice := ""
	if m.copyingLogs {
		notice = "Copying logs..."
	} else if m.frame < m.logNoticeUntil {
		notice = m.logNotice
	}
	const copyLabel = "[L] Copy Logs"
	const closeLabel = "[ESC] Close"
	if f.w < 30 || f.h < 12 {
		// Keep the controls usable even below the game's minimum grid size.
		f.put(0, 0, textStyle.Render(fit("HELP\n"+copyLabel+"\n"+notice+"\n"+visible, f.w, max(1, f.h-4), false)))
		f.region("logs", 0, 1, min(len(copyLabel), f.w), 1)
		y := max(0, f.h-4)
		f.put(0, y, textStyle.Render(ansi.Truncate(closeLabel, f.w, "…")))
		f.region("help-close", 0, y, min(len(closeLabel), f.w), 1)
		return
	}
	w, h := m.helpWidth(), page+8
	x, y := (f.w-w)/2, max(0, (f.h-8-h)/2)
	if f.w < 76 || f.h < 30 {
		y = max(0, (f.h-3-h)/2)
	}
	f.put(x, y, panel("HELP", w, h))
	f.put(x+3, y+2, textStyle.Render(copyLabel))
	f.region("logs", x+3, y+2, len(copyLabel), 1)
	f.put(x+3, y+3, dimStyle.Render(ansi.Truncate(notice, w-6, "…")))
	f.put(x+3, y+5, textStyle.Render(visible))
	if page < len(lines) {
		const up, down = "[↑] Up", "[↓] Down"
		hint := fmt.Sprintf("%s  %s  %d-%d/%d", up, down, start+1, start+page, len(lines))
		f.put(x+3, y+h-3, dimStyle.Render(ansi.Truncate(hint, w-6, "…")))
		f.region("help-up", x+3, y+h-3, len([]rune(up)), 1)
		f.region("help-down", x+3+len([]rune(up))+2, y+h-3, len([]rune(down)), 1)
	}
	f.put(x+3, y+h-2, textStyle.Render(closeLabel))
	f.region("help-close", x+3, y+h-2, len(closeLabel), 1)
}
