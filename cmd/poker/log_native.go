//go:build !js

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"arkade-poker/go/internal/storage"
)

const lastLogName = "last.log"

func logOutput() (io.WriteCloser, error) {
	directory, err := dataDirectory()
	if err != nil {
		return nil, err
	}
	if err := storage.PrepareDirectory(directory); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	// Replace the path, not the inode: another running process keeps its own
	// output file and cannot mix old entries into the most recently started run.
	// CreateTemp also guarantees 0600 and does not follow an existing symlink.
	f, err := os.CreateTemp(directory, ".last-log-*")
	if err != nil {
		return nil, fmt.Errorf("create native log: %w", err)
	}
	if err := os.Rename(f.Name(), filepath.Join(directory, lastLogName)); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("replace native log: %w", err)
	}
	return &nativeLog{file: f}, nil
}

type nativeLog struct {
	file *os.File
	err  error
}

// diagnostics serializes writes and has already redacted every entry. Write
// directly to the file so logs survive process crashes without a buffered flush.
func (l *nativeLog) Write(p []byte) (int, error) {
	n, err := l.file.Write(p)
	if err != nil && l.err == nil {
		l.err = err
	}
	return n, err
}

func (l *nativeLog) Close() error {
	if err := errors.Join(l.err, l.file.Sync(), l.file.Close()); err != nil {
		return fmt.Errorf("save native logs: %w", err)
	}
	return nil
}
