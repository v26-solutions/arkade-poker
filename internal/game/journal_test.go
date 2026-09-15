package game

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"arkade-poker/go/internal/storage"
)

type journalLog struct {
	records         [][]byte
	before          func(uint64, []byte)
	fail            error
	commitOnFailure bool
}

func (l *journalLog) Load(context.Context) ([][]byte, error) {
	out := make([][]byte, len(l.records))
	for i, b := range l.records {
		out[i] = bytes.Clone(b)
	}
	return out, nil
}
func (l *journalLog) Append(_ context.Context, index uint64, b []byte) error {
	if index != uint64(len(l.records)) {
		return storage.ErrIndex
	}
	if l.before != nil {
		l.before(index, b)
	}
	if l.fail == nil || l.commitOnFailure {
		l.records = append(l.records, bytes.Clone(b))
	}
	return l.fail
}
func (*journalLog) Close() error { return nil }

func TestJournalDurableBoundary(t *testing.T) {
	ctx := context.Background()
	config := testConfig(1)
	log := new(journalLog)
	j, err := loadJournal(ctx, config, log)
	if err != nil {
		t.Fatal(err)
	}
	step, err := j.game.Decide(Input{Kind: Progress})
	if err != nil {
		t.Fatal(err)
	}
	log.before = func(index uint64, b []byte) {
		if j.game.nextEvent != index {
			t.Fatal("advanced before acknowledgement")
		}
		expected, err := EncodeEvent(*step.Event)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, expected) {
			t.Fatal("wrote different event")
		}
	}
	if err := j.append(ctx, *step.Event); err != nil {
		t.Fatal(err)
	}
	log.before = nil
	e, err := j.game.PrepareSession(ctx, nil, Input{Kind: StartSession, Terms: testTerms, RelayURL: "ws://localhost:7777"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Secrets.Destroy()
	rejected := e
	rejected.Sequence++
	if err := j.append(ctx, rejected); err == nil || len(log.records) != 1 {
		t.Fatal("persisted rejected candidate")
	}
	before, _ := j.game.Snapshot()
	fault := errors.New("durability failure")
	log.fail = fault
	if err := j.append(ctx, e); !errors.Is(err, fault) {
		t.Fatal(err)
	}
	after, _ := j.game.Snapshot()
	if j.game.nextEvent != 1 || !reflect.DeepEqual(before, after) {
		t.Fatal("failure advanced game")
	}
	if err := j.append(ctx, e); !errors.Is(err, storage.ErrUncertain) {
		t.Fatal("continued after append error")
	}
	log.fail = nil
	restored, err := loadJournal(ctx, config, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.append(ctx, e); err != nil {
		t.Fatal(err)
	}
	defer discardGame(restored.game, nil)
	replayed, err := loadJournal(ctx, config, log)
	if err != nil {
		t.Fatal(err)
	}
	defer discardGame(replayed.game, nil)
	if replayed.game.stage != StageSessionPrepared || replayed.game.previous != restored.game.previous {
		t.Fatal("restored different session")
	}
	if _, err := loadJournal(ctx, testConfig(2), log); !errors.Is(err, ErrWalletIdentity) {
		t.Fatalf("wrong wallet: %v", err)
	}
	// Runtime endpoints are authoritative even when a journal already exists.
	changed := testConfig(1)
	changed.ArkdURL = "http://localhost:9999"
	replayedAgain, err := loadJournal(ctx, changed, log)
	if err != nil {
		t.Fatal(err)
	}
	defer discardGame(replayedAgain.game, nil)
	if replayedAgain.game.config.ArkdURL != changed.ArkdURL {
		t.Fatal("journal overrode runtime config")
	}
}

func TestJournalUncertainAppendAndCancellation(t *testing.T) {
	config := testConfig(1)
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "not_committed", true: "committed_without_ack"}[committed], func(t *testing.T) {
			log := &journalLog{commitOnFailure: committed, fail: errors.New("lost acknowledgement")}
			j, err := loadJournal(context.Background(), config, log)
			if err != nil {
				t.Fatal(err)
			}
			step, _ := j.game.Decide(Input{Kind: Progress})
			if err := j.append(context.Background(), *step.Event); err == nil {
				t.Fatal("missing failure")
			}
			if j.game.nextEvent != 0 {
				t.Fatal("unacknowledged live advancement")
			}
			log.fail = nil
			restored, err := loadJournal(context.Background(), config, log)
			if err != nil {
				t.Fatal(err)
			}
			want := uint64(0)
			if committed {
				want = 1
			}
			if restored.game.nextEvent != want {
				t.Fatal("restored wrong durable prefix")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	log := &journalLog{before: func(uint64, []byte) { cancel() }}
	j, err := loadJournal(ctx, config, log)
	if err != nil {
		t.Fatal(err)
	}
	step, _ := j.game.Decide(Input{Kind: Progress})
	if err := j.append(ctx, *step.Event); err != nil {
		t.Fatal(err)
	}
	if j.game.nextEvent != 1 {
		t.Fatal("discarded successful durable commit after cancellation")
	}
}

func TestJournalReplaysActualSignedWork(t *testing.T) {
	h := newHandHarness(t)
	h.action(1, Input{Kind: Progress})
	actor := h.games[0]
	e, err := actor.PrepareAction(context.Background(), nil, Input{Kind: Progress}, h.funding(0, 1100), 1000)
	if err != nil {
		t.Fatal(err)
	}
	h.record(0, e)
	// Use the exact prepared bundle, intentionally with no signature validity
	// assertion, to test the approved signature-opaque replay boundary.
	pending, err := actor.PendingSpend()
	if err != nil {
		t.Fatal(err)
	}
	pending.Signed = &pending.Prepared
	signed := actor.NewEvent(SpendSigned)
	signed.Spend = &pending
	h.record(0, signed)
	log := &journalLog{records: h.logs[0]}
	restored, err := loadJournal(context.Background(), actor.config, log)
	if err != nil {
		t.Fatal(err)
	}
	defer discardGame(restored.game, nil)
	got, err := restored.game.PendingSpend()
	if err != nil || !sameSaved(pending, got) || got.Signed == nil || !sameBundle(*pending.Signed, *got.Signed) {
		t.Fatalf("lost exact work: %v", err)
	}
	log.records[len(log.records)-1] = bytes.Clone(log.records[len(log.records)-1])
	log.records[len(log.records)-1][20] ^= 1
	if _, err := loadJournal(context.Background(), actor.config, log); err == nil {
		t.Fatal("replayed damaged record")
	}
}
