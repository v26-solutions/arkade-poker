package appconfig

import (
	"strings"
	"testing"

	"arkade-poker/go/internal/game"
)

func TestDefaultsAndEnvironmentOverrides(t *testing.T) {
	values := map[string]string{"POKER_NETWORK": "bitcoin"} // Network is discovered, never read from the environment.
	load := func() (Config, error) { return FromEnv(func(name string) string { return values[name] }) }
	c, err := load()
	want := Config{ArkdURL: "https://mutinynet.arkade.sh",
		EmulatorURL: "https://emulator.mutinynet.enclave-dev.arkade.sh", IndexerURL: "https://mutinynet.arkade.sh",
		EmulatorPCR0: "eb1be1bb0da69abf0f53d207a4a7c66642b4aa7a5140676c22700cfb491450194eb105a8ebc9bd94bfc14ba45e5bc793",
		DelegatorURL: "https://delegator.mutinynet.arkade.sh",
		RelayURL:     "wss://nos.lol", Terms: game.Terms{Stake: 5000, Bond: 5000, MinBet: 500, MaxWager: 100000}}
	if err != nil || c != want {
		t.Fatalf("defaults: %+v, %v", c, err)
	}
	values = map[string]string{
		"POKER_ARKD_URL":     "http://localhost:7070",
		"POKER_EMULATOR_URL": "http://localhost:7073", "POKER_RELAY_URL": "ws://localhost:7777",
		"POKER_DELEGATOR_URL": "http://localhost:7012",
		"POKER_EMULATOR_PCR0": strings.Repeat("ab", 48),
		"POKER_STAKE":         "10000", "POKER_BOND": "20000", "POKER_MIN_BET": "1000", "POKER_MAX_WAGER": "50000",
	}
	c, err = load()
	want = Config{ArkdURL: values["POKER_ARKD_URL"], EmulatorURL: values["POKER_EMULATOR_URL"],
		EmulatorPCR0: values["POKER_EMULATOR_PCR0"],
		DelegatorURL: values["POKER_DELEGATOR_URL"],
		IndexerURL:   values["POKER_ARKD_URL"], RelayURL: values["POKER_RELAY_URL"],
		Terms: game.Terms{Stake: 10000, Bond: 20000, MinBet: 1000, MaxWager: 50000}}
	if err != nil || c != want {
		t.Fatalf("overrides: %+v, %v", c, err)
	}
	values["POKER_INDEXER_URL"] = "http://localhost:7072"
	want.IndexerURL = "http://localhost:7072"
	c, err = load()
	if err != nil || c != want {
		t.Fatalf("explicit indexer: %+v, %v", c, err)
	}
	values = map[string]string{"POKER_STAKE": "", "POKER_ARKD_URL": "", "POKER_EMULATOR_PCR0": ""}
	c, err = load()
	if err != nil || c.ArkdURL != "https://mutinynet.arkade.sh" || c.Terms.Stake != 5000 || c.EmulatorPCR0 != Defaults().EmulatorPCR0 {
		t.Fatalf("empty variables: %+v, %v", c, err)
	}
}

func TestInvalidAmountOverrides(t *testing.T) {
	for _, name := range []string{"POKER_STAKE", "POKER_BOND", "POKER_MIN_BET", "POKER_MAX_WAGER"} {
		for _, value := range []string{"0", "-1", "1.5", "5,000", " 5000", "+5000", "0x500", "2100000000000001", "99999999999999999999"} {
			_, err := FromEnv(func(key string) string {
				if key == name {
					return value
				}
				return ""
			})
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("%s=%s: %v", name, value, err)
			}
		}
	}
	for _, values := range []map[string]string{
		{"POKER_MIN_BET": "100001"},
		{"POKER_STAKE": "2100000000000000"},
	} {
		if _, err := FromEnv(func(name string) string { return values[name] }); err == nil {
			t.Fatal("accepted invalid combined terms", values)
		}
	}
}
