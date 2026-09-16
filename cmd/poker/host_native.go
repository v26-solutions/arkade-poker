//go:build !js

package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"arkade-poker/go/internal/appconfig"
	"arkade-poker/go/internal/client"
	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/storage"
	"arkade-poker/go/internal/ui"
	"arkade-poker/go/internal/wallet"
	tea "charm.land/bubbletea/v2"
	booba "github.com/NimbleMarkets/go-booba"
	"github.com/atotto/clipboard"
)

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func newHost() (ui.Host, func(), error) {
	settings, err := appconfig.FromEnv(os.Getenv)
	if err != nil {
		return ui.Host{}, nil, err
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		return ui.Host{}, nil, err
	}
	cfg := client.Config{Game: game.Config{ArkdURL: settings.ArkdURL,
		EmulatorURL:  settings.EmulatorURL,
		DelegatorURL: settings.DelegatorURL,
		IndexerURL:   settings.IndexerURL},
		Open: func(ctx context.Context, public [32]byte) (storage.Log, error) {
			return storage.Open(ctx, env("POKER_DATA_DIR", filepath.Join(directory, "arkade-poker-go")), public)
		}, Clear: func(ctx context.Context, public [32]byte) error {
			return storage.Clear(ctx, env("POKER_DATA_DIR", filepath.Join(directory, "arkade-poker-go")), public)
		}, Connect: connectServices}
	runtime := client.New(cfg)
	h := ui.Host{CopyText: clipboard.WriteAll, ConnectSession: runtime.Open, ClearSavedGame: runtime.ClearSavedGame, AbortSetup: runtime.AbortSetup, RelayURL: settings.RelayURL, DefaultTerms: settings.Terms}
	h.DiscoverNetwork = func(ctx context.Context) (string, error) {
		return discoverNetwork(ctx, settings.ArkdURL)
	}
	if text := os.Getenv("POKER_WALLET_KEY"); text != "" {
		os.Unsetenv("POKER_WALLET_KEY")
		h.InitialKey, err = parseInitialKey(context.Background(), text, h.DiscoverNetwork)
		if err != nil {
			h.InitialError = err.Error()
		}
	}
	return h, func() { runtime.Close(); h.InitialKey.Destroy() }, nil
}

// Raw keys can be imported immediately; mnemonic derivation needs Arkd's network.
func parseInitialKey(ctx context.Context, text string, discover func(context.Context) (string, error)) (*wallet.Key, error) {
	key, err := wallet.ParseKey(text, "")
	if !errors.Is(err, wallet.ErrKeyNetwork) {
		return key, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	network, err := discover(ctx)
	if err != nil {
		return nil, err
	}
	return wallet.ParseKey(text, network)
}

func routeSignals(p *booba.Program) func() {
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(ch, os.Interrupt)
	go func() {
		for {
			select {
			case <-ch:
				p.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			case <-done:
				return
			}
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}
