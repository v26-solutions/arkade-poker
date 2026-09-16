package diagnostics

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestRedactionBeforeRetentionAndOutput(t *testing.T) {
	const phrase = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	cases := map[string]string{
		"hex":                strings.Repeat("ab", 32),
		"uppercase hex":      strings.Repeat("CD", 32),
		"nsec":               "nsec1" + strings.Repeat("q", 58),
		"WIF":                "L" + strings.Repeat("a", 51),
		"extended key":       "xprv" + strings.Repeat("a", 107),
		"mnemonic":           phrase,
		"multiline mnemonic": strings.ReplaceAll(phrase, " ", "\n"),
		"escaped mnemonic":   strings.ReplaceAll(phrase, " ", `\n`),
		"mnemonic array":     `"` + strings.ReplaceAll(phrase, " ", `","`) + `"`,
		"labeled bytes":      "private_key: [1 2 3 4 5 6 7 8]",
		"labeled multiline":  "seed=\"unexpected\nprivate material\"",
	}
	for name, secret := range cases {
		t.Run(name, func(t *testing.T) {
			var console bytes.Buffer
			l := New(&console)
			_, _ = fmt.Fprintln(l, "connection failed:", secret)
			for _, text := range []string{l.Snapshot(), strings.Join(l.entries, ""), console.String()} {
				if strings.Contains(text, secret) || !strings.Contains(text, redacted) {
					t.Fatal("secret was not redacted")
				}
				if strings.Contains(text, "abandon") || strings.Contains(text, "private material") {
					t.Fatal("partial mnemonic or multiline secret exposed")
				}
			}
			if console.String() != strings.Join(l.entries, "") {
				t.Fatal("console and retained logs differ")
			}
		})
	}
}

func TestStructuredSecretsAndUsefulMetadata(t *testing.T) {
	var console bytes.Buffer
	l := New(&console)
	logger := l.Logger()
	logger.Info("Service failed", "stage", 23, "network", "mutinynet", "error", errors.New("connection refused"))
	logger.Info("Fields", "private_key", "private-value", "MNEMONIC", "phrase-value",
		"object", struct{ Hidden string }{"object-value"})
	logger.With("wallet-key", "bound-value").Info("Bound field")
	logger.WithGroup("secrets").With("data", "group-value").Info("Bound group", "other", "other-value")
	logger.Info("Nested", slog.Group("credentials", slog.Group("seed", slog.String("bytes", "nested-value"))))
	for _, secret := range []string{"private-value", "phrase-value", "object-value", "bound-value", "group-value", "other-value", "nested-value"} {
		if strings.Contains(l.Snapshot(), secret) || strings.Contains(console.String(), secret) {
			t.Fatalf("structured secret %q escaped redaction", secret)
		}
	}
	for _, want := range []string{"time=", "level=INFO", "Service failed", "stage=23", "network=mutinynet", "connection refused"} {
		if !strings.Contains(l.Snapshot(), want) {
			t.Fatalf("useful diagnostic missing: %s", want)
		}
	}
}

func TestInstallCapturesStandardAndDependencyLogs(t *testing.T) {
	l := New(nil)
	restore := l.Install()
	defer restore()
	slog.Info("slog marker")
	log.Printf("standard marker %s", strings.Repeat("ef", 32))
	logrus.WithField("value", strings.Repeat("ab", 32)).Warn("dependency marker")
	for _, marker := range []string{"slog marker", "standard marker", "dependency marker"} {
		if !strings.Contains(l.Snapshot(), marker) {
			t.Fatalf("logger not captured: %s", marker)
		}
	}
	if strings.Contains(l.Snapshot(), strings.Repeat("ef", 32)) || strings.Contains(l.Snapshot(), strings.Repeat("ab", 32)) {
		t.Fatal("dependency/standard logger bypassed redaction")
	}
}

func TestBoundedConcurrentSnapshots(t *testing.T) {
	l := New(nil)
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			for i := range 400 {
				l.Logger().Info("progress", "worker", worker, "sequence", i)
				_ = l.Snapshot()
			}
		})
	}
	wg.Wait()
	if len(l.entries) > maxEntries || l.bytes > maxBytes || l.dropped == 0 {
		t.Fatal("log did not remain bounded")
	}
	_, _ = fmt.Fprintln(l, "last marker")
	if !strings.HasSuffix(l.Snapshot(), "last marker\n") || !strings.Contains(l.Snapshot(), "older entries omitted") {
		t.Fatal("snapshot did not preserve latest records/omission notice")
	}
	// The raw value crosses the truncation boundary; redact before slicing.
	_, _ = fmt.Fprintln(l, strings.Repeat(".", maxEntry-8)+strings.Repeat("ab", 32))
	last := l.entries[len(l.entries)-1]
	if len(last) > maxEntry+32 || strings.Contains(last, "abab") {
		t.Fatal("truncation exposed part of a secret")
	}
	for range 100 {
		_, _ = fmt.Fprintln(l, strings.Repeat(".", maxEntry))
	}
	if l.bytes > maxBytes {
		t.Fatal("byte limit exceeded")
	}
}
