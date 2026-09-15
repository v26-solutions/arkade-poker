package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveDeterministicContents(t *testing.T) {
	dir := t.TempDir()
	input := entries{"z.txt": {[]byte("last"), 0o644}, "bin/run": {[]byte("first"), 0o755}}
	var previous []byte
	for range 2 {
		if err := archive(dir, "example", input); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "example.tar.gz"))
		if err != nil {
			t.Fatal(err)
		}
		if previous != nil && !bytes.Equal(data, previous) {
			t.Fatal("fixed input did not produce identical archives")
		}
		previous = data
		sidecar, err := os.ReadFile(filepath.Join(dir, "example.tar.gz.sha256"))
		if err != nil || string(sidecar) != digest(data)+"  example.tar.gz\n" {
			t.Fatalf("checksum sidecar: %s, %v", sidecar, err)
		}
	}
	zipped, err := gzip.NewReader(bytes.NewReader(previous))
	if err != nil {
		t.Fatal(err)
	}
	defer zipped.Close()
	reader := tar.NewReader(zipped)
	var manifest manifest
	var names []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimPrefix(header.Name, "example/")
		names = append(names, name)
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if header.Uid != 0 || header.Gid != 0 || header.ModTime.Unix() != 0 {
			t.Fatalf("non-normalized header: %+v", header)
		}
		if name == "MANIFEST.json" {
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			continue
		}
		expected, ok := input[name]
		if !ok || !bytes.Equal(data, expected.data) || header.Mode != expected.mode {
			t.Fatalf("unexpected archive entry: %s", name)
		}
		record := manifest.Files[name]
		if record.SHA256 != digest(data) || record.Bytes != len(data) || record.Mode != header.Mode {
			t.Fatalf("manifest mismatch for %s", name)
		}
	}
	if strings.Join(names, ",") != "MANIFEST.json,bin/run,z.txt" || len(manifest.Files) != 2 {
		t.Fatalf("unexpected archive inventory: %v", names)
	}
}

func TestWebReleaseVerifiesManifest(t *testing.T) {
	dir := t.TempDir()
	data := []byte("current")
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", data)
	write("old.js", []byte("stale"))
	m := manifest{Schema: 1, Files: map[string]fileRecord{"index.html": {Bytes: len(data), SHA256: digest(data)}}}
	setManifest := func() {
		t.Helper()
		data, err := jsonBytes(m)
		if err != nil {
			t.Fatal(err)
		}
		write("manifest.json", data)
	}
	setManifest()
	e, err := webEntries(dir)
	if err != nil || len(e) != 2 {
		t.Fatalf("must include only manifest and current asset: %v, %v", e, err)
	}
	write("index.html", []byte("changed"))
	if _, err := webEntries(dir); err == nil {
		t.Fatal("accepted modified asset")
	}
	for _, name := range []string{"../outside", "..", ".", "/absolute", `..\outside`} {
		m.Files = map[string]fileRecord{name: {}}
		setManifest()
		if _, err := webEntries(dir); err == nil {
			t.Fatalf("accepted unsafe path %q", name)
		}
	}
}

func TestQualificationServer(t *testing.T) {
	for _, kind := range []string{"storage", "shuffle"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "app.wasm"), []byte("\x00asm"), 0o644); err != nil {
				t.Fatal(err)
			}
			handler := pageHandler(dir, kind)
			request := func(method, path, body string, status int) *httptest.ResponseRecorder {
				t.Helper()
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
				if w.Code != status {
					t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, status, w.Body)
				}
				return w
			}
			w := request("GET", "/app.wasm", "", http.StatusOK)
			if w.Header().Get("Content-Type") != "application/wasm" {
				t.Fatal("missing WASM MIME type")
			}
			valid, filename := `{"mode":"suite","output":"PASS"}`, "suite.json"
			if kind == "shuffle" {
				valid = `{"Operations":[],"SpinnerFrames":1,"DistinctFrames":1,"Error":"","BrowserAnimationFrames":1,"MaxAnimationGapMs":0,"UserAgent":"test"}`
				filename = "brave-result.json"
			}
			request("POST", "/qualification-result", valid, http.StatusNoContent)
			data, err := os.ReadFile(filepath.Join(dir, filename))
			if err != nil || !json.Valid(data) {
				t.Fatalf("missing valid saved report: %v", err)
			}
			for _, invalid := range []string{"{", "null", "[]", `{}`, `{"mode":"../../escape"}`, valid + "{}", strings.Repeat("x", 65537)} {
				request("POST", "/qualification-result", invalid, http.StatusBadRequest)
			}
			request("POST", "/other", valid, http.StatusMethodNotAllowed)
		})
	}
}
