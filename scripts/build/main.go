// Build, package and serve from the repository root: go run ./scripts/build.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "web" {
		settings, err := webConfig(args[1:])
		if err != nil {
			return err
		}
		return buildWeb("./cmd/poker", "build/web", "", settings)
	}
	if len(args) == 1 {
		switch args[0] {
		case "source":
			return sourceRelease()
		case "artifacts":
			return artifactRelease()
		}
	}
	if len(args) == 2 {
		switch args[0] {
		case "qualify":
			return qualify(args[1])
		case "serve":
			return serve(args[1])
		}
	}
	return fmt.Errorf("usage: go run ./scripts/build web [flags]|source|artifacts|qualify <shuffle|storage|transport>|serve <web|shuffle|storage>")
}

func goCommand(dir string, env []string, args ...string) *exec.Cmd {
	tool := os.Getenv("GO")
	if tool == "" {
		tool = "go"
	}
	cmd := exec.Command(tool, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stderr = os.Stderr
	return cmd
}

func goRun(dir string, env []string, args ...string) error {
	cmd := goCommand(dir, env, args...)
	cmd.Stdout = os.Stdout
	return cmd.Run()
}

func goOutput(args ...string) ([]byte, error) {
	return goCommand("", nil, args...).Output()
}

type module struct {
	Path, Version, Sum string
	Dir                string  `json:",omitempty"`
	Main               bool    `json:",omitempty"`
	Replace            *module `json:",omitempty"`
}

func findModule(path string) (module, error) {
	data, err := goOutput("list", "-m", "-json", path)
	if err != nil {
		return module{}, err
	}
	var m module
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.Replace != nil {
		return m, fmt.Errorf("build assets require an unmodified module: %s", path)
	}
	if m.Dir == "" {
		if err := goRun("", nil, "mod", "download", path); err != nil {
			return m, err
		}
		data, err = goOutput("list", "-m", "-json", path)
		if err != nil {
			return m, err
		}
		err = json.Unmarshal(data, &m)
	}
	return m, err
}

func copyFile(source, target string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	// Older builders copied the read-only mode from the Go module cache.
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}

func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unexpected non-regular source: %s", path)
		}
		return copyFile(path, dest)
	})
}

func copyRuntime(output string) error {
	root, err := goOutput("env", "GOROOT")
	if err != nil {
		return err
	}
	return copyFile(filepath.Join(strings.TrimSpace(string(root)), "lib/wasm/wasm_exec.js"), filepath.Join(output, "wasm_exec.js"))
}

func digest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

type fileRecord struct {
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
	Mode   int64  `json:"mode,omitempty"`
}

type manifest struct {
	Schema int                   `json:"schema"`
	Files  map[string]fileRecord `json:"files"`
}

func jsonBytes(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	return append(data, '\n'), err
}
