// Package buildinfo identifies the application build shown on the welcome screen.
package buildinfo

// Version is set by the native, browser and Nix builders. Source archives retain
// the same value here so rebuilding without a Git checkout preserves the label.
var Version = "dev"
