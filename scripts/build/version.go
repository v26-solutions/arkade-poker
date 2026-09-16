package main

import (
	"os"
	"os/exec"
	"strings"

	"arkade-poker/go/internal/buildinfo"
)

func buildVersion() string {
	if buildinfo.Version != "dev" {
		return buildinfo.Version
	}
	for _, args := range [][]string{
		{"describe", "--tags", "--exact-match", "HEAD"},
		{"rev-parse", "--short=12", "HEAD"},
	} {
		if output, err := exec.Command("git", args...).Output(); err == nil {
			return strings.TrimSpace(string(output))
		}
	}
	return buildinfo.Version
}

func versionLDFlags() string {
	return "-X=arkade-poker/go/internal/buildinfo.Version=" + buildVersion()
}

func buildNative() error {
	if err := os.MkdirAll("build", 0o755); err != nil {
		return err
	}
	return goRun("", []string{"CGO_ENABLED=0"}, "build", "-tags=purego", "-trimpath", "-buildvcs=false", "-ldflags", versionLDFlags(), "-o", "build/poker", "./cmd/poker")
}
