// Package diagnostics keeps a bounded, redacted, process-local support log.
// Callers log operation names and public scalar metadata, never input text,
// wallet objects, game snapshots, wire payloads or private recovery records.
package diagnostics

import (
	"fmt"
	"io"
	"log"
	"log/slog"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
)

const (
	maxBytes   = 1 << 20
	maxEntries = 2048
	maxEntry   = 16 << 10
)

type Log struct {
	mu      sync.Mutex
	entries []string
	bytes   int
	dropped uint64
	output  io.Writer
}

// New optionally mirrors sanitized records to output. Pass nil for memory only.
func New(output io.Writer) *Log { return &Log{output: output} }

func (l *Log) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{ReplaceAttr: safeAttr}))
}

// Install captures slog, the standard logger and dependency logrus output.
// Native logs must not write over the terminal UI. Call before starting workers,
// and restore only after they have stopped.
func (l *Log) Install() func() {
	previous := slog.Default()
	writer, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	dependency := logrus.StandardLogger().Out
	slog.SetDefault(l.Logger())
	logrus.SetOutput(l)
	return func() {
		slog.SetDefault(previous)
		log.SetOutput(writer)
		log.SetFlags(flags)
		log.SetPrefix(prefix)
		logrus.SetOutput(dependency)
	}
}

// Write accepts complete log records, as emitted by slog, log and logrus.
// Redaction happens before retention and truncation, so neither the buffer nor
// a truncated record can expose a fragment of a recognized secret.
func (l *Log) Write(p []byte) (int, error) {
	entry := Redact(string(p))
	if len(entry) > maxEntry {
		entry = entry[:maxEntry] + " [entry truncated]\n"
	}
	if !strings.HasSuffix(entry, "\n") {
		entry += "\n"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for len(l.entries) > 0 && (len(l.entries) >= maxEntries || l.bytes+len(entry) > maxBytes) {
		l.bytes -= len(l.entries[0])
		l.entries[0] = ""
		l.entries = l.entries[1:]
		l.dropped++
	}
	l.entries = append(l.entries, entry)
	l.bytes += len(entry)
	if l.output != nil {
		_, _ = io.WriteString(l.output, entry)
	}
	return len(p), nil
}

func (l *Log) Snapshot() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	header := "Arkade Poker diagnostic logs (current run; secrets redacted)\n"
	if l.dropped > 0 {
		header += fmt.Sprintf("[%d older entries omitted]\n", l.dropped)
	}
	return header + strings.Join(l.entries, "")
}

func safeAttr(groups []string, a slog.Attr) slog.Attr {
	secret := sensitiveName(a.Key)
	for _, group := range groups {
		secret = secret || sensitiveName(group)
	}
	if secret {
		a.Value = slog.StringValue(redacted)
		return a
	}
	switch a.Value.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(Redact(a.Value.String()))
	case slog.KindAny:
		// Errors carry useful diagnostics. Arbitrary object formatting may
		// expose byte arrays, keys or snapshots; omit it instead.
		switch value := a.Value.Any().(type) {
		case slog.Level:
			a.Value = slog.StringValue(value.String())
		case error:
			a.Value = slog.StringValue(Redact(value.Error()))
		default:
			a.Value = slog.StringValue(redacted)
		}
	}
	return a
}
