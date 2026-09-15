//go:build js && wasm

package storage

import (
	"context"
	"encoding/hex"
	"math"
	"strconv"
	"sync"
	"syscall/js"
)

type browserLog struct {
	mu             sync.Mutex
	wallet         [32]byte
	db             js.Value
	lock           *browserLock
	versionChange  js.Func
	closed, failed bool
}

var _ Log = (*browserLog)(nil)

// Open uses Web Locks for crash/refresh-safe exclusion across tabs, and an
// IndexedDB database scoped to the public wallet identity. There is deliberately
// no in-memory fallback when either facility is unavailable.
func Open(ctx context.Context, database string, walletPublicKey [32]byte) (Log, error) {
	l, err := lockDatabase(ctx, database, walletPublicKey)
	if err != nil {
		return nil, err
	}
	records, err := l.Load(ctx)
	for _, record := range records {
		clear(record)
	}
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// Clear resets this wallet's records and append index in one durable transaction,
// without requiring the old history to decode successfully.
func Clear(ctx context.Context, database string, walletPublicKey [32]byte) error {
	l, err := lockDatabase(ctx, database, walletPublicKey)
	if err != nil {
		return err
	}
	defer l.Close()
	return l.transaction(ctx, "readwrite", func(t *browserTransaction) {
		t.tx.Call("objectStore", "records").Call("clear")
		t.tx.Call("objectStore", "meta").Call("put", "0", "next")
	})
}

func lockDatabase(ctx context.Context, database string, walletPublicKey [32]byte) (*browserLog, error) {
	if ctx == nil || database == "" {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := "arkade-poker/log/v1/" + database + "/" + hex.EncodeToString(walletPublicKey[:])
	lock, err := acquireBrowserLock(ctx, name)
	if err != nil {
		return nil, err
	}
	db, err := openDatabase(ctx, name)
	if err != nil {
		_ = lock.close()
		return nil, err
	}
	l := &browserLog{wallet: walletPublicKey, db: db, lock: lock}
	l.versionChange = js.FuncOf(func(js.Value, []js.Value) any {
		_ = jsCall(func() { db.Call("close") })
		return nil
	})
	db.Set("onversionchange", l.versionChange)
	return l, nil
}

// Never include a JS exception's text: a platform exception may contain a key
// or private record argument. Preserve only our bounded local storage errors.
func jsCall(fn func()) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
	}()
	fn()
	return nil
}

type browserLock struct {
	release   js.Value
	done      chan error
	callbacks []js.Func
}

func acquireBrowserLock(ctx context.Context, name string) (*browserLock, error) {
	l := &browserLock{done: make(chan error, 1)}
	acquired := make(chan error, 1)
	signal := func(err error) {
		select {
		case acquired <- err:
		default:
		}
	}
	callback := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if err := ctx.Err(); err != nil {
			signal(err)
			return nil
		}
		if len(args) != 1 || args[0].IsNull() {
			signal(ErrLocked)
			return nil
		}
		var held js.Value
		err := jsCall(func() {
			executor := js.FuncOf(func(_ js.Value, args []js.Value) any { l.release = args[0]; return nil })
			defer executor.Release()
			held = js.Global().Get("Promise").New(executor)
		})
		signal(err)
		if err != nil {
			return nil
		}
		return held
	})
	success := js.FuncOf(func(js.Value, []js.Value) any { l.done <- nil; return nil })
	failure := js.FuncOf(func(js.Value, []js.Value) any { signal(ErrUnavailable); l.done <- ErrUnavailable; return nil })
	l.callbacks = []js.Func{callback, success, failure}
	err := jsCall(func() {
		locks := js.Global().Get("navigator").Get("locks")
		locks.Call("request", name, map[string]any{"mode": "exclusive", "ifAvailable": true}, callback).Call("then", success, failure)
	})
	if err != nil {
		for _, f := range l.callbacks {
			f.Release()
		}
		return nil, err
	}
	select {
	case err := <-acquired:
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			_ = l.close()
			return nil, err
		}
		return l, nil
	case <-ctx.Done():
		// The queued callback sees cancellation; retain it until the browser has
		// finished the request, releasing ownership if cancellation raced a grant.
		go func() { <-acquired; _ = l.close() }()
		return nil, ctx.Err()
	}
}
func (l *browserLock) close() error {
	if l.release.Type() == js.TypeFunction {
		_ = jsCall(func() { l.release.Invoke() })
	}
	err := <-l.done
	for _, f := range l.callbacks {
		f.Release()
	}
	return err
}

