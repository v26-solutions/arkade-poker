package client

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/ports"
	"arkade-poker/go/internal/storage"
	"arkade-poker/go/internal/wallet"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
)

type memoryLog struct {
	mu      sync.Mutex
	records [][]byte
	locked  bool
}

func (m *memoryLog) Load(context.Context) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := make([][]byte, len(m.records))
	for i := range r {
		r[i] = bytes.Clone(m.records[i])
	}
	return r, nil
}
func (m *memoryLog) Append(_ context.Context, i uint64, r []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i != uint64(len(m.records)) {
		return storage.ErrIndex
	}
	m.records = append(m.records, bytes.Clone(r))
	return nil
}
func (m *memoryLog) Close() error { m.mu.Lock(); defer m.mu.Unlock(); m.locked = false; return nil }
func (m *memoryLog) open(context.Context, [32]byte) (storage.Log, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locked {
		return nil, storage.ErrLocked
	}
	m.locked = true
	return m, nil
}

func TestClearSavedGameClosesSessionAndAllowsReimport(t *testing.T) {
	for _, state := range []string{"active", "failed open", "failed driver"} {
		t.Run(state, func(t *testing.T) {
			cfg, g, log, connects := fixture(t)
			seed(t, g, log, false)
			if state == "failed open" {
				log.records[0] = []byte("obsolete game encoding")
			} else if state == "failed driver" {
				e, err := game.DecodeEvent(log.records[0])
				if err != nil {
					t.Fatal(err)
				}
				e.Previous[0] ^= 1 // Correct identity, but invalid journal chain.
				log.records[0], err = game.EncodeEvent(e)
				if err != nil {
					t.Fatal(err)
				}
			}
			key := importKey(t, 20)
			defer key.Destroy()
			public := key.PublicKey()
			clears := 0
			var s *Session
			cfg.Clear = func(_ context.Context, got [32]byte) error {
				clears++
				if got != public {
					t.Fatal("cleared wrong wallet")
				}
				if s != nil {
					select {
					case <-s.Done():
					default:
						t.Fatal("clear ran before session stopped")
					}
					if key.PublicKey() != [32]byte{} {
						t.Fatal("clear retained imported key")
					}
				}
				log.mu.Lock()
				defer log.mu.Unlock()
				if log.locked {
					t.Fatal("clear ran before writer released")
				}
				log.records = nil
				return nil
			}
			c := New(cfg)
			defer c.Close()
			var err error
			s, err = c.Open(t.Context(), key)
			if state == "failed open" {
				if err == nil {
					t.Fatal("invalid history opened")
				}
				key.Destroy()
			} else {
				if err != nil {
					t.Fatal(err)
				}
				u := await(t, s, func(u game.Update) bool { return u.Err != nil || u.Snapshot.Choice != nil })
				if (state == "failed driver") != (u.Err != nil) {
					t.Fatal("wrong restore result", u.Err)
				}
				if err := c.ClearSavedGame(t.Context(), [32]byte{3}); !errors.Is(err, wallet.ErrKey) || clears != 0 {
					t.Fatal("mismatched wallet cleared active session", err)
				}
			}
			before := connects.Load()
			if err := c.ClearSavedGame(t.Context(), public); err != nil {
				t.Fatal(err)
			}
			if clears != 1 || connects.Load() != before || len(log.records) != 0 {
				t.Fatal("clear must remove history without service calls")
			}
			s, err = c.Open(t.Context(), importKey(t, 20))
			if err != nil {
				t.Fatal("reimport after clear", err)
			}
			u := await(t, s, func(u game.Update) bool { return u.Err != nil || u.Snapshot.Choice != nil })
			if u.Err != nil || u.Snapshot.Stage != game.StageInit {
				t.Fatal("reimport did not start fresh", u.Err)
			}
		})
	}
}

type arkFixture struct {
	ports.Arkd
	info ports.ArkInfo
}

func (a arkFixture) Info(context.Context) (ports.ArkInfo, error) { return a.info, nil }

type emuFixture struct {
	ports.Emulator
	info ports.EmulatorInfo
}

func (a emuFixture) Info(context.Context) (ports.EmulatorInfo, error) { return a.info, nil }

type delegateFixture struct {
	ports.Delegator
	info ports.DelegatorInfo
}

func (d delegateFixture) Info(context.Context) (ports.DelegatorInfo, error) { return d.info, nil }

