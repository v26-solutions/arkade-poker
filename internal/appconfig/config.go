// Package appconfig supplies runtime defaults to both application hosts and
// the web builder. Startup settings remain authoritative when reopening a wallet.
package appconfig

import (
	"fmt"
	"strconv"

	"arkade-poker/go/internal/game"
)

// Config contains only public settings that may be embedded in a web build.
type Config struct {
	ArkdURL      string     `json:"arkd"`
	EmulatorURL  string     `json:"emulator"`
	EmulatorPCR0 string     `json:"emulatorPCR0"`
	DelegatorURL string     `json:"delegator"`
	IndexerURL   string     `json:"indexer"`
	RelayURL     string     `json:"relay"`
	Terms        game.Terms `json:"terms"`
}

func Defaults() Config {
	return Config{
		ArkdURL:      "https://mutinynet.arkade.sh",
		EmulatorURL:  "https://emulator.mutinynet.enclave-dev.arkade.sh",
		EmulatorPCR0: "eb1be1bb0da69abf0f53d207a4a7c66642b4aa7a5140676c22700cfb491450194eb105a8ebc9bd94bfc14ba45e5bc793",
		DelegatorURL: "https://delegator.mutinynet.arkade.sh",
		RelayURL:     "wss://nos.lol",
		Terms:        game.Terms{Stake: 5000, Bond: 5000, MinBet: 500, MaxWager: 100000},
	}
}

// Resolve the indexer after overrides so it follows the selected Arkd endpoint.
func (c Config) Resolve() (Config, error) {
	if c.IndexerURL == "" {
		c.IndexerURL = c.ArkdURL
	}
	if err := c.Terms.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid default game amounts: %w", err)
	}
	return c, nil
}

func ParseAmount(value string) (int64, error) {
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("amount must be positive whole satoshis")
		}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 || n > 21_000_000*100_000_000 {
		return 0, fmt.Errorf("amount must be positive whole satoshis within Bitcoin's supply")
	}
	return n, nil
}

// FromEnv treats empty environment variables as unset, matching native startup.
func FromEnv(getenv func(string) string) (Config, error) {
	c := Defaults()
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"POKER_ARKD_URL", &c.ArkdURL},
		{"POKER_EMULATOR_URL", &c.EmulatorURL},
		{"POKER_EMULATOR_PCR0", &c.EmulatorPCR0},
		{"POKER_DELEGATOR_URL", &c.DelegatorURL},
		{"POKER_INDEXER_URL", &c.IndexerURL},
		{"POKER_RELAY_URL", &c.RelayURL},
	} {
		if value := getenv(field.name); value != "" {
			*field.value = value
		}
	}
	for _, field := range []struct {
		name  string
		value *int64
	}{
		{"POKER_STAKE", &c.Terms.Stake}, {"POKER_BOND", &c.Terms.Bond},
		{"POKER_MIN_BET", &c.Terms.MinBet}, {"POKER_MAX_WAGER", &c.Terms.MaxWager},
	} {
		if value := getenv(field.name); value != "" {
			n, err := ParseAmount(value)
			if err != nil {
				return Config{}, fmt.Errorf("%s: %w", field.name, err)
			}
			*field.value = n
		}
	}
	return c.Resolve()
}
