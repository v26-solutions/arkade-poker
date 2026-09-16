// Package ui is the shared native/browser Bubble Tea interface. Commands perform
// blocking work; Update exclusively owns the transient UI state.
package ui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arkade-poker/go/internal/appconfig"
	"arkade-poker/go/internal/client"
	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/wallet"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

type Host struct {
	Browser         bool
	InitialKey      *wallet.Key
	InitialError    string
	DiscoverNetwork func(context.Context) (string, error)
	ConnectWallet   func(context.Context, *wallet.Key) (wallet.Receive, error)
	ConnectSession  func(context.Context, *wallet.Key) (*client.Session, error)
	ClearSavedGame  func(context.Context, [32]byte) (*client.Session, error)
	AbortSetup      func(context.Context, [32]byte) (*client.Session, error)
	RelayURL        string
	DefaultTerms    game.Terms
	CopyText        func(string) error
}

type modal uint8

const (
	noModal modal = iota
	walletModal
	exitModal
	createModal
	joinModal
	tokenModal
	raiseModal
	joinConfirmModal
	clearGameModal
	allInModal
	payoutModal
	menuModal
	abortSetupModal
)

type tickMsg time.Time
type networkMsg struct {
	network string
	err     error
}
type connectedMsg struct {
	receive wallet.Receive
	err     error
	session *client.Session
}

type copiedMsg struct{ err error }

// ProgressMsg is transient and never recorded in the private game log.
type ProgressMsg struct {
	Shuffling bool
	Status    string
}

type Model struct {
	ctx               context.Context
	host              Host
	width, height     int
	modal             modal
	previousModal     modal
	exitConfirm       bool
	input             textinput.Model
	key               *wallet.Key
	receive           wallet.Receive
	network           string
	balance           int64
	balanceKnown      bool
	balanceFailed     bool
	connecting        bool
	errorText, status string
	shuffling         bool
	frame             uint64
	now               time.Time
	copyNoticeUntil   uint64
	copyNotice        string
	session           *client.Session
	snapshot          game.Snapshot
	fields            []textinput.Model
	field, selected   int
	joinInvitation    *game.Invitation
	busy, stopped     bool
	clearPublic       [32]byte
	clearConfirm      bool
	clearing          bool
	generation        uint64
}

func New(ctx context.Context, host Host) *Model {
	defaults := appconfig.Defaults()
	if host.DefaultTerms == (game.Terms{}) {
		host.DefaultTerms = defaults.Terms
	}
	if host.RelayURL == "" {
		host.RelayURL = defaults.RelayURL
	}
	input := textinput.New()
	input.Placeholder = "nsec, hexadecimal key or BIP39 mnemonic"
	input.EchoMode = textinput.EchoPassword
	input.EchoCharacter = '•'
	// Keep overlong input intact enough to reject it, rather than silently
	// truncating a pasted invalid key into a valid 64-digit key.
	input.CharLimit = 4096
	input.SetWidth(54)
	return &Model{ctx: ctx, host: host, width: 80, height: 30, input: input, now: time.Now(),
		key: host.InitialKey, errorText: host.InitialError,
		status: "Add a funded wallet to begin"}
}

func tick() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) Init() tea.Cmd {
	if m.key != nil {
		return tea.Batch(tick(), m.discoverNetwork(), m.connect())
	}
	return tea.Batch(tick(), m.discoverNetwork())
}

func (m *Model) discoverNetwork() tea.Cmd {
	if m.host.DiscoverNetwork == nil {
		return nil
	}
	ctx, discover := m.ctx, m.host.DiscoverNetwork
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		network, err := discover(ctx)
		return networkMsg{network: network, err: err}
	}
}

func (m *Model) connect() tea.Cmd {
	m.connecting = true
	m.status = "Connecting wallet..."
	key := m.key
	m.clearPublic = key.PublicKey()
	return func() tea.Msg {
		if m.host.ConnectSession != nil {
			s, err := m.host.ConnectSession(m.ctx, key)
			if err != nil {
				return connectedMsg{err: err}
			}
			return connectedMsg{receive: s.Receive, session: s}
		}
		if m.host.ConnectWallet == nil {
			return connectedMsg{err: errors.New("wallet unavailable")}
		}
		receive, err := m.host.ConnectWallet(m.ctx, key)
		return connectedMsg{receive: receive, err: err}
	}
}

func (m *Model) openWallet() tea.Cmd {
	if m.key != nil || m.connecting || m.clearing {
		return nil
	}
	m.modal = walletModal
	m.errorText = ""
	return m.input.Focus()
}