type indexFixture struct {
	ports.Indexer
	ports.ScriptSubscriber
}

func (indexFixture) Vtxos(ctx context.Context, _ ports.VtxoQuery) ([]ports.Vtxo, error) {
	return nil, ctx.Err()
}
func (indexFixture) Subscribe(ctx context.Context, _ [][]byte) (ports.ScriptSubscription, error) {
	return idleSubscription{}, ctx.Err()
}

type idleSubscription struct{}

func (idleSubscription) Next(ctx context.Context) (ports.ScriptEvent, error) {
	<-ctx.Done()
	return ports.ScriptEvent{}, ctx.Err()
}
func (idleSubscription) Close() error { return nil }

type peerFixture struct{ ports.PeerTransport }

func (*peerFixture) Close() error { return nil }
func importKey(t *testing.T, n byte) *wallet.Key {
	t.Helper()
	k, err := wallet.ParseKey(fmt.Sprintf("%064x", n), "")
	if err != nil {
		t.Fatal(err)
	}
	return k
}
func fixture(t *testing.T) (Config, game.Config, *memoryLog, *atomic.Int32) {
	t.Helper()
	server, _ := btcec.PrivKeyFromBytes([]byte{21})
	emu, _ := btcec.PrivKeyFromBytes([]byte{22})
	forfeit, _ := btcec.PrivKeyFromBytes([]byte{23})
	defer server.Zero()
	defer emu.Zero()
	defer forfeit.Zero()
	cp, err := (&script.CSVMultisigClosure{MultisigClosure: script.MultisigClosure{PubKeys: []*btcec.PublicKey{forfeit.PubKey()}}, Locktime: arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 16}}).Script()
	if err != nil {
		t.Fatal(err)
	}
	services := wallet.Services{Arkd: arkFixture{info: ports.ArkInfo{Network: "regtest", Signer: hex.EncodeToString(server.PubKey().SerializeCompressed()), Forfeit: hex.EncodeToString(forfeit.PubKey().SerializeCompressed()), CheckpointScript: hex.EncodeToString(cp), ExitDelay: 512, Dust: 100, MaxVtxo: -1}}, Emulator: emuFixture{info: ports.EmulatorInfo{Signer: hex.EncodeToString(emu.PubKey().SerializeCompressed())}}, Indexer: indexFixture{}}
	delegate, _ := btcec.PrivKeyFromBytes([]byte{24})
	defer delegate.Zero()
	services.Delegator = delegateFixture{info: ports.DelegatorInfo{PubKey: hex.EncodeToString(delegate.PubKey().SerializeCompressed())}}
	key := importKey(t, 20)
	defer key.Destroy()
	w, err := wallet.New(context.Background(), services, key, wallet.Config{Network: "regtest"})
	if err != nil {
		t.Fatal(err)
	}
	public, _ := w.Config()
	g := game.Config{Wallet: public, ArkdURL: "http://ark.invalid", EmulatorURL: "http://emu.invalid", DelegatorURL: "http://delegate.invalid", IndexerURL: "http://index.invalid"}
	log := &memoryLog{}
	calls := &atomic.Int32{}
	fresh := g
	fresh.Wallet = wallet.Config{Network: "regtest"}
	config := Config{Game: fresh, Open: log.open, Connect: func(_ context.Context, current game.Config) (Connections, error) {
		calls.Add(1)
		return Connections{Services: services, Subscriptions: indexFixture{}, Transport: &peerFixture{}, Close: func() {}}, nil
	}}
	return config, g, log, calls
}
func record(t *testing.T, g *game.Game, e game.Event, log *memoryLog) {
	t.Helper()
	raw, err := game.EncodeEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Apply(e); err != nil {
		t.Fatal(err)
	}
	if err := log.Append(context.Background(), uint64(len(log.records)), raw); err != nil {
		t.Fatal(err)
	}
}
func seed(t *testing.T, cfg game.Config, log *memoryLog, terminal bool) {
	t.Helper()
	g, err := game.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Destroy()
	s, err := g.Decide(game.Input{Kind: game.Progress})
	if err != nil {
		t.Fatal(err)
	}
	record(t, g, *s.Event, log)
	if !terminal {
		return
	}
	e, err := g.PrepareSession(context.Background(), nil, game.Input{Kind: game.StartSession, Terms: game.Terms{Stake: 1000, Bond: 1000, MinBet: 100, MaxWager: 1000}, RelayURL: "ws://relay.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Secrets.Destroy()
	record(t, g, e, log)
	e = g.NewEvent(game.SetupAborted)
	e.FailureCode = "funding_unavailable"
	record(t, g, e, log)
}
func await(t *testing.T, s *Session, predicate func(game.Update) bool) game.Update {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case u, ok := <-s.Updates:
			if !ok {
				t.Fatal("session closed")
			}
			if predicate(u) {
				return u
			}
		case <-timer.C:
			t.Fatal("update timeout")
		}
	}
}

