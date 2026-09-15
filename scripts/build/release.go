package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type entry struct {
	data []byte
	mode int64
}

type entries map[string]entry

func (e entries) addFile(name, source string, mode int64) error {
	data, err := os.ReadFile(source)
	if err == nil {
		e[name] = entry{data, mode}
	}
	return err
}

func (e entries) addTree(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unexpected non-regular source: %s", path)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := int64(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		return e.addFile(filepath.ToSlash(path), path, mode)
	})
}

func sourceRelease() error {
	e := entries{}
	// Explicit inputs keep local env files, signer state and Docker output out
	// of source exports, while retaining the regtest helpers used by tests.
	for _, tree := range []string{"cmd", "internal", "web", "scripts", "regtest/lib", "regtest/docker", "regtest/test"} {
		if err := e.addTree(tree); err != nil {
			return err
		}
	}
	for _, name := range []string{"go.mod", "go.sum", "Makefile", "RELEASE.md", ".gitignore", "flake.nix", "flake.lock",
		"regtest/regtest.mjs", "regtest/package.json", "regtest/README.md", "regtest/.gitignore", "regtest/.env.defaults", "regtest/.env.evm-e2e"} {
		if err := e.addFile(name, name, 0o644); err != nil {
			return err
		}
	}
	return archive("build/releases", "arkade-poker-go-source", e)
}

func archive(output, name string, e entries) error {
	e = maps.Clone(e)
	files := make(map[string]fileRecord, len(e))
	for path, file := range e {
		files[path] = fileRecord{Bytes: len(file.data), SHA256: digest(file.data), Mode: file.mode}
	}
	data, err := jsonBytes(manifest{Schema: 1, Files: files})
	if err != nil {
		return err
	}
	e["MANIFEST.json"] = entry{data, 0o644}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(output, ".release-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	zipped := gzip.NewWriter(file)
	tarball := tar.NewWriter(zipped)
	paths := make([]string, 0, len(e))
	for path := range e {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		file := e[path]
		if err := tarball.WriteHeader(&tar.Header{
			Name: name + "/" + path, Mode: file.mode, Size: int64(len(file.data)), Format: tar.FormatPAX,
		}); err != nil {
			return err
		}
		if _, err := tarball.Write(file.data); err != nil {
			return err
		}
	}
	if err := tarball.Close(); err != nil {
		return err
	}
	if err := zipped.Close(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	target := filepath.Join(output, name+".tar.gz")
	if err := os.Rename(file.Name(), target); err != nil {
		return err
	}
	data, err = os.ReadFile(target)
	if err != nil {
		return err
	}
	hash := digest(data)
	if err := os.WriteFile(target+".sha256", []byte(hash+"  "+filepath.Base(target)+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", target, hash)
	return nil
}

func moduleNotices(e entries) error {
	// Download the graph so a fresh source export includes the same notices
	// as a warm cache. Never put machine-local module paths in the metadata.
	if err := goRun("", nil, "mod", "download"); err != nil {
		return err
	}
	raw, err := goOutput("list", "-m", "-json", "all")
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var modules []module
	for {
		var m module
		if err := decoder.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		if m.Main {
			continue
		}
		location := m
		if m.Replace != nil {
			location = *m.Replace
			m.Replace.Dir = ""
		}
		m.Dir = ""
		modules = append(modules, m)
		if location.Dir == "" {
			continue
		}
		files, err := os.ReadDir(location.Dir)
		if err != nil {
			return err
		}
		label := strings.ReplaceAll(m.Path, "/", "_") + "@" + location.Version
		for _, file := range files {
			name := strings.ToUpper(file.Name())
			if file.Type().IsRegular() && (strings.HasPrefix(name, "LICENSE") || strings.HasPrefix(name, "COPYING") || strings.HasPrefix(name, "NOTICE")) {
				if err := e.addFile("licenses/"+label+"/"+file.Name(), filepath.Join(location.Dir, file.Name()), 0o644); err != nil {
					return err
				}
			}
		}
	}
	data, err := jsonBytes(modules)
	e["DEPENDENCIES.json"] = entry{data, 0o644}
	return err
}

func webEntries(directory string) (entries, error) {
	e := entries{}
	if err := e.addFile("manifest.json", filepath.Join(directory, "manifest.json"), 0o644); err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(e["manifest.json"].data, &m); err != nil {
		return nil, err
	}
	if m.Schema != 1 || len(m.Files) == 0 {
		return nil, fmt.Errorf("invalid web manifest")
	}
	for name, expected := range m.Files {
		if name == "." || name == ".." || strings.ContainsAny(name, "/\\") || name == "" {
			return nil, fmt.Errorf("unsafe web manifest path: %q", name)
		}
		if err := e.addFile(name, filepath.Join(directory, name), 0o644); err != nil {
			return nil, err
		}
		data := e[name].data
		if len(data) != expected.Bytes || digest(data) != expected.SHA256 {
			return nil, fmt.Errorf("web asset differs from manifest: %s", name)
		}
	}
	return e, nil
}

func artifactRelease() error {
	native := entries{}
	if err := native.addFile("poker", "build/poker", 0o755); err != nil {
		return err
	}
	toolchain, err := goOutput("version")
	if err != nil {
		return err
	}
	native["TOOLCHAIN.txt"] = entry{toolchain, 0o644}
	if err := moduleNotices(native); err != nil {
		return err
	}
	env, err := goOutput("env", "GOOS", "GOARCH", "GOROOT")
	if err != nil {
		return err
	}
	values := strings.Split(strings.TrimSpace(string(env)), "\n")
	if len(values) != 3 {
		return fmt.Errorf("unexpected go env output")
	}
	for _, name := range []string{"LICENSE", "PATENTS"} {
		if err := native.addFile("licenses/go/"+name, filepath.Join(values[2], name), 0o644); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	web, err := webEntries("build/web")
	if err != nil {
		return err
	}
	for name, file := range native {
		if name != "poker" {
			web[name] = file
		}
	}
	if err := archive("build/releases", "arkade-poker-"+values[0]+"-"+values[1], native); err != nil {
		return err
	}
	return archive("build/releases", "arkade-poker-web", web)
}
