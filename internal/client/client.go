package client

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/ports"
	"arkade-poker/go/internal/storage"
	"arkade-poker/go/internal/wallet"
)

type Connections struct {
	Services      wallet.Services
	Subscriptions ports.ScriptSubscriber
	Transport     ports.PeerTransport
	Close         func() // Service clients only; driver closes transport/subscription.
}

type Config struct {
	Game    game.Config // Runtime endpoints; wallet metadata is discovered on import.
	Open    func(context.Context, [32]byte) (storage.Log, error)
	Clear   func(context.Context, [32]byte) error
	Connect func(context.Context, game.Config) (Connections, error)
}

// Session exposes messages, never a mutable Game. NewGame is honored only after
// the driver returns a validated terminal outcome. Errors retain wallet ownership.
type Session struct {
	Receive       wallet.Receive
	Network       string // Network discovered from the configured Arkd endpoint.
	ValidateSetup func(game.Input) error
	Inputs        chan game.Input
	Updates       <-chan game.Update
	Balances      <-chan BalanceUpdate
	NewGame       chan struct{}
	stop          context.CancelFunc
	done          chan struct{}
	keyMu         sync.Mutex
	key           *wallet.Key
}

// releaseKey transfers ownership without copying the secret. A clear takes it
// before stopping the session, then waits for all signing work before reuse.
func (s *Session) releaseKey() *wallet.Key {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	key := s.key
	s.key = nil
	return key
}

func (s *Session) Close() {
	if s != nil {
		s.stop()
		<-s.done
	}
}

// Done closes after all workers stop and storage closes. The key is destroyed
// unless an explicit clear transferred it to the next session.
func (s *Session) Done() <-chan struct{} { return s.done }

type Client struct {
	config     Config
	mu         sync.Mutex
	session    *Session
	public     [32]byte
	pendingKey *wallet.Key // Retained across clear/reopen failures for an explicit retry.
	closed     bool
}

func New(config Config) *Client { return &Client{config: config} }

func (c *Client) Close() {
	c.mu.Lock()
	c.closed = true
	s := c.session
	key := c.pendingKey
	c.pendingKey = nil
	c.mu.Unlock()
	s.Close()
	key.Destroy()
}

// ClearSavedGame stops and joins any owned session before removing its history.
// It also works after Open fails, when no session could be restored. Clearing
// retains an already imported key and opens a fresh session without new ingress.
// Without a loaded wallet it only clears storage. It never submits transactions.
func (c *Client) ClearSavedGame(ctx context.Context, public [32]byte) (*Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, storage.ErrClosed
	}
	if ctx == nil || c.config.Clear == nil {
		return nil, storage.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if public == [32]byte{} || ((c.session != nil || c.pendingKey != nil) && public != c.public) {
		return nil, wallet.ErrKey
	}
	if c.session != nil {
		c.pendingKey = c.session.releaseKey()
	}
	c.session.Close()
	c.session = nil
	if err := c.config.Clear(ctx, public); err != nil {
		return nil, err
	}
	if c.pendingKey == nil {
		c.public = [32]byte{}
		return nil, nil
	}
	s, err := c.open(ctx, c.pendingKey)
	if err != nil {
		return nil, fmt.Errorf("restart cleared game: %w", err)
	}
	c.pendingKey = nil
	return s, nil
}

// Open owns the imported key only after success. Failures release all resources
// and let ingress destroy the key. Services are never contacted with private text.
func (c *Client) Open(ctx context.Context, key *wallet.Key) (_ *Session, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, storage.ErrClosed
	}
	if c.session != nil || c.pendingKey != nil {
		return nil, storage.ErrLocked
	}
	return c.open(ctx, key)
}

