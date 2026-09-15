package game

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
)

func setupPrefix(t *testing.T, i int, stage Stage) [][]byte {
	t.Helper()
	f := completeSetup(t)
	for n, s := range f.stages[i] {
		if s == stage {
			return append([][]byte(nil), f.logs[i][:n+1]...)
		}
	}
	t.Fatal("missing fixture stage")
	return nil
}

func TestDriverSavedPublicationAndDiscoveryBeforeExpiry(t *testing.T) {
	f := newDriverFixture(t)
	d, _, peer, log := f.newDriver(t, 0, setupPrefix(t, 0, StageFinalShufflePrepared))
	if err := d.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	step, err := d.step(context.Background(), Input{Kind: Progress})
	if err != nil || step.Event == nil || step.Event.Kind != PublicationPrepared {
		t.Fatalf("carrier preparation: %v", err)
	}
	if err := d.commit(context.Background(), *step.Event); err != nil {
		t.Fatal(err)
	}
	saved, err := d.journal.game.PendingPublication()
	if err != nil {
		t.Fatal(err)
	}
	if peer.prepareCalls != 1 || peer.publishCalls != 0 {
		t.Fatal("published before durable carrier")
	}
	// Publication reaches the relay, but its acknowledgement is never recorded.
	if err := peer.Publish(context.Background(), saved.Carrier); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	// An accepted deposit can already exist before P1 resumes the lost ACK.
	f.h.action(1, Input{Kind: Progress})
	f.importHistory(t)
	restored, w, p2, _ := f.newDriver(t, 0, log.records)
	restored.config.Now = func() time.Time { return time.Unix(5000, 0) }
	restored.config.Entropy = unexpectedEntropy{}
	if err := restored.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	step, err = restored.step(context.Background(), Input{Kind: Progress})
	if err != nil || step.Event == nil || step.Event.Kind != MessagePublished {
		t.Fatalf("exact publication retry: %v", err)
	}
	if p2.prepareCalls != 0 || p2.publishCalls != 1 {
		t.Fatal("publication retry generated a new signature")
	}
	if err := restored.commit(context.Background(), *step.Event); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	actual := bytes.Clone(f.relay[relayKey(SessionID(step.Event.Message.SessionID), covenant.Player1, 1)])
	f.mu.Unlock()
	if !bytes.Equal(actual, saved.Carrier) {
		t.Fatal("publication retry changed carrier bytes")
	}
	step, err = restored.step(context.Background(), Input{Kind: Progress})
	if err != nil || step.Event == nil || step.Event.Kind != DepositObserved {
		t.Fatalf("expiry hid prior accepted deposit: %v", err)
	}
	if err := restored.commit(context.Background(), *step.Event); err != nil {
		t.Fatal(err)
	}
	if w.selectCalls != 0 || w.signCalls != 0 || w.submitCalls != 0 {
		t.Fatal("catchup funded new work")
	}
}

func TestDriverCarrierAdmissionAndFailedDurability(t *testing.T) {
	f := newDriverFixture(t)
	d, _, peer, log := f.newDriver(t, 0, setupPrefix(t, 0, StageFinalShufflePrepared))
	if err := d.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	step, err := d.step(context.Background(), Input{Kind: Progress})
	if err != nil {
		t.Fatal(err)
	}
	event := *step.Event
	changed := event
	payload := *event.Publication
	changed.Publication = &payload
	payload.Payload = bytes.Clone(payload.Payload)
	payload.Payload[len(payload.Payload)-1] ^= 1
	before := d.journal.game.nextEvent
	if err := d.commit(context.Background(), changed); err == nil || d.journal.game.nextEvent != before {
		t.Fatal("carrier changed prepared message")
	}
	missing := d.journal.game.NewEvent(MessagePublished)
	message, _ := d.journal.game.PendingMessage()
	missing.Message = &message
	if err := d.commit(context.Background(), missing); err == nil {
		t.Fatal("publication ACK without saved carrier")
	}
	fault := errors.New("carrier durable write failed")
	log.fail = fault
	if err := d.commit(context.Background(), event); !errors.Is(err, fault) {
		t.Fatal(err)
	}
	if peer.publishCalls != 0 || d.journal.game.setup.publication != nil {
		t.Fatal("failed carrier append advanced or published")
	}
	payload = *event.Publication
	payload.Carrier = make([]byte, MaxCarrierBytes+1)
	event.Publication = &payload
	if _, err := EncodeEvent(event); !errors.Is(err, ErrEncoding) {
		t.Fatal("oversized carrier admitted")
	}
}

