package storage

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
)

func TestDurableLogAndWriterOwnership(t *testing.T) {
	ctx := context.Background()
	place := testLocation(t)
	wallet := [32]byte{2}
	log, err := Open(ctx, place, wallet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	if _, err := Open(ctx, place, wallet); !errors.Is(err, ErrLocked) {
		t.Fatalf("second writer: %v", err)
	}
	if err := log.Append(ctx, 1, []byte("gap")); !errors.Is(err, ErrIndex) {
		t.Fatalf("gap: %v", err)
	}
	if err := log.Append(ctx, 0, nil); !errors.Is(err, ErrRecord) {
		t.Fatalf("empty: %v", err)
	}
	if err := log.Append(ctx, 0, make([]byte, MaxRecordBytes+1)); !errors.Is(err, ErrRecord) {
		t.Fatalf("oversized: %v", err)
	}
	input := []byte("private session data")
	if err := log.Append(ctx, 0, input); err != nil {
		t.Fatal(err)
	}
	clear(input)
	if err := log.Append(ctx, 0, []byte("replacement")); !errors.Is(err, ErrIndex) {
		t.Fatalf("overwrite: %v", err)
	}
	if err := log.Append(ctx, 1, bytes.Repeat([]byte{0xff}, MaxRecordBytes)); err != nil {
		t.Fatal(err)
	}
	records, err := log.Load(ctx)
	if err != nil || len(records) != 2 || string(records[0]) != "private session data" || len(records[1]) != MaxRecordBytes {
		t.Fatalf("load: %v", err)
	}
	clear(records[0])
	clear(records[1])
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Load(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed read: %v", err)
	}
	if err := log.Append(ctx, 2, []byte("late")); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed append: %v", err)
	}
	log, err = Open(ctx, place, wallet)
	if err != nil {
		t.Fatal(err)
	}
	records, err = log.Load(ctx)
	if err != nil || len(records) != 2 || string(records[0]) != "private session data" || records[1][0] != 0xff {
		t.Fatalf("restore: %v", err)
	}
	if err := log.Append(ctx, 2, []byte("resumed")); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, place, [32]byte{3})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	records, err = other.Load(ctx)
	if err != nil || len(records) != 0 {
		t.Fatalf("wallet isolation: %v", err)
	}
}

func TestCancellationAndConcurrentAppend(t *testing.T) {
	ctx := context.Background()
	place := testLocation(t)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Open(cancelled, place, [32]byte{4}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open: %v", err)
	}
	log, err := Open(ctx, place, [32]byte{4})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if err := log.Append(cancelled, 0, []byte("cancelled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled append: %v", err)
	}
	if _, err := log.Load(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < cap(results); i++ {
		wg.Go(func() { results <- log.Append(ctx, 0, []byte("once")) })
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrIndex) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("concurrent append successes: %d", success)
	}
	records, err := log.Load(ctx)
	if err != nil || len(records) != 1 || string(records[0]) != "once" {
		t.Fatalf("concurrent read: %v", err)
	}
}

func TestRecordFrameIntegrity(t *testing.T) {
	wallet := [32]byte{5}
	b, err := frame(wallet, 9, []byte("opaque private event"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range b {
		bad := bytes.Clone(b)
		bad[i] ^= 1
		if _, err := unframe(wallet, 9, bad); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("byte %d: %v", i, err)
		}
	}
	for n := 0; n < len(b); n++ {
		if _, err := unframe(wallet, 9, b[:n]); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("prefix %d", n)
		}
	}
	if _, err := unframe(wallet, 10, b); !errors.Is(err, ErrCorrupt) {
		t.Fatal("index substitution")
	}
	if _, err := unframe([32]byte{6}, 9, b); !errors.Is(err, ErrCorrupt) {
		t.Fatal("wallet substitution")
	}
	if _, err := unframe(wallet, 9, append(bytes.Clone(b), 0)); !errors.Is(err, ErrCorrupt) {
		t.Fatal("trailing data")
	}
}

func TestClearWalletHistory(t *testing.T) {
	ctx := context.Background()
	place := testLocation(t)
	public, otherPublic := [32]byte{2}, [32]byte{3}
	log, err := Open(ctx, place, public)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	other, err := Open(ctx, place, otherPublic)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for _, l := range []Log{log, other} {
		if err := l.Append(ctx, 0, []byte("saved hand")); err != nil {
			t.Fatal(err)
		}
	}
	if err := Clear(ctx, place, public); !errors.Is(err, ErrLocked) {
		t.Fatalf("cleared an owned wallet: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := Clear(cancelled, place, public); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled clear: %v", err)
	}
	log, err = Open(ctx, place, public)
	if err != nil {
		t.Fatal(err)
	}
	records, err := log.Load(ctx)
	if err != nil || len(records) != 1 {
		t.Fatal("rejected clear changed history", err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Clear(ctx, place, public); err != nil {
			t.Fatal(err)
		}
	}
	log, err = Open(ctx, place, public)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	records, err = log.Load(ctx)
	if err != nil || len(records) != 0 {
		t.Fatal("clear did not persist", err)
	}
	if err := log.Append(ctx, 0, []byte("fresh hand")); err != nil {
		t.Fatal("clear did not reset append index", err)
	}
	records, err = other.Load(ctx)
	if err != nil || len(records) != 1 || string(records[0]) != "saved hand" {
		t.Fatal("clear changed another wallet", err)
	}
}