func TestFreshRestartAndWriterOwnership(t *testing.T) {
	cfg, _, log, calls := fixture(t)
	c := New(cfg)
	key := importKey(t, 20)
	s, err := c.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	await(t, s, func(u game.Update) bool { return u.Snapshot.Choice != nil })
	if u := awaitBalance(t, s.Balances); u.Err != nil || u.Sats != 0 {
		t.Fatal("fresh wallet balance", u)
	}
	if err := s.ValidateSetup(game.Input{Kind: game.StartSession, Terms: game.Terms{Stake: 1000, Bond: 1000, MinBet: 100, MaxWager: 1000}, RelayURL: "ws://bad relay"}); !errors.Is(err, game.ErrInvitation) {
		t.Fatal("invalid relay bypassed pure form admission", err)
	}
	second := New(cfg)
	defer second.Close()
	other := importKey(t, 20)
	defer other.Destroy()
	if _, err := second.Open(context.Background(), other); !errors.Is(err, storage.ErrLocked) {
		t.Fatal("duplicate writer", err)
	}
	c.Close()
	if key.PublicKey() != [32]byte{} {
		t.Fatal("closed client retained wallet key")
	}
	if len(log.records) != 1 {
		t.Fatal("configured journal missing")
	}
	changed := cfg
	changed.Game.ArkdURL = "http://changed.invalid"
	changed.Game.DelegatorURL = "http://changed-delegate.invalid"
	changed.Connect = func(ctx context.Context, current game.Config) (Connections, error) {
		if current.ArkdURL != changed.Game.ArkdURL || current.DelegatorURL != changed.Game.DelegatorURL {
			t.Fatal("journal overrode runtime endpoints")
		}
		return cfg.Connect(ctx, current)
	}
	restarted := New(changed)
	defer restarted.Close()
	s, err = restarted.Open(context.Background(), importKey(t, 20))
	if err != nil {
		t.Fatal(err)
	}
	await(t, s, func(u game.Update) bool { return u.Snapshot.Choice != nil || u.Err != nil })
	if u := awaitBalance(t, s.Balances); u.Err != nil || u.Sats != 0 {
		t.Fatal("restored wallet balance", u)
	}
	if calls.Load() != 2 || len(log.records) != 1 {
		t.Fatal("restore duplicated discovery or journal start")
	}
}

func TestCompletedGamesAppendAndUnfinishedHistoryCannotBeSkipped(t *testing.T) {
	cfg, g, log, _ := fixture(t)
	seed(t, g, log, true)
	prefix, _ := log.Load(context.Background())
	c := New(cfg)
	defer c.Close()
	s, err := c.Open(context.Background(), importKey(t, 20))
	if err != nil {
		t.Fatal(err)
	}
	await(t, s, func(u game.Update) bool { return u.Snapshot.Outcome != nil })
	if u := awaitBalance(t, s.Balances); u.Err != nil {
		t.Fatal("terminal wallet balance", u)
	}
	select {
	case s.NewGame <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("new game unavailable after terminal outcome")
	}
	await(t, s, func(u game.Update) bool { return u.Snapshot.Stage == game.StageInit && u.Snapshot.Choice != nil })
	if u := awaitBalance(t, s.Balances); u.Err != nil {
		t.Fatal("new game wallet balance", u)
	}
	if len(log.records) != len(prefix)+1 {
		t.Fatal("new configuration not appended")
	}
	for i, raw := range prefix {
		if !bytes.Equal(log.records[i], raw) {
			t.Fatal("completed history rewritten")
		}
	}
	c.Close()
	if _, err := currentSession(context.Background(), log, g); err != nil {
		t.Fatal("valid boundary", err)
	}
	bad := &memoryLog{}
	seed(t, g, bad, false)
	seed(t, g, bad, false)
	if _, err := currentSession(context.Background(), bad, g); !errors.Is(err, game.ErrOrder) {
		t.Fatal("unfinished game skipped", err)
	}
}

