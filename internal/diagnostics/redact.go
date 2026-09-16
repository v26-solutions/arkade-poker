package diagnostics

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/tyler-smith/go-bip39/wordlists"
)

const redacted = "[REDACTED]"

var (
	// A raw private scalar is indistinguishable from a hash. Conservatively
	// redact long hex values, including inside larger serialized payloads.
	hexSecret     = regexp.MustCompile(`(?i)[0-9a-f]{64,}`)
	encodedSecret = regexp.MustCompile(`(?i)nsec1[0-9a-z]+|[xtyz]prv[1-9A-HJ-NP-Za-km-z]+|\b[5KL9c][1-9A-HJ-NP-Za-km-z]{50,51}\b`)
	// For unstructured dependency/error text, discard everything after a
	// sensitive label. This also covers quoted, multiline and array values.
	labeledSecret = regexp.MustCompile(`(?is)\b(?:private[ _-]?key|privkey|secret(?:[ _-]?key)?|transport[ _-]?secret|shuffle[ _-]?secret|mnemonic|seed(?:[ _-]?phrase)?|nsec|xprv|wallet[ _-]?key)\b["']?\s*[:=]\s*.*`)
	words         = regexp.MustCompile(`\\[nrt]|[A-Za-z]+`)
	mnemonicWords = func() map[string]bool {
		out := make(map[string]bool, len(wordlists.English))
		for _, word := range wordlists.English {
			out[word] = true
		}
		return out
	}()
)

func sensitiveName(name string) bool {
	name = strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, name)
	for _, part := range []string{"privatekey", "privkey", "secret", "mnemonic", "seed", "nsec", "xprv", "walletkey"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}

// Redact is defense in depth for error/dependency text, not permission to log
// secret objects. No imported secret is registered or retained by this package.
func Redact(text string) string {
	text = redactMnemonic(text)
	text = labeledSecret.ReplaceAllString(text, redacted)
	text = encodedSecret.ReplaceAllString(text, redacted)
	return hexSecret.ReplaceAllString(text, redacted)
}

func redactMnemonic(text string) string {
	indices := words.FindAllStringIndex(text, -1)
	start, end, count := 0, 0, 0
	var spans [][2]int
	flush := func() {
		// Redact invalid/checksum-failing phrases too, without deriving a key.
		if count >= 12 {
			spans = append(spans, [2]int{start, end})
		}
		count = 0
	}
	for _, index := range indices {
		// Dependency loggers may already have escaped a multiline phrase.
		if text[index[0]] == '\\' {
			continue
		}
		if !mnemonicWords[strings.ToLower(text[index[0]:index[1]])] {
			flush()
			continue
		}
		if count == 0 {
			start = index[0]
		}
		end = index[1]
		count++
	}
	flush()
	for i := len(spans) - 1; i >= 0; i-- {
		span := spans[i]
		text = text[:span[0]] + redacted + text[span[1]:]
	}
	return text
}
