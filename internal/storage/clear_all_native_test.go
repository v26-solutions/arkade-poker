//go:build !js

package storage

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func savedWallet(t *testing.T, directory string, public [32]byte) string {
	t.Helper()
	l, err := Open(t.Context(), directory, public)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(t.Context(), 0, []byte("saved session")); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(directory, hex.EncodeToString(public[:]))
}

func TestClearAllPreservesLogsAndLockInodes(t *testing.T) {
	directory := t.TempDir()
	for _, public := range [][32]byte{{1}, {2}} {
		savedWallet(t, directory, public)
	}
	wallet := filepath.Join(directory, hex.EncodeToString(append([]byte{1}, make([]byte, 31)...)))
	before, err := os.Stat(filepath.Join(wallet, "writer.lock"))
	if err != nil {
		t.Fatal(err)
	}
	// Corrupted and interrupted records are still session data to be cleared.
	for _, name := range []string{recordName(4), ".append-interrupted"} {
		if err := os.WriteFile(filepath.Join(wallet, name), []byte("damaged"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"last.log", "unrelated.txt"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(directory, "unrelated-directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := ClearAll(t.Context(), directory); err != nil {
		t.Fatal(err)
	}
	for _, public := range [][32]byte{{1}, {2}} {
		path := filepath.Join(directory, hex.EncodeToString(public[:]))
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 1 || entries[0].Name() != "writer.lock" {
			t.Fatal("session data survived clear", err)
		}
	}
	after, err := os.Stat(filepath.Join(wallet, "writer.lock"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("clear replaced writer lock", err)
	}
	for _, name := range []string{"last.log", "unrelated.txt"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || string(data) != name {
			t.Fatal("clear changed non-session data", err)
		}
	}
	if err := ClearAll(t.Context(), directory); err != nil {
		t.Fatal("repeated clear failed", err)
	}
}

func TestClearAllPreflightsEveryStore(t *testing.T) {
	for _, problem := range []string{"locked", "unexpected file", "symlink"} {
		t.Run(problem, func(t *testing.T) {
			directory := t.TempDir()
			first := savedWallet(t, directory, [32]byte{1})
			second := savedWallet(t, directory, [32]byte{2})
			want := ErrCorrupt
			switch problem {
			case "locked":
				active, err := Open(t.Context(), directory, [32]byte{2})
				if err != nil {
					t.Fatal(err)
				}
				defer active.Close()
				want = ErrLocked
			case "unexpected file":
				if err := os.WriteFile(filepath.Join(second, "unknown"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				outside := t.TempDir()
				public := [32]byte{3}
				if err := os.Symlink(outside, filepath.Join(directory, hex.EncodeToString(public[:]))); err != nil {
					t.Fatal(err)
				}
			}
			if err := ClearAll(t.Context(), directory); !errors.Is(err, want) {
				t.Fatal("unsafe clear accepted", err)
			}
			for _, path := range []string{first, second} {
				if _, err := os.Stat(filepath.Join(path, recordName(0))); err != nil {
					t.Fatal("preflight failure partially deleted sessions", err)
				}
			}
			l, err := Open(t.Context(), directory, [32]byte{1})
			if err != nil {
				t.Fatal("preflight leaked wallet lock", err)
			}
			_ = l.Close()
		})
	}
}

func TestClearAllCancelledOrMissing(t *testing.T) {
	directory := t.TempDir()
	wallet := savedWallet(t, directory, [32]byte{1})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := ClearAll(ctx, directory); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wallet, recordName(0))); err != nil {
		t.Fatal("cancelled clear deleted data", err)
	}
	if err := ClearAll(t.Context(), filepath.Join(directory, "missing")); err != nil {
		t.Fatal(err)
	}
	if err := ClearAll(nil, directory); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
