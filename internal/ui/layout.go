package ui

import (
	"fmt"
	"image"
	"strings"
	"unicode"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/game"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var dimStyle = textStyle.Foreground(lipgloss.Color("#95b77c"))

type hitRegion struct {
	id   string
	rect image.Rectangle
}

// Rendering and hit testing use the same cell geometry. View has no side
// effects; a click can rebuild the layout without changing focus or game state.
type screen struct {
	w, h   int
	layers []*lipgloss.Layer
	hits   []hitRegion
}

func (s *screen) put(x, y int, content string) {
	s.layers = append(s.layers, lipgloss.NewLayer(content).X(x).Y(y))
}
func (s *screen) region(id string, x, y, w, h int) {
	s.hits = append(s.hits, hitRegion{id, image.Rect(x, y, x+w, y+h)})
}
func (s *screen) hit(x, y int) string {
	for i := len(s.hits) - 1; i >= 0; i-- {
		if image.Pt(x, y).In(s.hits[i].rect) {
			return s.hits[i].id
		}
	}
	return ""
}
func (s *screen) content() string {
	return fit(lipgloss.NewCompositor(s.layers...).Render(), s.w, s.h, false)
}

func fit(content string, w, h int, center bool) string {
	w, h = max(1, w), max(1, h)
	rows := strings.Split(content, "\n")
	if len(rows) > h {
		rows = rows[:h]
	}
	for i, row := range rows {
		row = ansi.Truncate(row, w, "…")
		pad := w - lipgloss.Width(row)
		left := 0
		if center {
			left = pad / 2
		}
		rows[i] = strings.Repeat(" ", left) + row + strings.Repeat(" ", pad-left)
	}
	blank := strings.Repeat(" ", w)
	if center {
		for n := (h - len(rows)) / 2; n > 0; n-- {
			rows = append([]string{blank}, rows...)
		}
	}
	for len(rows) < h {
		rows = append(rows, blank)
	}
	return strings.Join(rows, "\n")
}

func panel(title string, w, h int) string {
	title = " " + ansi.Truncate(title, max(1, w-6), "…") + " "
	top := "┌─" + title + strings.Repeat("─", max(0, w-3-lipgloss.Width(title))) + "┐"
	rows := []string{top}
	for i := 0; i < h-2; i++ {
		rows = append(rows, "│"+strings.Repeat(" ", max(0, w-2))+"│")
	}
	rows = append(rows, "└"+strings.Repeat("─", max(0, w-2))+"┘")
	return textStyle.Render(strings.Join(rows, "\n"))
}

func clean(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(text))
}

func (s *screen) section(title, body string, y, h int) {
	s.put(0, y, panel(title, s.w, h))
	s.put(2, y+1, textStyle.Render(fit(body, s.w-4, h-2, true)))
}

func button(label string, w int, selected bool) string {
	style := textStyle.Border(lipgloss.NormalBorder()).BorderForeground(green).Width(w - 2).Align(lipgloss.Center)
	if selected {
		// Explicit colors, never ANSI inverse: both terminal hosts must keep
		// the selected label black against its green background.
		style = style.Foreground(ink).Background(green).Border(lipgloss.DoubleBorder()).BorderForeground(ink).BorderBackground(green)
	}
	return style.Render(ansi.Truncate(label, max(1, w-2), "…"))
}

type screenButton struct {
	id, label string
	selected  bool
}

func (s *screen) buttons(items []screenButton, x, y, w int) {
	for i, item := range items {
		start := i * (w + 1) / len(items)
		end := (i+1)*(w+1)/len(items) - 1
		s.put(x+start, y, button(item.label, end-start, item.selected))
		s.region(item.id, x+start, y, end-start, 3)
	}
}