func TestDriverSubscriptionGateAndGap(t *testing.T) {
	f := newDriverFixture(t)
	f.h.start(0)
	f.savePending(t, 0, Input{Kind: Concede}, false)
	f.importHistory(t)
	d, w, _, _ := f.newDriver(t, 0, f.h.logs[0])
	gate := make(chan struct{})
	f.attachGate = gate
	started := make(chan struct{}, 1)
	f.attachStarted = started
	done := make(chan error, 1)
	go func() { done <- d.Restore(context.Background()) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("no subscription attempt")
	}
	select {
	case err := <-done:
		t.Fatalf("restore bypassed attachment: %v", err)
	default:
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if w.signCalls != 0 || w.submitCalls != 0 {
		t.Fatal("restore signed unsigned saved work")
	}
	old := d.watch
	old.sub.(*fixtureSubscription).events <- ports.ScriptEvent{Kind: ports.ScriptObservationGap}
	select {
	case <-old.changed:
	case <-time.After(time.Second):
		t.Fatal("gap not observed")
	}
	step, err := d.step(context.Background(), Input{Kind: Progress})
	if err != nil || step.Event == nil || step.Event.Kind != SpendSigned {
		t.Fatal("gap reconciliation failed", err)
	}
	if d.watch == old || d.watch.gap.Load() || w.signCalls != 1 {
		t.Fatal("signing resumed without fresh attachment")
	}
	f.mu.Lock()
	f.attachErr = errors.New("reattach failed")
	f.mu.Unlock()
	current := d.watch
	current.sub.(*fixtureSubscription).events <- ports.ScriptEvent{Kind: ports.ScriptObservationGap}
	select {
	case <-current.changed:
	case <-time.After(time.Second):
		t.Fatal("second gap not observed")
	}
	if err := d.commit(context.Background(), *step.Event); err != nil {
		t.Fatal(err)
	}
	if _, err := d.step(context.Background(), Input{Kind: Progress}); err == nil || w.submitCalls != 0 {
		t.Fatal("submit after failed attachment")
	}
}

func TestDriverAmbiguousDepositsAndFreshDeadline(t *testing.T) {
	for _, mode := range []string{"none", "ambiguous", "late_accepted"} {
		t.Run(mode, func(t *testing.T) {
			f := newDriverFixture(t)
			n := 0
			if mode == "ambiguous" {
				n = 2
			}
			if mode == "late_accepted" {
				n = 1
			}
			for range n {
				funding := f.h.funding(1, 1100)
				for _, source := range funding.Inputs {
					f.indexOutputs(source.PreviousTx)
				}
				built, err := f.h.games[1].setup.contract.InitialDeposit(funding)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.accept(1, built); err != nil {
					t.Fatal(err)
				}
			}
			d, w, _, _ := f.newDriver(t, 0, f.h.logs[0])
			d.config.Now = func() time.Time { return time.Unix(5000, 0) }
			if err := d.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			step, err := d.step(context.Background(), Input{Kind: Progress})
			if mode == "ambiguous" {
				if err == nil {
					t.Fatal("ambiguous deposits chose one")
				}
				return
			}
			if err != nil || step.Event == nil {
				t.Fatal("missing deposit decision", err)
			}
			want := SetupAborted
			if mode == "late_accepted" {
				want = DepositObserved
			}
			if step.Event.Kind != want || w.selectCalls != 0 {
				t.Fatal("deposit discovery/expiry ordering")
			}
		})
	}
}
