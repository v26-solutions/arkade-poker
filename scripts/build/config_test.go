package main

import (
	"encoding/json"
	"os/exec"
	"testing"

	"arkade-poker/go/internal/appconfig"
	"arkade-poker/go/internal/game"
	"github.com/evanw/esbuild/pkg/api"
)

func TestWebConfig(t *testing.T) {
	// Native settings must not leak into the web artifact.
	t.Setenv("POKER_NETWORK", "bitcoin")
	t.Setenv("POKER_STAKE", "90000")
	want, _ := appconfig.Defaults().Resolve()
	c, err := webConfig(nil)
	if err != nil || c != want {
		t.Fatalf("web defaults: %+v, %v", c, err)
	}
	c, err = webConfig([]string{"--arkd-url", "http://localhost:7070",
		"--emulator-url", "http://localhost:7073", "--relay-url", "ws://localhost:7777",
		"--delegator-url", "http://localhost:7012",
		"--stake", "10000", "--bond", "20000", "--min-bet", "1000", "--max-wager", "50000"})
	want = appconfig.Config{ArkdURL: "http://localhost:7070", IndexerURL: "http://localhost:7070",
		EmulatorURL: "http://localhost:7073", RelayURL: "ws://localhost:7777",
		DelegatorURL: "http://localhost:7012",
		Terms:        game.Terms{Stake: 10000, Bond: 20000, MinBet: 1000, MaxWager: 50000}}
	if err != nil || c != want {
		t.Fatalf("web flags: %+v, %v", c, err)
	}
	c, err = webConfig([]string{"--indexer-url=https://index.example"})
	if err != nil || c.IndexerURL != "https://index.example" {
		t.Fatalf("indexer: %+v, %v", c, err)
	}
	for _, args := range [][]string{{"--stake=1.5"}, {"--bond=-1"}, {"--min-bet=100001"}, {"--max-wager=499"}, {"--stake=2100000000000000"}, {"--stake"}, {"--network=mutinynet"}, {"--unknown"}, {"extra"}} {
		if _, err := webConfig(args); err == nil {
			t.Fatal("accepted invalid flags", args)
		}
	}
}

func TestEmbeddedWebConfig(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is needed to execute the embedded configuration")
	}
	c, err := webConfig([]string{"--stake=7000", "--bond=8000", "--min-bet=600", "--max-wager=120000",
		"--relay-url=wss://relay.example/path?label=\"quoted\"&line=\\n"})
	if err != nil {
		t.Fatal(err)
	}
	defines, err := frontendDefines("poker.hash.wasm", c)
	if err != nil {
		t.Fatal(err)
	}
	result := api.Transform("process.stdout.write(JSON.stringify({config: POKER_BUILD_CONFIG, wasm: POKER_WASM_PATH}))", api.TransformOptions{Define: defines})
	if len(result.Errors) != 0 {
		t.Fatal(result.Errors)
	}
	data, err := exec.Command(node, "-e", string(result.Code)).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Config appconfig.Config
		WASM   string
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Config != c || got.WASM != "./poker.hash.wasm" {
		t.Fatalf("embedded configuration changed: %+v", got)
	}
}