func (m *Model) copyWallet() tea.Cmd {
	if m.receive.Address == "" {
		return nil
	}
	address, copyText := m.receive.Address, m.host.CopyText
	m.copyNotice = "Address copied"
	return func() tea.Msg {
		if copyText == nil {
			return copiedMsg{errors.New("clipboard unavailable")}
		}
		return copiedMsg{copyText(address)}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(10, min(54, m.width-12)))
		for i := range m.fields {
			m.fields[i].SetWidth(max(10, min(46, m.width-26)))
		}
	case tickMsg:
		m.frame++
		m.now = time.Time(msg)
		return m, tick()
	case ProgressMsg:
		m.shuffling, m.status = msg.Shuffling, msg.Status
	case networkMsg:
		m.network = msg.network
		if msg.err != nil {
			m.network = "unavailable"
		}
	case copiedMsg:
		if msg.err != nil {
			m.errorText = "Could not copy the address to the clipboard."
			m.copyNoticeUntil = 0
		} else {
			m.errorText = ""
			m.copyNoticeUntil = m.frame + 25
		}
	case connectedMsg:
		m.connecting = false
		if msg.err != nil {
			m.errorText = "Wallet connection failed: " + msg.err.Error()
			m.status = "Add a funded wallet to begin"
			m.key.Destroy()
			m.key = nil
		} else {
			m.receive = msg.receive
			m.balanceKnown, m.balanceFailed = false, false
			m.status = "Wallet connected"
			m.session = msg.session
			if m.session != nil {
				m.status = "Restoring saved game..."
				return m, tea.Batch(m.waitUpdate(), m.waitBalance())
			}
		}
	case clearedGameMsg:
		m.clearing = false
		m.status = "Saved game cleared."
		if msg.err != nil {
			m.errorText = "Could not clear saved game: " + msg.err.Error()
			m.status = "Clear failed. Retry to continue."
			if msg.abort {
				m.errorText = "Could not abort setup: " + msg.err.Error()
				m.status = "Setup stopped. Saved work retained; restart to resume."
				m.stopped = true
			}
			return m, nil
		}
		m.snapshot = game.Snapshot{}
		if msg.session != nil {
			m.session = msg.session
			m.receive = msg.session.Receive
			return m, tea.Batch(m.waitUpdate(), m.waitBalance())
		} else {
			m.clearPublic = [32]byte{}
		}
	case balanceMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		if msg.closed {
			m.balanceKnown, m.balanceFailed = false, true
			return m, nil
		}
		m.balance = msg.update.Sats
		m.balanceKnown, m.balanceFailed = msg.update.Err == nil, msg.update.Err != nil
		return m, m.waitBalance()
	case driverMsg:
		return m, m.driverUpdate(msg)
	case sentMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		if msg.err != nil {
			m.errorText = "The game is no longer accepting actions."
			m.stopped = true
		}
		return m, nil
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft || m.clearing {
			return m, nil
		}
		id := m.layout().hit(msg.X, msg.Y)
		switch id {
		case "wallet":
			return m, m.openWallet()
		case "wallet-copy":
			return m, m.copyWallet()
		case "clear":
			m.openClearGame()
			return m, nil
		case "abort":
			m.openAbortSetup()
			return m, nil
		case "cancel":
			return m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		case "confirm":
			if m.modal == exitModal {
				return m, tea.Quit
			}
			if m.modal == clearGameModal {
				return m, m.clearGame()
			}
			if m.modal == abortSetupModal {
				return m, m.abortSetup()
			}
			return m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case "menu-exit":
			m.previousModal, m.modal, m.exitConfirm = menuModal, exitModal, false
			return m, nil
		}
		if len(id) > 7 && id[:7] == "action:" {
			cmd, _ := m.gameKey(id[7:])
			return m, cmd
		}
		if len(id) > 6 && id[:6] == "field:" {
			for i := range m.fields {
				if id == fmt.Sprintf("field:%d", i) {
					m.fields[m.field].Blur()
					m.field = i
					return m, m.fields[i].Focus()
				}
			}
		}
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			if !m.host.Browser && m.modal != exitModal {
				m.previousModal, m.modal, m.exitConfirm = m.modal, exitModal, false
			}
			return m, nil
		}
		if m.clearing && m.modal != exitModal {
			return m, nil
		}
		if m.modal == clearGameModal || m.modal == abortSetupModal {
			switch key {
			case "tab", "left", "right":
				m.clearConfirm = !m.clearConfirm
			case "esc":
				m.modal = noModal
			case "enter":
				if m.clearConfirm {
					if m.modal == abortSetupModal {
						return m, m.abortSetup()
					}
					return m, m.clearGame()
				}
				m.modal = noModal
			}
			return m, nil
		}
		if m.modal == noModal && key == "x" && m.canClearGame() {
			m.openClearGame()
			return m, nil
		}
		if key == "esc" {
			if m.modal == noModal && m.session != nil {
				m.modal = menuModal
			} else if m.modal == exitModal {
				m.modal = m.previousModal
			} else {
				m.modal = noModal
				m.input.Reset()
				m.input.Blur()
				m.clearForm()
				m.errorText = ""
			}
			return m, nil
		}
		if m.modal == exitModal {
			switch key {
			case "tab", "left", "right":
				m.exitConfirm = !m.exitConfirm
			case "enter":
				if m.exitConfirm {
					return m, tea.Quit
				}
				m.modal = m.previousModal
			}
			return m, nil
		}
		if m.modal == walletModal && key == "enter" {
			key, err := wallet.ParseKey(m.input.Value(), m.network)
			if err != nil {
				m.errorText = err.Error()
				if errors.Is(err, wallet.ErrKeyNetwork) {
					return m, m.discoverNetwork()
				}
				return m, nil
			}
			m.key = key
			m.input.Reset()
			m.input.Blur()
			m.modal = noModal
			m.errorText = ""
			return m, m.connect()
		}
		if m.modal == noModal && m.key == nil && m.session == nil && (key == "a" || key == "enter") {
			return m, m.openWallet()
		}
		if cmd, handled := m.gameKey(key); handled {
			return m, cmd
		}
	}
	if m.modal == walletModal {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	if len(m.fields) != 0 && (m.modal == createModal || m.modal == joinModal || m.modal == raiseModal) {
		var cmd tea.Cmd
		m.fields[m.field], cmd = m.fields[m.field].Update(msg)
		return m, cmd
	}
	return m, nil
}