func (m *Model) layout() *screen {
	w, h := max(1, m.width), max(1, m.height)
	f := &screen{w: w, h: h}
	f.put(0, 0, textStyle.Render(fit("", w, h, false)))
	if w < 76 || h < 30 {
		f.put(0, 0, textStyle.Render(fit(fmt.Sprintf("ARKADE POKER\n\nResize to at least 76 × 30\nCurrent: %d × %d\n\nYour input is preserved.", w, h), w, h, true)))
		if m.modal == exitModal || m.modal == clearGameModal || m.modal == abortSetupModal {
			m.dialog(f)
		}
		return f
	}
	gap := 0
	if h >= 38 {
		gap = 1
	}
	f.put(0, 1, panel("WALLET", w, 3))
	left := m.walletHeader()
	if m.receive.Address != "" {
		left = "CONNECTED / " + shortAddress(m.receive.Address)
		f.region("wallet-copy", 14, 2, lipgloss.Width(shortAddress(m.receive.Address)), 1)
	} else if !m.connecting && !m.clearing {
		left = "[A] Add Wallet"
		f.region("wallet", 2, 2, len(left), 1)
	}
	f.put(2, 2, textStyle.Render(left))
	if m.receive.Address != "" {
		balance := "AVAILABLE BALANCE … SATS"
		if m.balanceKnown {
			balance = "AVAILABLE BALANCE " + formatSats(m.balance) + " SATS"
		}
		if m.balanceFailed {
			balance = "BALANCE UNAVAILABLE"
		}
		f.put(w-len(balance)-2, 2, textStyle.Render(balance))
	}
	statusY := h - 3
	actionY := statusY - gap - 5
	handH := 7
	if h >= 45 {
		handH = 9
	}
	handY := actionY - gap - handH
	boardY := 4 + gap
	boardH := handY - gap - boardY
	const tagline = "HEADS-UP // SATS HOLD'EM"
	welcome := false
	if m.snapshot.State != nil {
		m.tableSections(f, boardY, boardH, handY, handH)
	} else {
		body := "▄▀█ █▀█ █▄▀ ▄▀█ █▀▄ █▀▀\n█▀█ █▀▄ █ █ █▀█ █▄▀ ██▄\n\n█▀█ █▀█ █▄▀ █▀▀ █▀█\n█▀▀ █▄█ █ █ ██▄ █▀▄"
		title := "POKER"
		if m.snapshot.Invitation != nil {
			title = "SESSION / PLAYER " + fmt.Sprint(m.snapshot.Role)
			body = "SESSION READY\n\n" + termsText(m.snapshot.Terms) + "\n\n" + clean(m.status)
		} else if m.connecting || m.shuffling || m.busy {
			body = "PREPARING YOUR SESSION\n\n" + clean(m.status)
		} else if m.snapshot.Outcome != nil {
			body = "SESSION ABORTED\n\n" + clean(m.snapshot.Outcome.AbortReason)
		} else if m.modal == noModal {
			body += "\n\n" + tagline
			welcome = true
		}
		f.section(title, body, boardY, actionY-gap-boardY)
	}
	if m.canClearGame() {
		label := "[X] Clear saved game"
		f.put(w-len(label)-1, 0, dimStyle.Render(label))
		f.region("clear", w-len(label)-1, 0, len(label), 1)
	} else if !welcome {
		f.put(w-len(tagline)-1, 0, dimStyle.Render(tagline))
	}
	legend := "ACTIONS / ARROWS SELECT · ENTER"
	if allowed(m.snapshot.Choice, game.Bet) {
		legend = "YOUR TURN / ARROWS SELECT · ENTER"
	}
	if m.snapshot.Outcome != nil {
		legend = "HAND COMPLETE"
	} else if c := m.countdown(); c != "" {
		legend = c
	} else if m.busy || m.shuffling {
		legend = "PLEASE WAIT"
	} else if m.snapshot.Stage == game.StageBettingOpponent {
		legend = "OPPONENT'S TURN"
	}
	f.put(0, actionY, panel(legend, w, 5))
	var items []screenButton
	for i, a := range m.actions() {
		items = append(items, screenButton{"action:" + a.key, "[" + strings.ToUpper(a.key) + "] " + strings.ToUpper(a.label), i == m.selected})
	}
	if m.key == nil && m.session == nil && !m.connecting && !m.clearing {
		items = []screenButton{{"wallet", "[A] ADD WALLET", true}}
	}
	if len(items) > 0 {
		f.buttons(items, 2, actionY+1, w-4)
	} else {
		f.put(2, actionY+2, dimStyle.Render(fit(m.actionHint(), w-4, 1, true)))
	}
	m.statusSection(f, statusY)
	if m.modal != noModal {
		m.dialog(f)
	}
	return f
}

func (m *Model) actionHint() string {
	if m.stopped {
		return "GAME STOPPED / Saved work retained"
	}
	if m.snapshot.Outcome == nil && m.snapshot.State != nil && m.snapshot.State.Phase.Kind == covenant.ShowdownEvaluation {
		return m.gameStatus()
	}
	if m.shuffling {
		return "Preparing the encrypted deck…"
	}
	if m.busy {
		return "Submitting your action…"
	}
	return m.gameStatus()
}

func (m *Model) statusSection(f *screen, y int) {
	network := m.network
	if m.session != nil && m.session.Network != "" {
		network = m.session.Network
	}
	if network == "" {
		network = "…"
	}
	network = clean(network) + " ●"
	status := clean(m.status)
	if c := m.snapshot.Choice; allowed(c, game.Bet) && c.CanCall && m.snapshot.Outcome == nil {
		status = "TO CALL " + formatSats(c.CallAmount) + " SATS / " + status
	}
	if m.snapshot.Outcome != nil {
		status = m.resultSummary()
	}
	if m.shuffling {
		status = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}[m.frame%10] + " shuffling..."
	}
	if m.frame < m.copyNoticeUntil {
		status = clean(m.copyNotice) + " / " + status
	}
	if m.errorText != "" {
		status = "ERROR / " + clean(m.errorText)
	}
	f.put(0, y, panel("GAME STATUS", f.w, 3))
	available := max(1, f.w-5-lipgloss.Width(network))
	f.put(2, y+1, textStyle.Render(ansi.Truncate(status, available, "…")))
	f.put(f.w-lipgloss.Width(network)-2, y+1, dimStyle.Render(network))
}

func (m *Model) wagers() (mine, theirs uint64) {
	if m.snapshot.State == nil {
		return
	}
	mine, theirs = m.snapshot.State.Wagers.Player1, m.snapshot.State.Wagers.Player2
	if m.snapshot.Role == covenant.Player2 {
		mine, theirs = theirs, mine
	}
	return
}
