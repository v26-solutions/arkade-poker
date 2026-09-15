package game

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	"arkade-poker/go/internal/storage"
	"arkade-poker/go/internal/wallet"
)

var (
	ErrDriverConfiguration = errors.New("game: connections do not match runtime configuration")
	ErrDriverBusy          = errors.New("game: driver already running")
	ErrDriverClosed        = errors.New("game: driver closed")
	ErrDriverStopped       = errors.New("game: automatic advancement stopped; close and restore the intact log")
)

// DriverWallet is the already imported wallet's spending boundary. Implementors
// own service policy, coin selection, signatures and finalization. Returned
// submissions still require independent accepted indexer evidence in the driver.
type DriverWallet interface {
	Config() (wallet.Config, error)
	AdmitAgreement(covenant.Params) error
	SelectFunding(context.Context, int64) (covenant.Funding, error)
	Sign(context.Context, ports.Bundle) (ports.Bundle, error)
	Submit(context.Context, wallet.Route, ports.Bundle) (ports.Submitted, error)
	RetrySaved(context.Context, wallet.Route, ports.Bundle) (ports.Submitted, error)
	ProveOwnership(context.Context, [32]byte) ([]byte, error)
	AuthenticateOwnership(context.Context, [32]byte, [32]byte, []byte) error
}

var _ DriverWallet = (*wallet.Wallet)(nil)

type DriverConnections struct {
	Wallet        DriverWallet
	Indexer       ports.Indexer
	Subscriptions ports.ScriptSubscriber
	Transport     ports.PeerTransport
}

type DriverConfig struct {
	Game          Config
	Log           storage.Log
	Wallet        DriverWallet
	Indexer       ports.Indexer
	Subscriptions ports.ScriptSubscriber
	Transport     ports.PeerTransport
	Entropy       io.Reader // nil selects crypto/rand in the protocol implementation
	// Connect constructs/rebinds services from the supplied runtime configuration,
	// after the currently imported wallet identity matches. If omitted, supplied
	// connections must correspond exactly to Game.
	// With Connect, leave Indexer/Subscriptions/Transport nil; Wallet supplies
	// the already imported identity. Factories own cleanup on error; the host
	// retains shared wallet/indexer lifetimes, as with supplied connections.
	Connect func(context.Context, Config) (DriverConnections, error)
	Now     func() time.Time // recorded clock observations, never read by replay
}
type Update struct {
	Snapshot  Snapshot
	Shuffling bool // UI displays exactly "shuffling..." with an animated spinner.
	Err       error
}

type Driver struct {
	config                      DriverConfig
	mu                          sync.Mutex
	active, closed, loaded, ran bool
	operationCancel             context.CancelFunc
	operationDone               chan struct{}
	lifetime                    context.Context
	stop                        context.CancelFunc
	closeDone                   chan struct{}
	closeErr                    error
	halted                      error
	journal                     *journal
	watch                       *driverWatch
	recovering, retried         bool
	updates                     chan<- Update // only the active Run owns this field
}

