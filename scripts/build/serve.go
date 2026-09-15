package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func serve(kind string) error {
	directory, port := "build/"+kind+"-qualification", ""
	switch kind {
	case "web":
		directory, port = "build/web", "5173"
	case "shuffle":
		port = "5174"
	case "storage":
		port = "5175"
	case "ui":
		port = "5176"
	default:
		return fmt.Errorf("unknown server: %s", kind)
	}
	if _, err := os.Stat(filepath.Join(directory, "index.html")); err != nil {
		return fmt.Errorf("build %s first: %w", kind, err)
	}
	server := &http.Server{
		Addr: "127.0.0.1:" + port, Handler: pageHandler(directory, kind),
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Printf("%s: http://%s\n", kind, server.Addr)
	return server.ListenAndServe()
}

func pageHandler(directory, kind string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.Dir(directory)))
	if kind == "web" || kind == "ui" {
		return mux
	}
	mux.HandleFunc("POST /qualification-result", func(w http.ResponseWriter, r *http.Request) {
		limit := int64(65536)
		if kind == "shuffle" {
			limit = 32768
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		var result map[string]json.RawMessage
		if err != nil || json.Unmarshal(data, &result) != nil || result == nil {
			http.Error(w, "invalid report", http.StatusBadRequest)
			return
		}
		name := "brave-result.json"
		if kind == "shuffle" {
			keys := []string{"Operations", "SpinnerFrames", "DistinctFrames", "Error", "BrowserAnimationFrames", "MaxAnimationGapMs", "UserAgent"}
			valid := len(result) == len(keys)
			for _, key := range keys {
				valid = valid && result[key] != nil
			}
			if !valid {
				http.Error(w, "unexpected report", http.StatusBadRequest)
				return
			}
		} else {
			var mode string
			if json.Unmarshal(result["mode"], &mode) != nil || (mode != "suite" && mode != "hold" && mode != "probe" && mode != "cleanup") {
				http.Error(w, "invalid mode", http.StatusBadRequest)
				return
			}
			name = mode + ".json"
		}
		data, err = jsonBytes(result)
		if err == nil {
			// Atomic replacement also keeps simultaneous test reports intact.
			err = writeReport(filepath.Join(directory, name), data)
		}
		if err != nil {
			http.Error(w, "cannot save report", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func writeReport(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".report-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
