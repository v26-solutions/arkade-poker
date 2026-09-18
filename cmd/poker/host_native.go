//go:build !js

package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
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

func newHost() (ui.Host, func(), error) {
	settings, err := appconfig.FromEnv(os.Getenv)
	if err != nil {
		return ui.Host{}, nil, err
	}
	directory, err := dataDirectory()
	if err != nil {
		return ui.Host{}, nil, err
	}
	cfg := client.Config{Game: game.Config{ArkdURL: settings.ArkdURL,
		EmulatorURL:  settings.EmulatorURL,
		DelegatorURL: settings.DelegatorURL,
		IndexerURL:   settings.IndexerURL},
		Open: func(ctx context.Context, public [32]byte) (storage.Log, error) {
			return storage.Open(ctx, directory, public)
		}, Clear: func(ctx context.Context, public [32]byte) error {
			return storage.Clear(ctx, directory, public)
		}, Connect: func(ctx context.Context, cfg game.Config) (client.Connections, error) {
			return connectServices(ctx, cfg, settings.EmulatorPCR0)
		}}
	runtime := client.New(cfg)
	h := ui.Host{CopyText: clipboard.WriteAll, ConnectSession: runtime.Open, ClearSavedGame: runtime.ClearSavedGame, AbortSetup: runtime.AbortSetup,
		ArkdURL: settings.ArkdURL, EmulatorURL: settings.EmulatorURL, DelegatorURL: settings.DelegatorURL,
		RelayURL: settings.RelayURL, DefaultTerms: settings.Terms}
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