func openDatabase(ctx context.Context, name string) (db js.Value, err error) {
	var request js.Value
	if err := jsCall(func() { request = js.Global().Get("indexedDB").Call("open", name, 1) }); err != nil {
		return db, err
	}
	done := make(chan error, 1)
	var upgradeErr error
	upgrade := js.FuncOf(func(js.Value, []js.Value) any {
		upgradeErr = jsCall(func() {
			d := request.Get("result")
			d.Call("createObjectStore", "records")
			d.Call("createObjectStore", "meta").Call("put", "0", "next")
		})
		if upgradeErr != nil {
			_ = jsCall(func() { request.Get("transaction").Call("abort") })
		}
		return nil
	})
	success := js.FuncOf(func(js.Value, []js.Value) any { db = request.Get("result"); done <- nil; return nil })
	failure := js.FuncOf(func(js.Value, []js.Value) any { done <- ErrUnavailable; return nil })
	request.Set("onupgradeneeded", upgrade)
	request.Set("onsuccess", success)
	request.Set("onerror", failure)
	cleanup := func() {
		request.Set("onupgradeneeded", js.Null())
		request.Set("onsuccess", js.Null())
		request.Set("onerror", js.Null())
		upgrade.Release()
		success.Release()
		failure.Release()
	}
	select {
	case err = <-done:
		cleanup()
		if err == nil {
			err = upgradeErr
		}
		if err == nil {
			err = ctx.Err()
		}
		if err != nil && db.Type() == js.TypeObject {
			_ = jsCall(func() { db.Call("close") })
		}
		return db, err
	case <-ctx.Done():
		// IDB open requests cannot be cancelled. Close a late success rather
		// than leaking a connection or invoking callbacks after Release.
		go func() {
			if <-done == nil {
				_ = jsCall(func() { db.Call("close") })
			}
			cleanup()
		}()
		return js.Undefined(), ctx.Err()
	}
}

type browserTransaction struct {
	tx        js.Value
	callbacks []js.Func
	err       error
}

