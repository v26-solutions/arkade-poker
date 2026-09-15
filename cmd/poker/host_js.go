//go:build js && wasm

package main

import (
	"context"
	"encoding/json"
	"syscall/js"

	"arkade-poker/go/internal/appconfig"
	"arkade-poker/go/internal/client"
	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/storage"
	"arkade-poker/go/internal/ui"
	booba "github.com/NimbleMarkets/go-booba"
	"github.com/atotto/clipboard"
)

func browserConfig(name, fallback string) string {
	c := js.Global().Get("pokerConfig")
	if c.Type() == js.TypeObject {
		if v := c.Get(name); v.Type() == js.TypeString && v.String() != "" {
			return v.String()
		}
	}
	return fallback
}

func newHost() (ui.Host, func(), error) {
	settings := appconfig.Defaults()
	if config := js.Global().Get("pokerConfig"); config.Type() == js.TypeObject {
		data := js.Global().Get("JSON").Call("stringify", config).String()
		if err := json.Unmarshal([]byte(data), &settings); err != nil {
			return ui.Host{}, nil, err
		}
	}
	settings, err := settings.Resolve()
	if err != nil {
		return ui.Host{}, nil, err
	}
	runtime := client.New(client.Config{Game: game.Config{ArkdURL: settings.ArkdURL,
		EmulatorURL:  settings.EmulatorURL,
		DelegatorURL: settings.DelegatorURL,
		IndexerURL:   settings.IndexerURL},
		Open: func(ctx context.Context, public [32]byte) (storage.Log, error) {
			return storage.Open(ctx, browserConfig("storage", "default"), public)
		}, Clear: func(ctx context.Context, public [32]byte) error {
			return storage.Clear(ctx, browserConfig("storage", "default"), public)
		}, Connect: connectServices})
	h := ui.Host{Browser: true, CopyText: clipboard.WriteAll, ConnectSession: runtime.Open, ClearSavedGame: runtime.ClearSavedGame, RelayURL: settings.RelayURL, DefaultTerms: settings.Terms}
	h.DiscoverNetwork = func(ctx context.Context) (string, error) {
		return discoverNetwork(ctx, settings.ArkdURL)
	}
	return h, runtime.Close, nil
}

func routeSignals(*booba.Program) func() { return func() {} }