func NewDriver(config DriverConfig) (*Driver, error) {
	if config.Log == nil || config.Wallet == nil || config.Connect == nil && (config.Indexer == nil || config.Subscriptions == nil || config.Transport == nil) {
		return nil, ErrInput
	}
	if config.Connect != nil && (config.Indexer != nil || config.Subscriptions != nil || config.Transport != nil) {
		return nil, ErrInput
	}
	g, err := New(config.Game)
	if err != nil {
		return nil, err
	}
	public, err := config.Wallet.Config()
	if err != nil {
		return nil, err
	}
	if public.WalletPublicKey != g.config.Wallet.WalletPublicKey {
		return nil, ErrWalletIdentity
	}
	if config.Connect == nil && !walletConfigMatches(g.config, public) {
		return nil, ErrDriverConfiguration
	}
	config.Game = g.config
	if config.Now == nil {
		config.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Driver{config: config, lifetime: ctx, stop: cancel, closeDone: make(chan struct{})}, nil
}
func (d *Driver) begin(ctx context.Context) (context.Context, error) {
	if d == nil || ctx == nil {
		return nil, ErrInput
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, ErrDriverClosed
	}
	if d.active {
		return nil, ErrDriverBusy
	}
	if d.halted != nil {
		return nil, errors.Join(ErrDriverStopped, d.halted)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	d.active = true
	d.operationCancel = cancel
	d.operationDone = make(chan struct{})
	return ctx, nil
}
func (d *Driver) end(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.halted = err
	}
	d.operationCancel()
	d.operationCancel = nil
	d.active = false
	close(d.operationDone)
}

// Restore replays once, matches the currently imported public wallet identity,
// then reconciles each accepted source edge. It never runs user choices. A saved
// signed spend can be retried once after explicit unspent evidence; failure stops
// this driver and retains the log/ownership until Close. A fresh Driver starts a
// new explicit recovery attempt. Run calls Restore internally if needed.
func (d *Driver) Restore(ctx context.Context) (err error) {
	ctx, err = d.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { d.end(err) }()
	if d.loaded {
		return ErrOrder
	}
	return d.restore(ctx)
}
func (d *Driver) restore(ctx context.Context) error {
	public, err := d.config.Wallet.Config()
	if err != nil {
		return err
	}
	if public.WalletPublicKey != d.config.Game.Wallet.WalletPublicKey {
		return ErrWalletIdentity
	}
	j, err := loadJournal(ctx, d.config.Game, d.config.Log)
	if err != nil {
		return err
	}
	d.journal = j
	d.loaded = true
	if d.config.Connect != nil {
		raw, err := encodeConfig(j.game.config)
		if err != nil {
			return err
		}
		owned, err := decodeConfig(raw)
		if err != nil {
			return err
		}
		connections, err := d.config.Connect(ctx, owned)
		if err != nil {
			return err
		}
		// Retain the returned transport for Close, including rejected results.
		d.config.Wallet = connections.Wallet
		d.config.Indexer = connections.Indexer
		d.config.Subscriptions = connections.Subscriptions
		d.config.Transport = connections.Transport
		if connections.Wallet == nil || connections.Indexer == nil || connections.Subscriptions == nil || connections.Transport == nil {
			return ErrDriverConfiguration
		}
		public, err = connections.Wallet.Config()
		if err != nil {
			return err
		}
		if public.WalletPublicKey != j.game.config.Wallet.WalletPublicKey {
			return ErrWalletIdentity
		}
	} else if !equalConfig(d.config.Game, j.game.config) {
		return ErrDriverConfiguration
	}
	if !walletConfigMatches(j.game.config, public) {
		return ErrDriverConfiguration
	}
	d.config.Game = j.game.config
	d.recovering = j.game.hand != nil || j.game.prepared != nil
	d.retried = false
	if !d.recovering {
		return nil
	}
	return d.recover(ctx)
}

// Run owns one sequential game and all effects. UI commands never mutate Game.
// An invalid user choice is reported without changing the state; durable/network
// failures stop the loop, preserving saved work. Context cancellation/Close does
// not concede, discard the log, or release funds for another game.
func (d *Driver) Run(ctx context.Context, inputs <-chan Input, updates chan<- Update) (err error) {
	ctx, err = d.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { d.end(err) }()
	if d.ran {
		return ErrOrder
	}
	d.ran = true
	d.updates = updates
	defer func() { d.updates = nil }()
	if !d.loaded {
		if updates != nil {
			select {
			case updates <- Update{Snapshot: Snapshot{Stage: StageInit}, Shuffling: true}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if err := d.restore(ctx); err != nil {
			return d.reportError(ctx, err)
		}
	}
	input := Input{Kind: Progress}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.recovering {
			if err := d.recover(ctx); err != nil {
				return d.reportError(ctx, err)
			}
			if d.recovering {
				if err := d.emit(ctx, nil, false, nil); err != nil {
					return err
				}
				if _, err := d.waitHint(ctx, 250*time.Millisecond); err != nil {
					return d.reportError(ctx, err)
				}
				continue
			}
		}
		step, stepErr := d.step(ctx, input)
		userInput := input.Kind != Progress
		input = Input{Kind: Progress}
		if stepErr != nil {
			if userInput && (errors.Is(stepErr, ErrInput) || errors.Is(stepErr, ErrAmount)) {
				if err := d.emit(ctx, nil, false, stepErr); err != nil {
					return err
				}
				continue
			}
			return d.reportError(ctx, stepErr)
		}
		switch step.Kind {
		case RecordEvent:
			if step.Event == nil {
				return ErrProtocol
			}
			err := d.commit(ctx, *step.Event)
			if step.Event.Secrets != nil {
				_ = step.Event.Secrets.Destroy()
			}
			if err != nil {
				return d.reportError(ctx, err)
			}
			if err := d.emit(ctx, nil, false, nil); err != nil {
				return err
			}
			// The journal start precedes processing a queued create/join command.
		case Finished:
			return d.emit(ctx, nil, false, nil)
		case NeedsInput, Waiting:
			if err := d.emit(ctx, step.Choice, false, nil); err != nil {
				return err
			}
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case next, ok := <-inputs:
				timer.Stop()
				if !ok {
					inputs = nil
				} else {
					input = next
				}
			case <-d.watchSignal():
				timer.Stop()
			case <-timer.C:
			}
		default:
			return ErrProtocol
		}
		if input.Kind == Progress {
			select {
			case next, ok := <-inputs:
				if !ok {
					inputs = nil
				} else {
					input = next
				}
			default:
			}
		}
	}
}
func (d *Driver) emit(ctx context.Context, choice *Choice, shuffling bool, err error) error {
	if d.updates == nil {
		return nil
	}
	snapshot := Snapshot{Stage: StageInit}
	if d.journal != nil {
		var e error
		snapshot, e = d.journal.game.Snapshot()
		if e != nil {
			return e
		}
	}
	snapshot.Choice = choice // Expose choices only after this step's source observation.
	select {
	case d.updates <- Update{Snapshot: snapshot, Shuffling: shuffling, Err: err}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (d *Driver) reportError(ctx context.Context, err error) error {
	_ = d.emit(ctx, nil, false, err)
	return err
}
func (d *Driver) commit(ctx context.Context, e Event) error {
	prior := d.journal.game.stage
	if err := d.journal.append(ctx, e); err != nil {
		return err
	}
	if e.Kind == SubmissionAttempted && e.Receipt.TxID == nil {
		d.recovering = true
		d.retried = false
	}
	if e.Kind == SpendObserved || e.Kind == SetupAborted || e.Kind == DepositObserved && prior == StageAwaitInitialDeposit {
		_ = d.closeWatch()
	}
	return nil
}
func (d *Driver) now() (covenant.UnixSeconds, error) {
	n := d.config.Now().Unix()
	if n < 0 {
		return 0, ErrDeadline
	}
	return covenant.UnixSeconds(n), nil
}

// Close is safe during Run/Restore: cancel work, await its single owner, then
// release local writers and session secrets. The host retains the imported
// wallet key and shared indexer/service client lifetimes.
func (d *Driver) Close() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	if d.closed {
		done := d.closeDone
		d.mu.Unlock()
		<-done
		return d.closeErr
	}
	d.closed = true
	d.stop()
	if d.operationCancel != nil {
		d.operationCancel()
	}
	active, done := d.active, d.operationDone
	d.mu.Unlock()
	if active {
		<-done
	}
	var errs []error
	errs = append(errs, d.closeWatch())
	if d.config.Transport != nil {
		errs = append(errs, d.config.Transport.Close())
	}
	errs = append(errs, d.config.Log.Close())
	if d.journal != nil {
		discardGame(d.journal.game, nil)
	}
	d.mu.Lock()
	d.closeErr = errors.Join(errs...)
	close(d.closeDone)
	d.mu.Unlock()
	return d.closeErr
}

func equalConfig(a, b Config) bool {
	x, e1 := encodeConfig(a)
	y, e2 := encodeConfig(b)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}
func walletConfigMatches(config Config, public wallet.Config) bool {
	actual := config
	actual.Wallet = public
	return equalConfig(actual, config)
}
