//go:build !js

package storage

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func testLocation(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sessions", "private")
}

func TestNativeClearDamagedRecordsKeepsLock(t *testing.T) {
	ctx := context.Background()
	place := testLocation(t)
	public := [32]byte{2}
	log, err := Open(ctx, place, public)
	if err != nil {
		t.Fatal(err)
	}
	directory := log.(*fileLog).directory
	lockPath := filepath.Join(directory, "writer.lock")
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{recordName(0), recordName(4), ".append-interrupted"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("damaged"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Open(ctx, place, public); !errors.Is(err, ErrCorrupt) {
		t.Fatal("damaged fixture loaded", err)
	}
	if err := Clear(ctx, place, public); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(lockPath)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("clear replaced writer lock inode", err)
	}
	log, err = Open(ctx, place, public)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	records, err := log.Load(ctx)
	if err != nil || len(records) != 0 {
		t.Fatal("damaged records survived clear", err)
	}
}

func TestNativeInterruptedAppend(t *testing.T) {
	ctx := context.Background()
	place := testLocation(t)
	wallet := [32]byte{7}
	log, err := Open(ctx, place, wallet)
	if err != nil {
		t.Fatal(err)
	}
	l := log.(*fileLog)
	if err := l.Append(ctx, 0, []byte("committed")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.directory, ".append-interrupted"), []byte("partial private bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	log, err = Open(ctx, place, wallet)
	if err != nil {
		t.Fatal(err)
	}
	l = log.(*fileLog)
	records, err := l.Load(ctx)
	if err != nil || len(records) != 1 || string(records[0]) != "committed" {
		t.Fatalf("partial staging recovery: %v", err)
	}
	// Simulate directory-sync failure after rename: no acknowledgement, and no
	// further use until re-open. Recovery can find the complete pending record.
	if err := l.dir.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(ctx, 1, []byte("complete but unacknowledged")); err == nil {
		t.Fatal("acknowledged without directory sync")
	}
	if err := l.Append(ctx, 1, []byte("replacement")); !errors.Is(err, ErrUncertain) {
		t.Fatalf("continued after failure: %v", err)
	}
	if _, err := l.Load(ctx); !errors.Is(err, ErrUncertain) {
		t.Fatalf("used failed writer: %v", err)
	}
	_ = l.Close()
	log, err = Open(ctx, place, wallet)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	records, err = log.Load(ctx)
	if err != nil || len(records) != 2 || string(records[1]) != "complete but unacknowledged" {
		t.Fatalf("uncertain recovery: %v", err)
	}
}

func TestNativeDamagedLog(t *testing.T) {
	ctx := context.Background()
	wallet := [32]byte{8}
	for _, mutation := range []string{"damage", "gap", "truncation", "unexpected", "wallet"} {
		t.Run(mutation, func(t *testing.T) {
			place := testLocation(t)
			log, err := Open(ctx, place, wallet)
			if err != nil {
				t.Fatal(err)
			}
			if err := log.Append(ctx, 0, []byte("first")); err != nil {
				t.Fatal(err)
			}
			if err := log.Append(ctx, 1, []byte("second")); err != nil {
				t.Fatal(err)
			}
			if err := log.Close(); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(place, hex.EncodeToString(wallet[:]))
			path := filepath.Join(dir, recordName(0))
			switch mutation {
			case "gap":
				err = os.Remove(path)
			case "truncation":
				err = os.Truncate(path, 10)
			case "unexpected":
				err = os.WriteFile(filepath.Join(dir, "unknown"), []byte{1}, 0600)
			case "wallet":
				var b []byte
				b, err = frame([32]byte{9}, 0, []byte("first"))
				if err == nil {
					err = os.WriteFile(path, b, 0600)
				}
			case "damage":
				var b []byte
				b, err = os.ReadFile(path)
				if err == nil {
					b[len(b)-1] ^= 1
					err = os.WriteFile(path, b, 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Open(ctx, place, wallet); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("accepted damaged log: %v", err)
			}
			if _, err := Open(ctx, place, wallet); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("failed open retained ownership: %v", err)
			}
		})
	}
}

func TestNativeProcessCrashReleasesWriter(t *testing.T) {
	const helperEnv = "POKER_STORAGE_TEST_PROCESS"
	ctx := context.Background()
	wallet := [32]byte{10}
	if place := os.Getenv(helperEnv); place != "" {
		log, err := Open(ctx, place, wallet)
		if err != nil {
			os.Exit(12)
		}
		if err := log.Append(ctx, 0, []byte("before crash")); err != nil {
			os.Exit(13)
		}
		os.Exit(0) // No Close/defer; the OS must release ownership.
	}
	place := testLocation(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeProcessCrashReleasesWriter$")
	cmd.Env = append(os.Environ(), helperEnv+"="+place)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v %s", err, out)
	}
	log, err := Open(ctx, place, wallet)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	records, err := log.Load(ctx)
	if err != nil || len(records) != 1 || string(records[0]) != "before crash" {
		t.Fatalf("crash recovery: %v", err)
	}
}
