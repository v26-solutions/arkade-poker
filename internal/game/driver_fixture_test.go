package game

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	"arkade-poker/go/internal/wallet"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

type driverFixture struct {
	mu                  sync.Mutex
	h                   *handHarness
	records             map[wire.OutPoint]ports.Vtxo
	txs                 map[chainhash.Hash]*wire.MsgTx
	subscriptions       map[*fixtureSubscription]bool
	relay               map[string][]byte
	relayChanged        chan struct{}
	trace               []string
	queryErr, attachErr error
	attachStarted       chan<- struct{}
	attachGate          <-chan struct{}
	beforeQuery         func(ports.VtxoQuery)
}

func newDriverFixture(t *testing.T) *driverFixture {
	h := newHandHarness(t)
	return &driverFixture{h: h, records: map[wire.OutPoint]ports.Vtxo{}, txs: map[chainhash.Hash]*wire.MsgTx{}, subscriptions: map[*fixtureSubscription]bool{}, relay: map[string][]byte{}, relayChanged: make(chan struct{})}
}
func (f *driverFixture) recordTrace(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trace = append(f.trace, s)
}
func (f *driverFixture) Vtxos(ctx context.Context, q ports.VtxoQuery) ([]ports.Vtxo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trace = append(f.trace, "query")
	if f.beforeQuery != nil {
		f.beforeQuery(q)
	}
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	var out []ports.Vtxo
	add := func(v ports.Vtxo) { v.Script = bytes.Clone(v.Script); out = append(out, v) }
	if len(q.Script) > 0 {
		for _, v := range f.records {
			if bytes.Equal(v.Script, q.Script) {
				add(v)
			}
		}
	} else {
		for _, p := range q.Outpoints {
			if v, ok := f.records[p]; ok {
				add(v)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Outpoint.String() < out[j].Outpoint.String() })
	return out, nil
}
func (f *driverFixture) Transaction(ctx context.Context, id chainhash.Hash) (*wire.MsgTx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	if tx := f.txs[id]; tx != nil {
		return tx.Copy(), nil
	}
	return nil, nil
}
func (*driverFixture) Close() error { return nil }
func (f *driverFixture) indexOutputs(tx *wire.MsgTx) {
	id := tx.TxHash()
	f.txs[id] = tx.Copy()
	f.h.known[id] = tx.Copy()
	for i, o := range tx.TxOut {
		if txscript.IsPayToTaproot(o.PkScript) {
			p := wire.OutPoint{Hash: id, Index: uint32(i)}
			f.records[p] = ports.Vtxo{Outpoint: p, Script: bytes.Clone(o.PkScript), Amount: o.Value, Preconfirmed: true}
		}
	}
}

// accept indexes real source/checkpoint/main evidence only after the actual
// emulator accepts the builder's PSBTs, independently of the game reducer.
func (f *driverFixture) accept(i int, b *covenant.Unsigned) error {
	if err := f.h.execute(i, b); err != nil {
		return err
	}
	id := b.Ark.UnsignedTx.TxHash()
	if f.txs[id] != nil {
		return nil
	}
	for _, cp := range b.Checkpoints {
		previous := cp.UnsignedTx.TxIn[0].PreviousOutPoint
		v, ok := f.records[previous]
		if !ok || v.Spent {
			return errors.New("fixture source unavailable")
		}
		cpID := cp.UnsignedTx.TxHash()
		v.Spent = true
		v.SpentBy = &cpID
		v.ArkTxID = &id
		f.records[previous] = v
		f.txs[cpID] = cp.UnsignedTx.Copy()
	}
	f.indexOutputs(b.Ark.UnsignedTx)
	for sub := range f.subscriptions {
		select {
		case sub.events <- ports.ScriptEvent{Kind: ports.ScriptChanged, Script: bytes.Clone(sub.script), TxID: id, NewVtxos: []wire.OutPoint{{Hash: id}}, SpentVtxos: func() []wire.OutPoint {
			points := make([]wire.OutPoint, len(b.Checkpoints))
			for i, cp := range b.Checkpoints {
				points[i] = cp.UnsignedTx.TxIn[0].PreviousOutPoint
			}
			return points
		}()}:
		default:
		}
	}
	return nil
}
func (f *driverFixture) watched(i int) bool {
	for sub := range f.subscriptions {
		if sub.player == i {
			return true
		}
	}
	return false
}

type fixtureSubscriber struct {
	f      *driverFixture
	player int
}
type fixtureSubscription struct {
	f      *driverFixture
	player int
	script []byte
	events chan ports.ScriptEvent
	closed chan struct{}
	once   sync.Once
}

func (s fixtureSubscriber) Subscribe(ctx context.Context, scripts [][]byte) (ports.ScriptSubscription, error) {
	f := s.f
	f.mu.Lock()
	gate, err, started := f.attachGate, f.attachErr, f.attachStarted
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	if len(scripts) != 1 {
		return nil, ErrProtocol
	}
	sub := &fixtureSubscription{f: f, player: s.player, script: bytes.Clone(scripts[0]), events: make(chan ports.ScriptEvent, 64), closed: make(chan struct{})}
	f.mu.Lock()
	f.subscriptions[sub] = true
	f.trace = append(f.trace, fmt.Sprintf("attach:%d", s.player))
	f.mu.Unlock()
	return sub, nil
}
func (s *fixtureSubscription) Next(ctx context.Context) (ports.ScriptEvent, error) {
	select {
	case e := <-s.events:
		return e, nil
	case <-s.closed:
		return ports.ScriptEvent{}, context.Canceled
	case <-ctx.Done():
		return ports.ScriptEvent{}, ctx.Err()
	}
}
func (s *fixtureSubscription) Close() error {
	s.once.Do(func() { s.f.mu.Lock(); delete(s.f.subscriptions, s); s.f.mu.Unlock(); close(s.closed) })
	return nil
}

type fixtureDriverWallet struct {
	f                                               *driverFixture
	player                                          int
	config                                          wallet.Config
	key                                             *wallet.Key
	driver                                          *Driver
	signCalls, selectCalls, submitCalls, retryCalls int
	submitErr, retryErr                             error
	acceptBeforeError, delayAcceptance              bool
	mutatePolicy                                    bool
	signResult                                      func(ports.Bundle) ports.Bundle
	onSubmit                                        func(bool)
}

func (w *fixtureDriverWallet) Config() (wallet.Config, error) { return w.config, nil }
func (w *fixtureDriverWallet) AdmitAgreement(p covenant.Params) error {
	if w.mutatePolicy {
		p.Players.Player1.PayoutScript[0] ^= 1
		p.Players.Player2.PayoutScript[0] ^= 1
	}
	return nil
}
func (w *fixtureDriverWallet) ProveOwnership(ctx context.Context, d [32]byte) ([]byte, error) {
	return w.key.ProveOwnership(ctx, d)
}
func (w *fixtureDriverWallet) AuthenticateOwnership(ctx context.Context, k, d [32]byte, b []byte) error {
	return wallet.AuthenticateOwnership(ctx, k, d, b)
}
func (w *fixtureDriverWallet) SelectFunding(ctx context.Context, amount int64) (covenant.Funding, error) {
	if err := ctx.Err(); err != nil {
		return covenant.Funding{}, err
	}
	w.f.mu.Lock()
	defer w.f.mu.Unlock()
	w.selectCalls++
	if w.driver.journal.game.stage == StageInitialDeposit && !w.f.watched(w.player) {
		return covenant.Funding{}, errors.New("selection before deposit watch")
	}
	w.f.trace = append(w.f.trace, fmt.Sprintf("select:%d", w.player))
	funding := w.f.h.funding(w.player, amount)
	for _, v := range funding.Inputs {
		w.f.indexOutputs(v.PreviousTx)
	}
	return funding, nil
}
func (w *fixtureDriverWallet) Sign(ctx context.Context, b ports.Bundle) (ports.Bundle, error) {
	if err := ctx.Err(); err != nil {
		return ports.Bundle{}, err
	}
	w.f.mu.Lock()
	defer w.f.mu.Unlock()
	w.signCalls++
	if !w.f.watched(w.player) {
		return ports.Bundle{}, errors.New("sign before watch")
	}
	g := w.driver.journal.game
	if g.stage != StageTransactionPrepared || uint64(len(w.driver.config.Log.(*journalLog).records)) != g.nextEvent || !sameBundle(g.prepared.saved.Prepared, b) {
		return ports.Bundle{}, errors.New("sign before durable preparation")
	}
	w.f.trace = append(w.f.trace, fmt.Sprintf("sign:%d", w.player))
	// Missing transaction signatures intentionally exercise the approved opaque
	// comparison. Transaction signing itself is a later wallet qualification.
	if w.signResult != nil {
		return w.signResult(b), nil
	}
	return ports.Bundle{Ark: b.Ark, Checkpoints: append([]string(nil), b.Checkpoints...)}, nil
}
func (w *fixtureDriverWallet) Submit(ctx context.Context, r wallet.Route, b ports.Bundle) (ports.Submitted, error) {
	return w.submit(ctx, r, b, false)
}
func (w *fixtureDriverWallet) RetrySaved(ctx context.Context, r wallet.Route, b ports.Bundle) (ports.Submitted, error) {
	return w.submit(ctx, r, b, true)
}
func (w *fixtureDriverWallet) submit(ctx context.Context, r wallet.Route, b ports.Bundle, retry bool) (ports.Submitted, error) {
	if err := ctx.Err(); err != nil {
		return ports.Submitted{}, err
	}
	f := w.f
	f.mu.Lock()
	defer f.mu.Unlock()
	if retry {
		w.retryCalls++
	} else {
		w.submitCalls++
	}
	if !f.watched(w.player) {
		return ports.Submitted{}, errors.New("submit before watch")
	}
	g := w.driver.journal.game
	if g.prepared == nil || g.prepared.saved.Signed == nil || !sameBundle(*g.prepared.saved.Signed, b) || g.prepared.saved.Route != r || uint64(len(w.driver.config.Log.(*journalLog).records)) != g.nextEvent {
		return ports.Submitted{}, errors.New("submit before exact signed persistence")
	}
	f.trace = append(f.trace, fmt.Sprintf("submit:%d:%t", w.player, retry))
	if w.onSubmit != nil {
		w.onSubmit(retry)
	}
	err := w.submitErr
	if retry {
		err = w.retryErr
	}
	if !w.delayAcceptance && (err == nil || w.acceptBeforeError) {
		if e := f.accept(w.player, g.prepared.built); e != nil {
			return ports.Submitted{}, e
		}
	}
	if err != nil {
		return ports.Submitted{}, err
	}
	return ports.Submitted{TxID: g.prepared.built.Ark.UnsignedTx.TxHash().String()}, nil
}

type fixturePeer struct {
	f                                     *driverFixture
	player                                int
	driver                                *Driver
	key                                   *btcec.PrivateKey
	session                               ports.PeerSession
	prepareCalls, publishCalls, openCalls int
	publishErr                            error
}

func (p *fixturePeer) Open(ctx context.Context, s ports.PeerSession) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.openCalls++
	key, _ := btcec.PrivKeyFromBytes(s.TransportSecret[:])
	if p.key != nil && !bytes.Equal(p.key.Serialize(), key.Serialize()) {
		key.Zero()
		return errors.New("transport identity replaced")
	}
	if p.key != nil {
		p.key.Zero()
	}
	p.key = key
	p.session = s
	clear(p.session.TransportSecret[:])
	return nil
}
func (p *fixturePeer) Prepare(ctx context.Context, q ports.PeerRequest, b []byte, at int64) (ports.PreparedMessage, error) {
	if err := ctx.Err(); err != nil {
		return ports.PreparedMessage{}, err
	}
	p.prepareCalls++
	m, err := DecodeMessage(b)
	if err != nil {
		return ports.PreparedMessage{}, err
	}
	if p.key == nil || m.SessionID != SessionID(q.SessionID) || m.Sequence != q.Sequence || uint8(m.Sender) != q.Role {
		return ports.PreparedMessage{}, ErrProtocol
	}
	digest := sha256.Sum256(b)
	sig, err := schnorr.Sign(p.key, digest[:])
	if err != nil {
		return ports.PreparedMessage{}, err
	}
	carrier := append(schnorr.SerializePubKey(p.key.PubKey()), sig.Serialize()...)
	carrier = append(carrier, b...)
	return ports.PreparedMessage{Payload: bytes.Clone(b), Carrier: carrier}, nil
}
func decodeFixtureCarrier(b []byte) (Message, error) {
	if len(b) < 96 {
		return Message{}, ErrEncoding
	}
	public, err := schnorr.ParsePubKey(b[:32])
	if err != nil {
		return Message{}, err
	}
	sig, err := schnorr.ParseSignature(b[32:96])
	if err != nil {
		return Message{}, err
	}
	digest := sha256.Sum256(b[96:])
	if !sig.Verify(digest[:], public) {
		return Message{}, errors.New("unauthenticated fixture carrier")
	}
	m, err := DecodeMessage(b[96:])
	if err != nil {
		return Message{}, err
	}
	if m.Identity != [32]byte(b[:32]) {
		return Message{}, ErrProtocol
	}
	return m, nil
}
func relayKey(id SessionID, role covenant.Player, seq uint64) string {
	return fmt.Sprintf("%x/%d/%d", id, role, seq)
}
func (p *fixturePeer) Publish(ctx context.Context, b []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.publishCalls++
	if p.publishErr != nil {
		return p.publishErr
	}
	m, err := decodeFixtureCarrier(b)
	if err != nil {
		return err
	}
	f := p.f
	f.mu.Lock()
	defer f.mu.Unlock()
	g := p.driver.journal.game
	if g.setup.publication == nil || !bytes.Equal(g.setup.publication.Carrier, b) || uint64(len(p.driver.config.Log.(*journalLog).records)) != g.nextEvent {
		return errors.New("publication before durable carrier")
	}
	if m.Kind == FinalShuffle && !f.watched(p.player) {
		return errors.New("final publication before watch")
	}
	key := relayKey(m.SessionID, m.Sender, m.Sequence)
	if prior := f.relay[key]; prior != nil && !bytes.Equal(prior, b) {
		return errors.New("carrier equivocation")
	}
	f.relay[key] = bytes.Clone(b)
	close(f.relayChanged)
	f.relayChanged = make(chan struct{})
	f.trace = append(f.trace, fmt.Sprintf("publish:%d:%d", p.player, m.Kind))
	return nil
}
func (p *fixturePeer) Receive(ctx context.Context, q ports.PeerRequest) (*ports.PeerDelivery, error) {
	for {
		p.f.mu.Lock()
		b := bytes.Clone(p.f.relay[relayKey(SessionID(q.SessionID), covenant.Player(q.Role), q.Sequence)])
		changed := p.f.relayChanged
		p.f.mu.Unlock()
		if b != nil {
			m, err := decodeFixtureCarrier(b)
			if err != nil {
				return nil, err
			}
			return &ports.PeerDelivery{Identity: m.Identity, Payload: bytes.Clone(b[96:])}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}
func (p *fixturePeer) Close() error {
	if p.key != nil {
		p.key.Zero()
	}
	return nil
}

func (f *driverFixture) newDriver(t *testing.T, i int, records [][]byte) (*Driver, *fixtureDriverWallet, *fixturePeer, *journalLog) {
	t.Helper()
	config := f.h.games[i].config
	key, err := wallet.ParseKey(fmt.Sprintf("%064x", i+1), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Destroy)
	w := &fixtureDriverWallet{f: f, player: i, config: config.Wallet, key: key}
	p := &fixturePeer{f: f, player: i}
	log := &journalLog{}
	for _, b := range records {
		log.records = append(log.records, bytes.Clone(b))
	}
	d, err := NewDriver(DriverConfig{Game: config, Log: log, Wallet: w, Indexer: f, Subscriptions: fixtureSubscriber{f, i}, Transport: p, Now: func() time.Time { return time.Unix(1000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	w.driver = d
	p.driver = d
	t.Cleanup(func() { _ = d.Close() })
	return d, w, p, log
}

// importHistory supplies finalized indexer evidence for already executed real-VM
// histories, including old outputs spent onward. It never synthesizes a tip.
func (f *driverFixture) importHistory(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, tx := range f.h.known {
		f.indexOutputs(tx)
	}
	seen := map[chainhash.Hash]bool{}
	for _, b := range f.h.logs[0] {
		e, err := DecodeEvent(b)
		if err != nil {
			t.Fatal(err)
		}
		if e.Secrets != nil {
			_ = e.Secrets.Destroy()
		}
		if e.Accepted == nil {
			continue
		}
		a := e.Accepted
		id := a.Transaction.TxHash()
		if seen[id] {
			continue
		}
		seen[id] = true
		for _, cp := range a.Checkpoints {
			op := cp.TxIn[0].PreviousOutPoint
			v, ok := f.records[op]
			if !ok {
				t.Fatal("missing fixture history source")
			}
			cpID := cp.TxHash()
			v.Spent = true
			v.SpentBy = &cpID
			v.ArkTxID = &id
			f.records[op] = v
			f.txs[cpID] = cp.Copy()
		}
		f.indexOutputs(a.Transaction)
	}
}
func (f *driverFixture) savePending(t *testing.T, i int, input Input, signed bool) SavedSpend {
	t.Helper()
	g := f.h.games[i]
	amount, err := g.RequiredFunding(input)
	if err != nil {
		t.Fatal(err)
	}
	e, err := g.PrepareAction(context.Background(), nil, input, f.h.funding(i, amount), f.h.at)
	if err != nil {
		t.Fatal(err)
	}
	f.h.record(i, e)
	saved, err := g.PendingSpend()
	if err != nil {
		t.Fatal(err)
	}
	if signed {
		saved.Signed = &saved.Prepared
		e = g.NewEvent(SpendSigned)
		e.Spend = &saved
		f.h.record(i, e)
	}
	return saved
}