// open requires c.mu; ownership transfers only once startup succeeds.
func (c *Client) open(ctx context.Context, key *wallet.Key) (_ *Session, err error) {
	if key == nil || key.PublicKey() == [32]byte{} || c.config.Open == nil || c.config.Connect == nil {
		return nil, wallet.ErrKey
	}
	base, err := c.config.Open(ctx, key.PublicKey())
	if err != nil {
		return nil, err
	}
	var connections Connections
	defer func() {
		if err != nil {
			if connections.Transport != nil {
				_ = connections.Transport.Close()
			}
			if connections.Close != nil {
				connections.Close()
			}
			_ = base.Close()
		}
	}()
	cfg := c.config.Game
	discovery, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	connections, err = c.config.Connect(discovery, cfg)
	if err != nil {
		return nil, err
	}
	identity, err := wallet.New(discovery, connections.Services, key, wallet.Config{})
	if err != nil {
		return nil, err
	}
	cfg.Wallet, err = identity.Config()
	if err != nil {
		return nil, err
	}
	log, err := currentSession(ctx, base, cfg)
	if err != nil {
		return nil, err
	}
	check, err := game.New(cfg)
	if err != nil {
		return nil, err
	}
	check.Destroy()
	life, stop := context.WithCancel(ctx)
	updates := make(chan game.Update)
	balances := make(chan BalanceUpdate, 1)
	s := &Session{Receive: cfg.Wallet.Receive, Network: cfg.Wallet.Network, Inputs: make(chan game.Input), Updates: updates,
		Balances: balances, NewGame: make(chan struct{}), stop: stop, done: make(chan struct{}), key: key}
	s.ValidateSetup = func(input game.Input) error {
		if input.Kind != game.StartSession && input.Kind != game.JoinSession {
			return game.ErrInput
		}
		g, err := game.New(cfg)
		if err != nil {
			return err
		}
		defer g.Destroy()
		step, err := g.Decide(game.Input{Kind: game.Progress})
		if err != nil {
			return err
		}
		if err := g.Apply(*step.Event); err != nil {
			return err
		}
		_, err = g.Decide(input) // Pure validation; no secrets, effects or saved facts.
		return err
	}
	c.session = s
	c.public = key.PublicKey()
	go c.run(life, s, key, base, log, cfg, identity, connections, updates, balances)
	return s, nil
}

func (c *Client) run(ctx context.Context, s *Session, key *wallet.Key, base storage.Log, log *sessionLog, cfg game.Config, identity game.DriverWallet, connections Connections, updates chan game.Update, balances chan BalanceUpdate) {
	defer close(s.done)
	defer close(updates)
	defer close(balances)
	defer func() { s.releaseKey().Destroy() }()
	defer base.Close()
	defer func() {
		if connections.Transport != nil {
			_ = connections.Transport.Close()
		}
		if connections.Close != nil {
			connections.Close()
		}
	}()
	var stopBalance func()
	defer func() {
		if stopBalance != nil {
			stopBalance()
		}
	}()
	sessionCtx := ctx
	for {
		d, err := game.NewDriver(game.DriverConfig{Game: cfg, Log: log, Wallet: identity,
			Connect: func(ctx context.Context, current game.Config) (game.DriverConnections, error) {
				// Replay uses runtime configuration. Game effects start only after
				// the intact journal and imported wallet identity are admitted.
				if connections.Transport == nil {
					discovery, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()
					var err error
					connections, err = c.config.Connect(discovery, current)
					if err != nil {
						return game.DriverConnections{}, err
					}
					identity, err = wallet.New(discovery, connections.Services, key, wallet.Config{})
					if err != nil {
						if connections.Transport != nil {
							_ = connections.Transport.Close()
						}
						return game.DriverConnections{}, err
					}
				}
				if stopBalance == nil {
					stopBalance = startBalance(sessionCtx, connections.Services, connections.Subscriptions, current.Wallet.Receive.Script, balances)
				}
				return game.DriverConnections{Wallet: identity, Indexer: connections.Services.Indexer, Subscriptions: connections.Subscriptions, Transport: connections.Transport}, nil
			}})
		terminal := false
		if err == nil {
			raw := make(chan game.Update)
			done := make(chan error, 1)
			go func() { done <- d.Run(ctx, s.Inputs, raw); close(raw) }()
			for u := range raw {
				terminal = u.Err == nil && u.Snapshot.Outcome != nil && (u.Snapshot.Stage == game.StageFinished || u.Snapshot.Stage == game.StageAborted)
				select {
				case updates <- u:
				case <-ctx.Done():
				}
			}
			err = <-done
			_ = d.Close()
		} else if connections.Transport != nil {
			_ = connections.Transport.Close()
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil || !terminal {
			if err == nil {
				err = game.ErrDriverStopped
			}
			select {
			case updates <- game.Update{Err: err}:
			case <-ctx.Done():
				return
			}
			<-ctx.Done() // Never turn an error into a fresh game or an automatic retry.
			return
		}
		select {
		case <-s.NewGame:
		case <-ctx.Done():
			return
		}
		records, e := base.Load(ctx)
		if e == nil {
			log = &sessionLog{base: base, offset: uint64(len(records))}
			clearRecords(records)
		}
		if stopBalance != nil {
			stopBalance()
			stopBalance = nil
		}
		if connections.Close != nil {
			connections.Close()
			connections.Close = nil
		}
		connections = Connections{}
		if e != nil {
			if connections.Transport != nil {
				_ = connections.Transport.Close()
			}
			select {
			case updates <- game.Update{Err: errors.Join(game.ErrDriverStopped, e)}:
			case <-ctx.Done():
				return
			}
			<-ctx.Done()
			return
		}
	}
}
