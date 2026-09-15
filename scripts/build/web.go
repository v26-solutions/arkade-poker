package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"arkade-poker/go/internal/appconfig"
	"github.com/evanw/esbuild/pkg/api"
)

func buildWASM(target, output string) error {
	output, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	temp, err := os.MkdirTemp("", "poker-wasm-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	// Bubbles imports clipboard v0.1.4, which lacks a browser backend. Stage
	// a writable copy without changing the module cache or native dependency.
	clipboard, err := findModule("github.com/atotto/clipboard")
	if err != nil {
		return err
	}
	if clipboard.Version != "v0.1.4" || clipboard.Replace != nil {
		return fmt.Errorf("review clipboard backend for changed dependency")
	}
	for _, tree := range []struct{ source, dest string }{
		{clipboard.Dir, ".clipboard"}, {"cmd", "cmd"}, {"internal", "internal"},
	} {
		if err := copyTree(tree.source, filepath.Join(temp, tree.dest)); err != nil {
			return err
		}
	}
	if err := copyFile("web/compat/clipboard_js.go.txt", filepath.Join(temp, ".clipboard/clipboard_js.go")); err != nil {
		return err
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		if err := copyFile(name, filepath.Join(temp, name)); err != nil {
			return err
		}
	}
	if err := goRun(temp, nil, "mod", "edit", "-replace=github.com/atotto/clipboard=./.clipboard"); err != nil {
		return err
	}
	// Retain upstream booba's signal/TTY stubs and temporary modfile handling.
	args := []string{"run", "github.com/NimbleMarkets/go-booba/cmd/booba-wasm-build", "-trimpath", "-buildvcs=false", "-o", output}
	if target == "./cmd/ui-preview" {
		args = append(args, "-tags=uipreview")
	}
	return goRun(temp, []string{"CGO_ENABLED=0"}, append(args, target)...)
}

func buildWeb(target, output, extraHTML string, settings appconfig.Config) error {
	settings, err := settings.Resolve()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return err
	}
	if err := buildWASM(target, filepath.Join(output, "poker.wasm")); err != nil {
		return err
	}
	// Frontend and renderer come from the same checksum-pinned Go module as
	// the runtime. go.mod is the only version pin; no npm package is needed.
	booba, err := findModule("github.com/NimbleMarkets/go-booba")
	if err != nil {
		return err
	}
	files := make(map[string]fileRecord)
	writeAsset := func(name string, data []byte, fingerprint bool) (string, error) {
		if fingerprint {
			ext := filepath.Ext(name)
			name = strings.TrimSuffix(name, ext) + "." + digest(data)[:16] + ext
		}
		err := os.WriteFile(filepath.Join(output, name), data, 0o644)
		files[name] = fileRecord{Bytes: len(data), SHA256: digest(data)}
		return name, err
	}
	copyAsset := func(source, name string, fingerprint bool) (string, error) {
		data, err := os.ReadFile(source)
		if err != nil {
			return "", err
		}
		return writeAsset(name, data, fingerprint)
	}
	wasm, err := copyAsset(filepath.Join(output, "poker.wasm"), "poker.wasm", true)
	if err != nil {
		return err
	}
	renderer, err := copyAsset(filepath.Join(booba.Dir, "serve/static/ghostty-web/ghostty-web.js"), "ghostty-web.js", true)
	if err != nil {
		return err
	}
	defines, err := frontendDefines(wasm, settings)
	if err != nil {
		return err
	}
	result := api.Build(api.BuildOptions{
		EntryPoints: []string{"web/main.ts"}, Outfile: filepath.Join(output, "main.js"),
		Bundle: true, Format: api.FormatESModule, Target: api.ES2022, LogLevel: api.LogLevelInfo,
		Define: defines,
		Alias:  map[string]string{"@nimblemarkets/booba": filepath.Join(booba.Dir, "ts/booba.ts")},
		Plugins: []api.Plugin{{Name: "pinned-ghostty", Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: "^ghostty-web$"}, func(api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: "./" + renderer, External: true}, nil
			})
		}}},
	})
	if len(result.Errors) > 0 || len(result.OutputFiles) != 1 {
		return fmt.Errorf("frontend bundle failed")
	}
	frontend, err := writeAsset("main.js", result.OutputFiles[0].Contents, true)
	if err != nil {
		return err
	}
	if err := copyRuntime(output); err != nil {
		return err
	}
	runtime, err := copyAsset(filepath.Join(output, "wasm_exec.js"), "wasm_exec.js", true)
	if err != nil {
		return err
	}
	for _, asset := range []struct{ source, name string }{
		{filepath.Join(booba.Dir, "serve/static/ghostty-web/ghostty-vt.wasm"), "ghostty-vt.wasm"},
		{"web/compat/ghostty-fs.mjs", "__vite-browser-external-2447137e.js"},
	} {
		if _, err := copyAsset(asset.source, asset.name, false); err != nil {
			return err
		}
	}
	html, err := os.ReadFile("web/index.html")
	if err != nil {
		return err
	}
	page := strings.NewReplacer("./main.js", "./"+frontend, "./wasm_exec.js", "./"+runtime,
		"</body>", extraHTML+"</body>").Replace(string(html))
	// Publish the entry page after its assets. Package only this manifest's
	// files, excluding old fingerprints and development qualification data.
	if _, err := writeAsset("index.html", []byte(page), false); err != nil {
		return err
	}
	data, err := jsonBytes(manifest{Schema: 1, Files: files})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(output, "manifest.json"), data, 0o644)
}

func qualify(kind string) error {
	if kind == "ui" {
		html, err := os.ReadFile("web/qualification/ui.html")
		if err != nil {
			return err
		}
		return buildWeb("./cmd/ui-preview", "build/ui-qualification", string(html), appconfig.Defaults())
	}
	if kind != "shuffle" && kind != "storage" && kind != "transport" {
		return fmt.Errorf("unknown qualification: %s", kind)
	}
	output := "build/" + kind + "-qualification"
	if kind == "shuffle" {
		html, err := os.ReadFile("web/qualification/shuffle.html")
		if err != nil {
			return err
		}
		return buildWeb("./cmd/shuffle-qualify", output, string(html), appconfig.Defaults())
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return err
	}
	args := []string{"test", "-c", "-o", filepath.Join(output, kind+".test.wasm")}
	if kind == "storage" {
		args = append(args, "./internal/storage")
	} else {
		args = append(args, "-tags=qualification", "./internal/adapters/http")
	}
	if err := goRun("", []string{"GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0"}, args...); err != nil {
		return err
	}
	if err := copyRuntime(output); err != nil {
		return err
	}
	return copyFile("web/qualification/"+kind+".html", filepath.Join(output, "index.html"))
}
