//go:build js && wasm

package storage

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"syscall/js"
	"testing"
	"time"
)

func testLocation(t *testing.T) string {
	t.Helper()
	if js.Global().Get("indexedDB").Type() != js.TypeObject {
		t.Skip("requires real browser IndexedDB and Web Locks")
	}
	name := fmt.Sprintf("qualification/%s/%d", t.Name(), time.Now().UnixNano())
	t.Cleanup(func() {
		for _, b := range []byte{2, 3, 4, 11} {
			wallet := [32]byte{b}
			request := js.Global().Get("indexedDB").Call("deleteDatabase", "arkade-poker/log/v1/"+name+"/"+hex.EncodeToString(wallet[:]))
			done := make(chan bool, 1)
			success := js.FuncOf(func(js.Value, []js.Value) any { done <- true; return nil })
			failure := js.FuncOf(func(js.Value, []js.Value) any { done <- false; return nil })
			request.Set("onsuccess", success)
			request.Set("onerror", failure)
			if !<-done {
				t.Error("qualification database cleanup")
			}
			request.Set("onsuccess", js.Null())
			request.Set("onerror", js.Null())
			success.Release()
			failure.Release()
		}
	})
	return name
}

func TestBrowserAbortAndCorruption(t *testing.T) {
	ctx := context.Background()
	place := testLocation(t)
	wallet := [32]byte{11}
	log, err := Open(ctx, place, wallet)
	if err != nil {
		t.Fatal(err)
	}
	l := log.(*browserLog)
	if err := l.Append(ctx, 0, []byte("intact")); err != nil {
		t.Fatal(err)
	}
	// Abort after both writes are enqueued: neither record nor next-index may
	// commit, even though individual request success callbacks may have run.
	injected := errors.New("injected transaction abort")
	err = l.transaction(ctx, "readwrite", func(tx *browserTransaction) {
		tx.tx.Call("objectStore", "meta").Call("put", "2", "next")
		tx.tx.Call("objectStore", "records").Call("add", js.Global().Get("Uint8Array").New(2), browserRecordName(1))
		tx.fail(injected)
	})
	if !errors.Is(err, injected) {
		t.Fatalf("abort: %v", err)
	}
	records, err := l.Load(ctx)
	if err != nil || len(records) != 1 || string(records[0]) != "intact" {
		t.Fatalf("partial commit: %v", err)
	}
	l.db.Call("close") // Force an actual platform error before transaction creation.
	if err := l.Append(ctx, 1, []byte("lost")); err == nil {
		t.Fatal("closed database acknowledged append")
	}
	if err := l.Append(ctx, 1, []byte("replacement")); !errors.Is(err, ErrUncertain) {
		t.Fatalf("failed writer continued: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	log, err = Open(ctx, place, wallet)
	if err != nil {
		t.Fatal(err)
	}
	l = log.(*browserLog)
	if err := l.Append(ctx, 1, []byte("resumed")); err != nil {
		t.Fatal(err)
	}
	err = l.transaction(ctx, "readwrite", func(tx *browserTransaction) {
		tx.tx.Call("objectStore", "records").Call("delete", browserRecordName(0))
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, place, wallet); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("damaged log: %v", err)
	}
	if _, err := Open(ctx, place, wallet); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("failed open kept lock: %v", err)
	}
	if err := Clear(ctx, place, wallet); err != nil {
		t.Fatal("clear damaged browser log", err)
	}
	log, err = Open(ctx, place, wallet)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	records, err = log.Load(ctx)
	if err != nil || len(records) != 0 {
		t.Fatal("damaged browser records survived clear", err)
	}
	if err := log.Append(ctx, 0, []byte("fresh")); err != nil {
		t.Fatal("damaged browser append index survived clear", err)
	}
}

// Opt-in real navigation test driven by make qualify-storage. A holding page
// is deliberately left open: another tab must fail to acquire its lock, and a
// reload must release ownership and recover exactly the acknowledged records.
func TestBrowserLifecycle(t *testing.T) {
	if js.Global().Get("indexedDB").Type() != js.TypeObject {
		t.Skip("requires browser")
	}
	query := js.Global().Get("URLSearchParams").New(js.Global().Get("location").Get("search"))
	mode := query.Call("get", "mode")
	if mode.IsNull() {
		t.Skip("opt-in browser navigation qualification")
	}
	id := query.Call("get", "store")
	if id.IsNull() || id.String() == "" {
		t.Fatal("missing qualification store")
	}
	place := "qualification/lifecycle/" + id.String()
	wallet := [32]byte{12}
	ctx := context.Background()
	log, err := Open(ctx, place, wallet)
	if mode.String() == "probe" {
		if !errors.Is(err, ErrLocked) {
			if log != nil {
				_ = log.Close()
			}
			t.Fatalf("other tab did not exclude writer: %v", err)
		}
		t.Log("other tab excluded")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	records, err := log.Load(ctx)
	if err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	for i, record := range records {
		if string(record) != fmt.Sprintf("acknowledged %d", i) {
			_ = log.Close()
			t.Fatal("navigation changed record")
		}
	}
	switch mode.String() {
	case "hold":
		if len(records) > 1 {
			_ = log.Close()
			t.Fatal("use a fresh qualification store")
		}
		if err := log.Append(ctx, uint64(len(records)), []byte(fmt.Sprintf("acknowledged %d", len(records)))); err != nil {
			_ = log.Close()
			t.Fatal(err)
		}
		t.Logf("holding writer; recovered %d, durably appended record %d", len(records), len(records))
		js.Global().Get("document").Call("getElementById", "status").Set("textContent", fmt.Sprintf("Holding writer; recovered %d record(s)", len(records)))
		select {} // Navigation destroys the page and must release the Web Lock.
	case "cleanup":
		if len(records) != 2 {
			_ = log.Close()
			t.Fatalf("reload lost committed records: %d", len(records))
		}
		if err := log.Close(); err != nil {
			t.Fatal(err)
		}
		request := js.Global().Get("indexedDB").Call("deleteDatabase", "arkade-poker/log/v1/"+place+"/"+hex.EncodeToString(wallet[:]))
		done := make(chan bool, 1)
		success := js.FuncOf(func(js.Value, []js.Value) any { done <- true; return nil })
		failure := js.FuncOf(func(js.Value, []js.Value) any { done <- false; return nil })
		request.Set("onsuccess", success)
		request.Set("onerror", failure)
		if !<-done {
			t.Error("qualification cleanup failed")
		}
		request.Set("onsuccess", js.Null())
		request.Set("onerror", js.Null())
		success.Release()
		failure.Release()
		t.Log("refresh retained both acknowledgements; navigation released writer; temporary store removed")
	default:
		_ = log.Close()
		t.Fatal("unknown qualification mode")
	}
}
