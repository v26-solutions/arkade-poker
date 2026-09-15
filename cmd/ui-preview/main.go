//go:build uipreview

// ui-preview displays the shared UI without connecting a wallet or services.
// F1/F2 switch fixtures. Run with: go run -tags=uipreview ./cmd/ui-preview
package main

import (
	"fmt"
	"os"

	"arkade-poker/go/internal/ui"
	booba "github.com/NimbleMarkets/go-booba"
)

func main() {
	if _, err := booba.NewProgram(ui.NewPreview()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
