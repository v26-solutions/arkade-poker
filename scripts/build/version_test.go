package main

import (
	"os/exec"
	"strings"
	"testing"

	"arkade-poker/go/internal/buildinfo"
)

func TestBuildVersion(t *testing.T) {
	previous := buildinfo.Version
	t.Cleanup(func() { buildinfo.Version = previous })
	buildinfo.Version = "dev"
	t.Chdir(t.TempDir())
	git := func(args ...string) string {
		t.Helper()
		output, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init")
	if got := buildVersion(); got != "dev" {
		t.Fatalf("unversioned build: %q", got)
	}
	git("-c", "user.name=Build Test", "-c", "user.email=build@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "initial")
	sha := git("rev-parse", "--short=12", "HEAD")
	if got := buildVersion(); got != sha {
		t.Fatalf("untagged build: %q, want %q", got, sha)
	}
	git("-c", "tag.gpgsign=false", "tag", "v1.2.3")
	if got := buildVersion(); got != "v1.2.3" {
		t.Fatalf("tagged build: %q", got)
	}
	git("-c", "user.name=Build Test", "-c", "user.email=build@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "after release")
	sha = git("rev-parse", "--short=12", "HEAD")
	if got := buildVersion(); got != sha {
		t.Fatalf("build after tag: %q, want %q", got, sha)
	}
	// An extracted source archive keeps its stamp, even inside another checkout.
	buildinfo.Version = "v1.0.0"
	if got := buildVersion(); got != "v1.0.0" {
		t.Fatalf("source archive build: %q", got)
	}
}
