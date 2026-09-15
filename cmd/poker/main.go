package main

import (
	"context"
	"fmt"
	"os"

	"arkade-poker/go/internal/ui"
	tea "charm.land/bubbletea/v2"
	booba "github.com/NimbleMarkets/go-booba"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host, closeHost, err := newHost()
	if err != nil {
		return err
	}
	defer closeHost()
	p := booba.NewProgram(ui.New(ctx, host), tea.WithoutSignalHandler())
	stopSignals := routeSignals(p)
	defer stopSignals()
	_, err = p.Run()
	// The game driver uses ctx and remains live through the exit modal. Only
	// confirmed exit or a runtime failure cancels it.
	cancel()
	return err
}
