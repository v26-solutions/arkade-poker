package game

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
	"arkade-poker/go/internal/shuffle"
	"arkade-poker/go/internal/wallet"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

func publicScalar(n byte) [32]byte {
	b := make([]byte, 32)
	b[31] = n
	sk, _ := btcec.PrivKeyFromBytes(b)
	defer sk.Zero()
	return [32]byte(schnorr.SerializePubKey(sk.PubKey()))
}
func testConfig(n byte) Config {
	key := publicScalar(n)
	serverKey := publicScalar(3)
	server, _ := schnorr.ParsePubKey(serverKey[:])
	checkpoint := &script.CSVMultisigClosure{MultisigClosure: script.MultisigClosure{PubKeys: []*btcec.PublicKey{server}}, Locktime: arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 72}}
	cp, err := checkpoint.Script()
	if err != nil {
		panic(err)
	}
	return Config{Wallet: wallet.Config{Network: "regtest", WalletPublicKey: key, ArkSigningKey: publicScalar(3), EmulatorSigningKey: publicScalar(4), CheckpointScript: cp,
		Receive: wallet.Receive{Address: "fixture", Script: append([]byte{0x51, 0x20}, key[:]...), Tapscripts: []string{"51"}}, OutputPolicy: wallet.OutputPolicy{MinAmount: 1, MaxAmount: maxMoney}}, ArkdURL: "http://localhost:7070", EmulatorURL: "http://localhost:7071", IndexerURL: "http://localhost:7070"}
}

var testTerms = Terms{Stake: 1000, Bond: 100, MinBet: 10, MaxWager: 1000}

func applyRecord(t *testing.T, g *Game, e Event, log *[][]byte) {
	t.Helper()
	b, err := EncodeEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEvent(b)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if decoded.Secrets != nil {
			_ = decoded.Secrets.Destroy()
		}
	}()
	if err := g.Apply(decoded); err != nil {
		t.Fatal(err)
	}
	if log != nil {
		*log = append(*log, b)
	}
}
func initialized(t *testing.T, c Config) (*Game, [][]byte) {
	t.Helper()
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	step, err := g.Decide(Input{Kind: Progress})
	if err != nil || step.Kind != RecordEvent {
		t.Fatalf("config step: %v %v", step, err)
	}
	var log [][]byte
	applyRecord(t, g, *step.Event, &log)
	return g, log
}

type pairFixture struct {
	logs           [2][][]byte
	stages         [2][]Stage
	configs        [2]Config
	contractID     covenant.ContractID
	contractScript []byte
}

var fixtureOnce sync.Once
var fixture pairFixture