func (t *browserTransaction) fail(err error) {
	if t.err == nil {
		t.err = err
	}
	_ = jsCall(func() { t.tx.Call("abort") })
}
func (t *browserTransaction) callback(fn func()) js.Func {
	f := js.FuncOf(func(js.Value, []js.Value) any {
		if err := jsCall(fn); err != nil {
			t.fail(err)
		}
		return nil
	})
	t.callbacks = append(t.callbacks, f)
	return f
}
func (l *browserLog) transaction(ctx context.Context, mode string, start func(*browserTransaction)) error {
	t := &browserTransaction{}
	done := make(chan error, 1)
	err := jsCall(func() {
		t.tx = l.db.Call("transaction", []any{"records", "meta"}, mode, map[string]any{"durability": "strict"})
	})
	if err != nil {
		return err
	}
	complete := js.FuncOf(func(js.Value, []js.Value) any { done <- t.err; return nil })
	abort := js.FuncOf(func(js.Value, []js.Value) any {
		if t.err == nil {
			t.err = ctx.Err()
		}
		if t.err == nil {
			t.err = ErrUnavailable
		}
		done <- t.err
		return nil
	})
	t.tx.Set("oncomplete", complete)
	t.tx.Set("onabort", abort)
	if err := jsCall(func() {
		if mode == "readwrite" && t.tx.Get("durability").String() != "strict" {
			t.fail(ErrUnavailable)
			return
		}
		start(t)
	}); err != nil {
		t.fail(err)
	}
	select {
	case err = <-done:
	case <-ctx.Done():
		t.fail(ctx.Err())
		err = <-done // Await commit/abort before freeing callbacks or returning.
	}
	t.tx.Set("oncomplete", js.Null())
	t.tx.Set("onabort", js.Null())
	complete.Release()
	abort.Release()
	for _, callback := range t.callbacks {
		callback.Release()
	}
	return err
}
func (l *browserLog) ready(ctx context.Context) error {
	if l.closed {
		return ErrClosed
	}
	if l.failed {
		return ErrUncertain
	}
	if ctx == nil {
		return ErrUnavailable
	}
	return ctx.Err()
}
func storedIndex(v js.Value) (uint64, error) {
	if v.Type() != js.TypeString {
		return 0, ErrCorrupt
	}
	s := v.String()
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != s {
		return 0, ErrCorrupt
	}
	return n, nil
}
func browserRecordName(index uint64) string {
	s := strconv.FormatUint(index, 10)
	return "00000000000000000000"[:20-len(s)] + s
}
func (l *browserLog) Load(ctx context.Context) ([][]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.ready(ctx); err != nil {
		return nil, err
	}
	var records [][]byte
	var count uint64
	err := l.transaction(ctx, "readonly", func(t *browserTransaction) {
		meta := t.tx.Call("objectStore", "meta").Call("get", "next")
		meta.Set("onsuccess", t.callback(func() {
			var err error
			count, err = storedIndex(meta.Get("result"))
			if err != nil {
				t.fail(err)
			}
		}))
		request := t.tx.Call("objectStore", "records").Call("openCursor")
		request.Set("onsuccess", t.callback(func() {
			cursor := request.Get("result")
			if cursor.IsNull() {
				return
			}
			index := uint64(len(records))
			if cursor.Get("key").Type() != js.TypeString || cursor.Get("key").String() != browserRecordName(index) {
				t.fail(ErrCorrupt)
				return
			}
			v := cursor.Get("value")
			if !v.InstanceOf(js.Global().Get("Uint8Array")) || v.Get("byteLength").Int() > MaxRecordBytes+frameOverhead {
				t.fail(ErrCorrupt)
				return
			}
			b := make([]byte, v.Get("byteLength").Int())
			js.CopyBytesToGo(b, v)
			record, err := unframe(l.wallet, index, b)
			clear(b)
			if err != nil {
				t.fail(err)
				return
			}
			records = append(records, record)
			cursor.Call("continue")
		}))
	})
	if err == nil && count != uint64(len(records)) {
		err = ErrCorrupt
	}
	if err != nil {
		for _, record := range records {
			clear(record)
		}
		return nil, err
	}
	return records, nil
}
func (l *browserLog) Append(ctx context.Context, expectedIndex uint64, record []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.ready(ctx); err != nil {
		return err
	}
	if expectedIndex == math.MaxUint64 {
		return ErrIndex
	}
	b, err := frame(l.wallet, expectedIndex, record)
	if err != nil {
		return err
	}
	defer clear(b)
	err = l.transaction(ctx, "readwrite", func(t *browserTransaction) {
		meta := t.tx.Call("objectStore", "meta")
		request := meta.Call("get", "next")
		request.Set("onsuccess", t.callback(func() {
			index, err := storedIndex(request.Get("result"))
			if err != nil {
				t.fail(err)
				return
			}
			if index != expectedIndex {
				t.fail(ErrIndex)
				return
			}
			value := js.Global().Get("Uint8Array").New(len(b))
			js.CopyBytesToJS(value, b)
			// add, never put: a duplicate index cannot overwrite prior evidence.
			t.tx.Call("objectStore", "records").Call("add", value, browserRecordName(index))
			meta.Call("put", strconv.FormatUint(index+1, 10), "next")
		}))
	})
	if err != nil && err != ErrIndex {
		l.failed = true
	}
	return err
}
func (l *browserLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	err := jsCall(func() { l.db.Set("onversionchange", js.Null()); l.db.Call("close") })
	l.versionChange.Release()
	lockErr := l.lock.close()
	if err != nil {
		return err
	}
	return lockErr
}
