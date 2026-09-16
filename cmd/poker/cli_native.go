//go:build !js

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"arkade-poker/go/internal/storage"
)

// Keep the TUI, diagnostic file and maintenance commands on the same location.
// An explicit override does not depend on an available user config directory.
func dataDirectory() (string, error) {
	if directory := os.Getenv("POKER_DATA_DIR"); directory != "" {
		return filepath.Abs(directory)
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "arkade-poker-go"), nil
}

func runCLI(args []string, in io.Reader, out io.Writer) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	flags := flag.NewFlagSet("poker", flag.ContinueOnError)
	flags.SetOutput(out)
	show := flags.Bool("show-last-logs", false, "print the last native run's logs without launching the TUI")
	clear := flags.Bool("clear-session-data", false, "delete all saved sessions after confirmation, keeping last logs")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, nil
		}
		return true, err
	}
	if flags.NArg() != 0 || *show == *clear {
		return true, errors.New("use exactly one of --show-last-logs or --clear-session-data, or no arguments to play")
	}
	directory, err := dataDirectory()
	if err != nil {
		return true, err
	}
	if *show {
		return true, showLastLogs(directory, out)
	}
	return true, clearSessionData(directory, in, out)
}

func showLastLogs(directory string, out io.Writer) error {
	path := filepath.Join(directory, lastLogName)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		_, err = fmt.Fprintf(out, "No saved native logs at %s.\n", path)
		return err
	}
	if err != nil {
		return fmt.Errorf("open last logs: %w", err)
	}
	defer f.Close()
	// The file only contains diagnostics' redacted output. Stream it without
	// truncation or loading an entire long-running session into memory.
	_, err = io.Copy(out, f)
	return err
}

func clearSessionData(directory string, in io.Reader, out io.Writer) error {
	if _, err := fmt.Fprintf(out, "Permanently delete all saved sessions in %s?\nRecovery data for unfinished hands will be lost; this does not refund funds.\nLast logs will be kept. Type DELETE to confirm: ", directory); err != nil {
		return err
	}
	confirmation, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read confirmation: %w", err)
	}
	if strings.TrimSpace(confirmation) != "DELETE" {
		_, err := fmt.Fprintln(out, "Cancelled. Session data and last logs were kept.")
		return err
	}
	if err := storage.ClearAll(context.Background(), directory); err != nil {
		if errors.Is(err, storage.ErrLocked) {
			return errors.New("session data is in use; close running poker instances and retry")
		}
		return fmt.Errorf("clear session data: %w", err)
	}
	_, err = fmt.Fprintln(out, "Session data cleared. Last logs kept.")
	return err
}