func completeSetup(t *testing.T) pairFixture {
	t.Helper()
	fixtureOnce.Do(func() {
		c1, c2 := testConfig(1), testConfig(2)
		c2.ArkdURL = "grpc://localhost:7070" // Host endpoint syntax is not the common service commitment.
		g1, l1 := initialized(t, c1)
		g2, l2 := initialized(t, c2)
		defer func() {
			if g1.setup != nil {
				_ = g1.setup.secrets.Destroy()
			}
			if g2.setup != nil {
				_ = g2.setup.secrets.Destroy()
			}
		}()
		e1, err := g1.PrepareSession(context.Background(), nil, Input{Kind: StartSession, Terms: testTerms, RelayURL: "wss://relay.example:443/path?query=1"})
		if err != nil {
			t.Fatal(err)
		}
		defer e1.Secrets.Destroy()
		e2, err := g2.PrepareSession(context.Background(), nil, Input{Kind: JoinSession, Invitation: e1.Invitation})
		if err != nil {
			t.Fatal(err)
		}
		defer e2.Secrets.Destroy()
		// Opaque signature bytes affect transcripts, but the FSM does not validate them.
		e1.Message.WalletOwnership = []byte{0xff}
		e2.Message.WalletOwnership = []byte{0x00, 0x01}
		applyRecord(t, g1, e1, &l1)
		applyRecord(t, g2, e2, &l2)
		applyRecord(t, g1, g1.NewEvent(SessionOpened), &l1)
		applyRecord(t, g2, g2.NewEvent(SessionOpened), &l2)
		send := func(sender, recipient *Game, sl, rl *[][]byte, at covenant.UnixSeconds) {
			m, err := sender.PendingMessage()
			if err != nil {
				t.Fatal(err)
			}
			payload, err := EncodeMessage(m)
			if err != nil {
				t.Fatal(err)
			}
			publication := sender.NewEvent(PublicationPrepared)
			publication.Publication = &ports.PreparedMessage{Payload: payload, Carrier: []byte("opaque fixture carrier")}
			applyRecord(t, sender, publication, sl)
			sent := sender.NewEvent(MessagePublished)
			sent.Message = &m
			applyRecord(t, sender, sent, sl)
			received := recipient.NewEvent(MessageReceived)
			received.Message = &m
			received.ObservedAt = at
			applyRecord(t, recipient, received, rl)
		}
		send(g2, g1, &l2, &l1, 0)
		send(g1, g2, &l1, &l2, 0)
		initial, err := g2.PrepareShuffle(context.Background(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		applyRecord(t, g2, initial, &l2)
		send(g2, g1, &l2, &l1, 0)
		clockCalls := 0
		final, err := g1.PrepareShuffle(context.Background(), nil, func() (covenant.UnixSeconds, error) { clockCalls++; return 1000, nil })
		if err != nil {
			t.Fatal(err)
		}
		if clockCalls != 1 {
			t.Fatal("final deadline observation")
		}
		if final.Message.InitialDeadline != 1060 {
			t.Fatalf("initial deadline: got %d, want 1060", final.Message.InitialDeadline)
		}
		applyRecord(t, g1, final, &l1)
		step, err := g1.Decide(Input{Kind: Progress})
		if err != nil || step.Effect != WatchAndPublishFinalEffect {
			t.Fatalf("final must attach before publishing: %v %v", step, err)
		}
		send(g1, g2, &l1, &l2, 1000)
		contract1, _ := g1.AgreedContract()
		contract2, _ := g2.AgreedContract()
		id1, _ := contract1.ID()
		id2, _ := contract2.ID()
		script1, _ := contract1.ScriptPubKey()
		script2, _ := contract2.ScriptPubKey()
		if id1 != id2 || !bytes.Equal(script1, script2) {
			t.Fatal("two players derived different agreements")
		}
		if g1.stage != StageAwaitInitialDeposit || g2.stage != StageInitialDeposit {
			t.Fatal("deposit ordering")
		}
		fixture = pairFixture{logs: [2][][]byte{l1, l2}, configs: [2]Config{c1, c2}, contractID: id1, contractScript: script1,
			stages: [2][]Stage{
				{StageInit, StageSessionPrepared, StageAwaitOpponentKeys, StagePlayer1KeysPrepared, StagePlayer1KeysPrepared, StageAwaitInitialShuffle, StageFinalShuffle, StageFinalShufflePrepared, StageFinalShufflePrepared, StageAwaitInitialDeposit},
				{StageInit, StageSessionPrepared, StagePlayer2KeysPrepared, StagePlayer2KeysPrepared, StageAwaitOpponentKeys, StageInitialShuffle, StageInitialShufflePrepared, StageInitialShufflePrepared, StageAwaitFinalShuffle, StageInitialDeposit}}}
	})
	return fixture
}
func replayBytes(t *testing.T, config Config, log [][]byte) *Game {
	t.Helper()
	var events []Event
	defer func() {
		for _, e := range events {
			if e.Secrets != nil {
				_ = e.Secrets.Destroy()
			}
		}
	}()
	for _, b := range log {
		e, err := DecodeEvent(b)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	g, err := Replay(config, events)
	if err != nil {
		t.Fatal(err)
	}
	if g.setup != nil {
		t.Cleanup(func() { _ = g.setup.secrets.Destroy() })
	}
	return g
}

func TestCompleteSetupAndEveryReplayPrefix(t *testing.T) {
	f := completeSetup(t)
	for role, log := range f.logs {
		for i := 1; i <= len(log); i++ {
			g := replayBytes(t, f.configs[role], log[:i])
			if g.stage != f.stages[role][i-1] {
				t.Fatalf("role %d prefix %d stage %d", role+1, i, g.stage)
			}
			if g.nextEvent != uint64(i) || g.previous != digest("", log[i-1]) {
				t.Fatal("replay cursor")
			}
			snap, err := g.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if snap.Cards != (covenant.DealtCards[KnownCard]{}) {
				t.Fatal("setup disclosed cards")
			}
			if g.stage == StageInitialShufflePrepared || g.stage == StageFinalShufflePrepared {
				pending, err := g.PendingMessage()
				if err != nil {
					t.Fatal(err)
				}
				prepared, _ := DecodeEvent(log[i-1])
				if prepared.Kind == PublicationPrepared {
					message, err := DecodeMessage(prepared.Publication.Payload)
					if err != nil {
						t.Fatal(err)
					}
					prepared.Message = &message
				}
				if !sameMessage(pending, *prepared.Message) {
					t.Fatal("replay replaced prepared proof")
				}
			}
		}
		g := replayBytes(t, f.configs[role], log)
		contract, _ := g.AgreedContract()
		id, _ := contract.ID()
		if id != f.contractID {
			t.Fatal("replayed contract changed")
		}
	}
}

func setupEventIndex(t *testing.T, log [][]byte, kind EventKind, messageKind MessageKind) int {
	t.Helper()
	for i, b := range log {
		e, err := DecodeEvent(b)
		if err != nil {
			t.Fatal(err)
		}
		if e.Secrets != nil {
			_ = e.Secrets.Destroy()
		}
		if e.Kind == kind && (messageKind == 0 || e.Message != nil && e.Message.Kind == messageKind) {
			return i
		}
	}
	t.Fatal("missing fixture event")
	return 0
}

func TestSetupRejectionsAreAtomic(t *testing.T) {
	f := completeSetup(t)
	offer := setupEventIndex(t, f.logs[0], MessageReceived, KeyOffer)
	reply := setupEventIndex(t, f.logs[1], MessageReceived, KeyReply)
	sentOffer := setupEventIndex(t, f.logs[1], MessagePublished, KeyOffer)
	sentReply := setupEventIndex(t, f.logs[0], MessagePublished, KeyReply)
	initial := setupEventIndex(t, f.logs[0], MessageReceived, InitialShuffle)
	final := setupEventIndex(t, f.logs[1], MessageReceived, FinalShuffle)
	tests := []struct {
		name        string
		role, index int
		mutate      func(*Event)
	}{
		{"wrong event sequence", 0, offer, func(e *Event) { e.Sequence++ }},
		{"wrong previous hash", 0, offer, func(e *Event) { e.Previous[0] ^= 1 }},
		{"foreign session", 0, offer, func(e *Event) { e.SessionID[0] ^= 1 }},
		{"foreign message session", 0, offer, func(e *Event) { e.Message.SessionID[0] ^= 1 }},
		{"skipped message", 0, offer, func(e *Event) { e.Message.Sequence++ }},
		{"wrong role", 0, offer, func(e *Event) { e.Message.Sender = covenant.Player1 }},
		{"duplicate wallet", 0, offer, func(e *Event) { e.Message.Participant.SigningKey = publicScalar(1) }},
		{"wrong payout", 0, offer, func(e *Event) { e.Message.Participant.PayoutScript[2] ^= 1 }},
		{"invalid payout policy", 0, offer, func(e *Event) { e.Message.Participant.PayoutScript = []byte{0x51} }},
		{"ownership identity substitution", 0, offer, func(e *Event) { e.Message.Identity = publicScalar(10) }},
		{"creator substitution", 1, reply, func(e *Event) { e.Message.Identity = publicScalar(11) }},
		{"unpublished keys", 1, sentOffer, func(e *Event) { e.Kind = MessageReceived }},
		{"changed publication", 0, sentReply, func(e *Event) { e.Message.WalletOwnership = []byte{2} }},
		{"changed deck", 0, initial, func(e *Event) { d := *e.Message.Deck; d[0], d[1] = d[1], d[0]; e.Message.Deck = &d }},
		{"changed proof", 0, initial, func(e *Event) {
			b, _ := e.Message.ShuffleProof.MarshalBinary()
			b[len(b)-1] ^= 1
			p, err := shuffle.DecodeShuffleProof(b)
			if err != nil {
				t.Fatal(err)
			}
			e.Message.ShuffleProof = &p
		}},
		{"wrong final sequence", 1, final, func(e *Event) { e.Message.Sequence = 0 }},
		{"early final deadline", 1, final, func(e *Event) { e.Message.InitialDeadline = 1029 }},
		{"late final deadline", 1, final, func(e *Event) { e.Message.InitialDeadline = 1091 }},
		{"overflow observation", 1, final, func(e *Event) { e.ObservedAt = math.MaxUint64 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := replayBytes(t, f.configs[tt.role], f.logs[tt.role][:tt.index])
			e, err := DecodeEvent(f.logs[tt.role][tt.index])
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(&e)
			before, _ := g.Snapshot()
			cursor, previous := g.nextEvent, g.previous
			if err := g.Apply(e); err == nil {
				t.Fatal("accepted changed evidence")
			}
			after, _ := g.Snapshot()
			if !reflect.DeepEqual(before, after) || cursor != g.nextEvent || previous != g.previous {
				t.Fatal("rejected event mutated game")
			}
			good, _ := DecodeEvent(f.logs[tt.role][tt.index])
			if err := g.Apply(good); err != nil {
				t.Fatalf("rejection advanced hidden state: %v", err)
			}
		})
	}
}

func TestInvitationsConfigAndSecrets(t *testing.T) {
	c := testConfig(1)
	inv, s, err := CreateInvitation(context.Background(), nil, c, testTerms, "ws://relay.example/a?x=1")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Destroy()
	text, err := EncodeInvitation(inv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeInvitation(text)
	if err != nil || got != inv {
		t.Fatal("invitation roundtrip", err)
	}
	for _, bad := range []string{text + "00", strings.ToUpper(text), "arkpg1:00", "arkp1:" + text[7:], text[:len(text)-1]} {
		if _, err := DecodeInvitation(bad); err == nil {
			t.Fatal("bad invitation", bad)
		}
	}
	for _, url := range []string{"", "https://relay.example", "ws:///bad", "wss://:80", "wss://user@host", "wss://host/#f", "wss://host/\\x", "ws://host/a b", "ws://host/\x00", string([]byte{'w', 's', ':', '/', '/', 0xff})} {
		if validateRelay(url) == nil {
			t.Fatal("relay syntax accepted")
		}
	}
	for _, terms := range []Terms{{}, {1, 1, 2, 1}, {maxMoney, 1, 1, 1}, {1, -1, 1, 1}, {1, 1, 1, math.MaxInt64}} {
		if terms.Validate() == nil {
			t.Fatal("invalid terms")
		}
	}
	changed := c
	changed.Wallet.Network = "signet"
	if _, err := AcceptInvitation(context.Background(), nil, changed, inv); err == nil {
		t.Fatal("foreign service")
	}
	b, err := s.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(b)
	restored, err := DecodeSessionSecrets(b)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Destroy()
	keys, _ := s.KeyMessage(c, inv, covenant.Player1)
	keys2, _ := restored.KeyMessage(c, inv, covenant.Player1)
	if !sameMessage(keys, keys2) {
		t.Fatal("secret restore replaced ownership proof")
	}
	pub, _ := EncodeMessage(keys)
	if bytes.Contains(pub, b[2:34]) || bytes.Contains(pub, b[34:66]) {
		t.Fatal("secret in public message")
	}
	for _, data := range [][]byte{b[:130], append(bytes.Clone(b), 0), make([]byte, 131)} {
		if bad, err := DecodeSessionSecrets(data); err == nil {
			_ = bad.Destroy()
			t.Fatal("invalid secret encoding")
		}
	}
	if fmt.Sprintf("%+v", *s) != "<game session secrets>" {
		t.Fatal("secret diagnostics")
	}
	alias := *s
	_ = alias.Destroy()
	if _, err := s.MarshalBinary(); !errors.Is(err, ErrSecrets) {
		t.Fatal("copy bypassed erasure")
	}
}

type failEntropy struct{}

func (failEntropy) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestSetupCancellationAndEntropy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, r := range []io.Reader{nil, failEntropy{}} {
		if inv, s, err := CreateInvitation(ctx, r, testConfig(1), testTerms, "ws://host"); !errors.Is(err, context.Canceled) || s != nil || inv != (Invitation{}) {
			t.Fatal("cancelled setup returned work", err)
		}
	}
	if _, s, err := CreateInvitation(context.Background(), failEntropy{}, testConfig(1), testTerms, "ws://host"); !errors.Is(err, io.ErrUnexpectedEOF) || s != nil {
		t.Fatal("entropy failure", err)
	}
	for _, tt := range []struct {
		now, deadline uint64
		ok            bool
	}{{1000, 1030, true}, {1000, 1060, true}, {1000, 1090, true}, {1000, 1029, false}, {1000, 1091, false}, {1000, 1300, false}, {math.MaxUint64 - 90, math.MaxUint64, true}, {math.MaxUint64 - 89, math.MaxUint64, false}} {
		if (deadlineWindow(covenant.UnixSeconds(tt.now), covenant.UnixSeconds(tt.deadline)) == nil) != tt.ok {
			t.Fatal("deadline window")
		}
	}
}
