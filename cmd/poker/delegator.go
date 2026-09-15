package main

import (
	adapter "arkade-poker/go/internal/adapters/http"
	"arkade-poker/go/internal/ports"
)

func connectDelegator(endpoint string) (ports.Delegator, error) {
	// An explicitly empty runtime endpoint selects the nondelegated tree.
	if endpoint == "" {
		return nil, nil
	}
	return adapter.NewDelegator(endpoint)
}
