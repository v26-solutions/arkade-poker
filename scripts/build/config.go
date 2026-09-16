package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"arkade-poker/go/internal/appconfig"
	"arkade-poker/go/internal/palette"
)

func webConfig(args []string) (appconfig.Config, error) {
	c := appconfig.Defaults()
	flags := flag.NewFlagSet("web", flag.ContinueOnError)
	flags.StringVar(&c.ArkdURL, "arkd-url", c.ArkdURL, "Arkd endpoint")
	flags.StringVar(&c.EmulatorURL, "emulator-url", c.EmulatorURL, "Emulator endpoint")
	flags.StringVar(&c.DelegatorURL, "delegator-url", c.DelegatorURL, "Delegator endpoint")
	flags.StringVar(&c.IndexerURL, "indexer-url", c.IndexerURL, "Indexer endpoint (defaults to Arkd)")
	flags.StringVar(&c.RelayURL, "relay-url", c.RelayURL, "Nostr relay")
	for _, field := range []struct {
		name  string
		value *int64
	}{
		{"stake", &c.Terms.Stake}, {"bond", &c.Terms.Bond},
		{"min-bet", &c.Terms.MinBet}, {"max-wager", &c.Terms.MaxWager},
	} {
		flags.Func(field.name, fmt.Sprintf("Default %s in satoshis (default %d)", field.name, *field.value), func(value string) error {
			n, err := appconfig.ParseAmount(value)
			if err == nil {
				*field.value = n
			}
			return err
		})
	}
	if err := flags.Parse(args); err != nil {
		return appconfig.Config{}, err
	}
	if flags.NArg() != 0 {
		return appconfig.Config{}, fmt.Errorf("unexpected web build arguments: %v", flags.Args())
	}
	return c.Resolve()
}

func frontendDefines(wasm string, settings appconfig.Config) (map[string]string, error) {
	data, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	colors, err := json.Marshal(palette.Colors())
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"POKER_WASM_PATH":    strconv.Quote("./" + wasm),
		"POKER_BUILD_CONFIG": string(data),
		"POKER_PALETTE":      string(colors),
	}, nil
}

func paletteCSS() string {
	var properties []string
	for name, color := range palette.Colors() {
		properties = append(properties, "--"+name+": "+color+";")
	}
	// Keep generated assets deterministic despite map iteration order.
	slices.Sort(properties)
	return ":root { " + strings.Join(properties, " ") + " }"
}
