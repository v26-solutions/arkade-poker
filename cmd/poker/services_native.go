//go:build !js

package main

import (
	"context"

	adapter "arkade-poker/go/internal/adapters/grpc"
	"arkade-poker/go/internal/adapters/websocket"
	"arkade-poker/go/internal/client"
	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/nostr"
	"arkade-poker/go/internal/wallet"
)

func discoverNetwork(ctx context.Context, endpoint string) (string, error) {
	ark, err := adapter.NewArkd(endpoint)
	if err != nil {
		return "", err
	}
	defer ark.Close()
	info, err := ark.Info(ctx)
	if err != nil {
		return "", err
	}
	return info.Network, nil
}

func connectServices(ctx context.Context, cfg game.Config) (client.Connections, error) {
	if err := ctx.Err(); err != nil {
		return client.Connections{}, err
	}
	ark, err := adapter.NewArkd(cfg.ArkdURL)
	if err != nil {
		return client.Connections{}, err
	}
	emu, err := adapter.NewEmulator(cfg.EmulatorURL)
	if err != nil {
		_ = ark.Close()
		return client.Connections{}, err
	}
	index, err := adapter.NewIndexer(cfg.IndexerURL)
	if err != nil {
		_ = emu.Close()
		_ = ark.Close()
		return client.Connections{}, err
	}
	delegate, err := connectDelegator(cfg.DelegatorURL)
	if err != nil {
		_ = index.Close()
		_ = emu.Close()
		_ = ark.Close()
		return client.Connections{}, err
	}
	closeServices := func() {
		if delegate != nil {
			_ = delegate.Close()
		}
		_ = index.Close()
		_ = emu.Close()
		_ = ark.Close()
	}
	peer, err := nostr.New(nostr.Config{Dial: websocket.Dial})
	if err != nil {
		closeServices()
		return client.Connections{}, err
	}
	return client.Connections{Services: wallet.Services{Arkd: ark, Emulator: emu, Delegator: delegate, Indexer: index},
		Subscriptions: index, Transport: peer, Close: closeServices}, nil
}
