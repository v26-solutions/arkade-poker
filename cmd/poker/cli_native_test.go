//go:build !js

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkade-poker/go/internal/diagnostics"
	"arkade-poker/go/internal/storage"
)

func TestNativeLogFileAndLastLogsCommand(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "sessions")
	t.Setenv("POKER_DATA_DIR", directory)
	// Maintenance must not depend on service config or wallet import.
	t.Setenv("POKER_WALLET_KEY", "invalid-wallet-fixture")
	t.Setenv("POKER_ARKD_URL", ":invalid endpoint")
	output, err := logOutput()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.Close() })
	logs := diagnostics.New(output)
	secret := strings.Repeat("ab", 32)
	logs.Logger().Info("First run", "error", errors.New("diagnostic "+secret), "mnemonic", "private phrase")
	path := filepath.Join(directory, lastLogName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(before, []byte("First run")) || bytes.Contains(before, []byte(secret)) || bytes.Contains(before, []byte("private phrase")) || !bytes.Contains(before, []byte("[REDACTED]")) {
		t.Fatal("log file missing current redacted output")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("native log is not private", err)
	}
	var printed bytes.Buffer
	handled, err := runCLI([]string{"--show-last-logs"}, strings.NewReader(""), &printed)
	if err != nil || !handled || !bytes.Equal(printed.Bytes(), before) {
		t.Fatal("last logs command failed", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) || os.Getenv("POKER_WALLET_KEY") != "invalid-wallet-fixture" {
		t.Fatal("show changed logs or imported a wallet")
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	printed.Reset()
	if _, err := runCLI([]string{"--show-last-logs"}, strings.NewReader(""), &printed); err != nil || !bytes.Equal(printed.Bytes(), before) {
		t.Fatal("logs were not retained after close", err)
	}
	output, err = logOutput()
	if err != nil {
		t.Fatal(err)
	}
	logs = diagnostics.New(output)
	logs.Logger().Info("Second run")
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(path)
	if bytes.Contains(after, []byte("First run")) || !bytes.Contains(after, []byte("Second run")) {
		t.Fatal("new run did not replace last logs")
	}
}

func TestNativeConcurrentLogFilesDoNotInterleave(t *testing.T) {
	t.Setenv("POKER_DATA_DIR", t.TempDir())
	first, err := logOutput()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := logOutput()
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	diagnostics.New(first).Logger().Info("older process")
	diagnostics.New(second).Logger().Info("latest process")
	var printed bytes.Buffer
	if _, err := runCLI([]string{"--show-last-logs"}, nil, &printed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(printed.String(), "older process") || !strings.Contains(printed.String(), "latest process") {
		t.Fatal("process logs interleaved")
	}
}

func TestClearCommandRequiresConfirmationAndKeepsLogs(t *testing.T) {
	for _, answer := range []string{"", "\n", "n\n", "yes\n", "delete\n", "DELETE\n"} {
		t.Run(answer, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("POKER_DATA_DIR", directory)
			public := [32]byte{4}
			saved, err := storage.Open(t.Context(), directory, public)
			if err != nil {
				t.Fatal(err)
			}
			if err := saved.Append(t.Context(), 0, []byte("private session record")); err != nil {
				t.Fatal(err)
			}
			if err := saved.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, lastLogName)
			const last = "last diagnostic logs\n"
			if err := os.WriteFile(path, []byte(last), 0600); err != nil {
				t.Fatal(err)
			}
			var printed bytes.Buffer
			handled, err := runCLI([]string{"--clear-session-data"}, strings.NewReader(answer), &printed)
			if err != nil || !handled {
				t.Fatal("clear command failed", err)
			}
			if !strings.Contains(printed.String(), directory) || !strings.Contains(printed.String(), "Type DELETE") || !strings.Contains(printed.String(), "does not refund funds") {
				t.Fatal("missing confirmation scope")
			}
			saved, err = storage.Open(t.Context(), directory, public)
			if err != nil {
				t.Fatal(err)
			}
			defer saved.Close()
			records, err := saved.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if (len(records) == 0) != (answer == "DELETE\n") {
				t.Fatal("incorrect deletion/confirmation behavior")
			}
			logs, err := os.ReadFile(path)
			if err != nil || string(logs) != last {
				t.Fatal("clear changed last logs", err)
			}
		})
	}
}

func TestMaintenanceWithoutExistingDataAndInvalidArguments(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "does-not-exist")
	t.Setenv("POKER_DATA_DIR", directory)
	for _, args := range [][]string{{"--show-last-logs"}, {"--clear-session-data"}, {"--help"}} {
		var printed bytes.Buffer
		handled, err := runCLI(args, strings.NewReader("DELETE\n"), &printed)
		if err != nil || !handled || printed.Len() == 0 {
			t.Fatal("maintenance failed", args, err)
		}
		if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("maintenance created data directory")
		}
	}
	for _, args := range [][]string{{"--unknown"}, {"--show-last-logs", "--clear-session-data"}, {"--clear-session-data", "wallet"}, {"--show-last-logs=false"}} {
		handled, err := runCLI(args, strings.NewReader("DELETE\n"), &bytes.Buffer{})
		if !handled || err == nil {
			t.Fatal("invalid arguments would launch TUI", args)
		}
	}
	if handled, err := runCLI(nil, nil, nil); handled || err != nil {
		t.Fatal("normal startup was intercepted")
	}
}

func TestNativeLogFileCreationFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POKER_DATA_DIR", path)
	if out, err := logOutput(); err == nil || out != nil {
		t.Fatal("failed log creation was hidden")
	}
}
