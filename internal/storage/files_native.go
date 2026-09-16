//go:build !js

package storage

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

type fileLog struct {
	mu             sync.Mutex
	wallet         [32]byte
	directory      string
	lock, dir      *os.File
	count          uint64
	closed, failed bool
}

var _ Log = (*fileLog)(nil)

// PrepareDirectory durably creates the shared native data directory. Hosts
// writing diagnostics before Open must preserve the same directory-creation
// guarantees as the session store's first acknowledged append.
func PrepareDirectory(directory string) error {
	if directory == "" {
		return ErrUnavailable
	}
	return durableMkdir(directory)
}

// Open uses an OS-held lock, released even after a process crash. The lock file
// must never be unlinked: all contenders must lock the same inode. Each append
// fsyncs a staged file, atomically renames it, then fsyncs its directory. Staged
// files left by an interrupted append cannot become acknowledged records.
func Open(ctx context.Context, directory string, walletPublicKey [32]byte) (Log, error) {
	l, err := lockFiles(ctx, directory, walletPublicKey)
	if err != nil {
		return nil, err
	}
	records, err := l.load(ctx)
	for _, record := range records {
		clear(record)
	}
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// Clear removes this wallet's saved game history, including damaged records.
// It requires exclusive ownership and keeps the persistent writer lock inode.
func Clear(ctx context.Context, directory string, walletPublicKey [32]byte) error {
	l, err := lockFiles(ctx, directory, walletPublicKey)
	if err != nil {
		return err
	}
	defer l.Close()
	entries, err := l.clearEntries()
	if err != nil {
		return err
	}
	return l.clear(ctx, entries)
}

// ClearAll clears recognized wallet stores, preserving diagnostics and unrelated
// files. Acquire every existing wallet lock and validate all contents before any
// deletion, so an active or unrecognized store cannot cause a partial clear.
// Empty wallet directories and their lock inodes must remain for other writers.
func ClearAll(ctx context.Context, directory string) error {
	if ctx == nil || directory == "" {
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	type pendingClear struct {
		log     *fileLog
		entries []os.DirEntry
	}
	var stores []pendingClear
	defer func() {
		for _, store := range stores {
			_ = store.log.Close()
		}
	}()
	for _, entry := range entries {
		name := entry.Name()
		if len(name) != 64 || strings.ToLower(name) != name {
			continue
		}
		public, err := hex.DecodeString(name)
		if err != nil {
			continue
		}
		if !entry.IsDir() { // In particular, never follow wallet-directory symlinks.
			return ErrCorrupt
		}
		l, err := lockFiles(ctx, directory, [32]byte(public))
		if err != nil {
			return err
		}
		stores = append(stores, pendingClear{log: l})
		files, err := l.clearEntries()
		if err != nil {
			return err
		}
		stores[len(stores)-1].entries = files
	}
	for _, store := range stores {
		if err := store.log.clear(ctx, store.entries); err != nil {
			return err
		}
	}
	return nil
}

func (l *fileLog) clearEntries() ([]os.DirEntry, error) {
	entries, err := os.ReadDir(l.directory)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Name() == "writer.lock" {
			continue
		}
		if !entry.Type().IsRegular() || !(strings.HasSuffix(entry.Name(), ".record") || strings.HasPrefix(entry.Name(), ".append-")) {
			return nil, ErrCorrupt
		}
	}
	return entries, nil
}

func (l *fileLog) clear(ctx context.Context, entries []os.DirEntry) error {
	// Delete from the end so an interrupted clear does not introduce gaps into
	// an otherwise intact log. A failed clear can be explicitly retried.
	for i := len(entries) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entries[i].Name() != "writer.lock" {
			if err := os.Remove(filepath.Join(l.directory, entries[i].Name())); err != nil {
				return err
			}
		}
	}
	return l.dir.Sync()
}

func lockFiles(ctx context.Context, directory string, walletPublicKey [32]byte) (*fileLog, error) {
	if ctx == nil || directory == "" {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := durableMkdir(directory); err != nil {
		return nil, err
	}
	directory = filepath.Join(directory, hex.EncodeToString(walletPublicKey[:]))
	if err := durableMkdir(directory); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(directory, "writer.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	l := &fileLog{wallet: walletPublicKey, directory: directory, lock: lock}
	l.dir, err = os.Open(directory)
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// Persist newly created directories too, so an acknowledged first record cannot
// disappear solely because its parent directory entry was never synchronized.
func durableMkdir(path string) error {
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err == nil {
		if !info.IsDir() {
			return ErrUnavailable
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(path)
	if err := durableMkdir(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	dir, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (l *fileLog) ready(ctx context.Context) error {
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

func recordName(index uint64) string { return fmt.Sprintf("%020d.record", index) }

func (l *fileLog) load(ctx context.Context) (records [][]byte, err error) {
	defer func() {
		if err != nil {
			for _, record := range records {
				clear(record)
			}
			records = nil
		}
	}()
	entries, err := os.ReadDir(l.directory) // Sorted names enforce the complete prefix.
	if err != nil {
		return nil, err
	}
	var index uint64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return records, err
		}
		name := entry.Name()
		if name == "writer.lock" {
			continue
		}
		if strings.HasPrefix(name, ".append-") && entry.Type().IsRegular() {
			// Remove only our own unfinished staging files while holding ownership.
			if err := os.Remove(filepath.Join(l.directory, name)); err != nil {
				return records, err
			}
			continue
		}
		if name != recordName(index) || !entry.Type().IsRegular() {
			return records, ErrCorrupt
		}
		f, err := os.Open(filepath.Join(l.directory, name))
		if err != nil {
			return records, err
		}
		b, readErr := io.ReadAll(io.LimitReader(f, int64(MaxRecordBytes+frameOverhead+1)))
		closeErr := f.Close()
		if readErr != nil {
			clear(b)
			return records, readErr
		}
		if closeErr != nil {
			clear(b)
			return records, closeErr
		}
		record, err := unframe(l.wallet, index, b)
		clear(b)
		if err != nil {
			return records, err
		}
		records = append(records, record)
		index++
	}
	l.count = index
	return records, nil
}

func (l *fileLog) Load(ctx context.Context) ([][]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.ready(ctx); err != nil {
		return nil, err
	}
	return l.load(ctx)
}

func (l *fileLog) Append(ctx context.Context, expectedIndex uint64, record []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.ready(ctx); err != nil {
		return err
	}
	if expectedIndex != l.count || l.count == math.MaxUint64 {
		return ErrIndex
	}
	b, err := frame(l.wallet, expectedIndex, record)
	if err != nil {
		return err
	}
	defer clear(b)
	f, err := os.CreateTemp(l.directory, ".append-")
	if err != nil {
		l.failed = true
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(l.directory, recordName(expectedIndex)))
	}
	if err == nil {
		err = l.dir.Sync()
	}
	if err != nil {
		l.failed = true
		return err
	}
	l.count++
	return nil
}

func (l *fileLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	var errs []error
	if l.dir != nil {
		errs = append(errs, l.dir.Close())
	}
	if l.lock != nil {
		errs = append(errs, l.lock.Close())
	}
	return errors.Join(errs...)
}