func TestReplayFailureRetainsWriterAfterDiscovery(t *testing.T) {
	cfg, g, log, calls := fixture(t)
	seed(t, g, log, true)
	e, err := game.DecodeEvent(log.records[1])
	if err != nil {
		t.Fatal(err)
	}
	defer e.Secrets.Destroy()
	e.Previous[0] ^= 1
	log.records[1], err = game.EncodeEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	c := New(cfg)
	defer c.Close()
	s, err := c.Open(context.Background(), importKey(t, 20))
	if err != nil {
		t.Fatal(err)
	}
	u := await(t, s, func(u game.Update) bool { return u.Err != nil })
	if u.Snapshot.Outcome != nil || calls.Load() != 1 {
		t.Fatal("replay failure repeated discovery or released game")
	}
	select {
	case s.NewGame <- struct{}{}:
		t.Fatal("error allowed new game")
	default:
	}
	log.mu.Lock()
	locked := log.locked
	log.mu.Unlock()
	if !locked {
		t.Fatal("error released local writer")
	}
}

func TestWrongSavedIdentityRejectedAfterDiscovery(t *testing.T) {
	cfg, g, log, calls := fixture(t)
	seed(t, g, log, false)
	c := New(cfg)
	defer c.Close()
	key := importKey(t, 19)
	defer key.Destroy()
	if _, err := c.Open(context.Background(), key); !errors.Is(err, game.ErrWalletIdentity) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("wrong wallet repeated discovery")
	}
}

type scriptBalanceFixture struct {
	indexFixture
	script []byte
}

func (i scriptBalanceFixture) Vtxos(ctx context.Context, q ports.VtxoQuery) ([]ports.Vtxo, error) {
	if !bytes.Equal(q.Script, i.script) {
		return nil, errors.New("queried stale receive script")
	}
	return []ports.Vtxo{{Script: bytes.Clone(i.script), Amount: 1234, ExpiresAt: time.Now().Add(time.Hour).Unix()}}, ctx.Err()
}

func TestRestartRediscoversReceiveAndBalance(t *testing.T) {
	cfg, delegated, log, _ := fixture(t)
	plain := cfg
	plain.Game.DelegatorURL = ""
	plain.Connect = func(ctx context.Context, current game.Config) (Connections, error) {
		c, err := cfg.Connect(ctx, current)
		c.Services.Delegator = nil
		return c, err
	}
	first := New(plain)
	s, err := first.Open(context.Background(), importKey(t, 20))
	if err != nil {
		t.Fatal(err)
	}
	oldAddress := s.Receive.Address
	await(t, s, func(u game.Update) bool { return u.Snapshot.Choice != nil })
	first.Close()
	before, err := log.Load(context.Background())
	if err != nil || len(before) != 1 {
		t.Fatal("journal start", err)
	}
	event, err := game.DecodeEvent(before[0])
	if err != nil || event.Config != nil {
		t.Fatal("runtime config was persisted", err)
	}
	for _, value := range []string{oldAddress, cfg.Game.ArkdURL, cfg.Game.EmulatorURL, cfg.Game.DelegatorURL} {
		if bytes.Contains(before[0], []byte(value)) {
			t.Fatal("wallet configuration leaked into journal")
		}
	}
	connect := cfg.Connect
	cfg.Connect = func(ctx context.Context, current game.Config) (Connections, error) {
		c, err := connect(ctx, current)
		c.Services.Indexer = scriptBalanceFixture{script: delegated.Wallet.Receive.Script}
		return c, err
	}
	second := New(cfg)
	defer second.Close()
	s, err = second.Open(context.Background(), importKey(t, 20))
	if err != nil {
		t.Fatal(err)
	}
	if s.Receive.Address == oldAddress || s.Receive.Address != delegated.Wallet.Receive.Address {
		t.Fatal("receive address was not rediscovered")
	}
	u := await(t, s, func(u game.Update) bool { return u.Snapshot.Choice != nil || u.Err != nil })
	if u.Err != nil {
		t.Fatal(u.Err)
	}
	if balance := awaitBalance(t, s.Balances); balance.Err != nil || balance.Sats != 1234 {
		t.Fatal("balance did not follow current receive script", balance)
	}
	after, err := log.Load(context.Background())
	if err != nil || len(after) != 1 || !bytes.Equal(before[0], after[0]) {
		t.Fatal("restart rewrote journal", err)
	}
}
