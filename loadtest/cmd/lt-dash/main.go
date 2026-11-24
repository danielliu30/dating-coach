// lt-dash serves a tiny live dashboard over the out dir: one HTML page that
// polls the live-<component>.json snapshots each component's Progress
// reporter writes, plus a /log terminal view that tails the run log.
package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
)

//go:embed index.html log.html
var pages embed.FS

const maxLogBytes = 512 << 10 // serve the tail beyond this

func main() {
	out := flag.String("out", "out", "report directory holding live-*.json")
	logPath := flag.String("log", "", "run log file to serve at /log (e.g. out/run.log)")
	addr := flag.String("addr", ":8099", "listen address")
	flag.Parse()

	http.HandleFunc("/api/live", func(w http.ResponseWriter, r *http.Request) {
		paths, _ := filepath.Glob(filepath.Join(*out, "live-*.json"))
		sort.Strings(paths)
		states := []json.RawMessage{}
		for _, p := range paths {
			if data, err := os.ReadFile(p); err == nil {
				states = append(states, json.RawMessage(data))
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(states)
	})
	http.HandleFunc("/api/log", func(w http.ResponseWriter, r *http.Request) {
		if *logPath == "" {
			http.Error(w, "no log configured", http.StatusNotFound)
			return
		}
		data, err := os.ReadFile(*logPath)
		if err != nil {
			http.Error(w, "log unreadable", http.StatusNotFound)
			return
		}
		if len(data) > maxLogBytes {
			data = data[len(data)-maxLogBytes:]
			if i := bytes.IndexByte(data, '\n'); i >= 0 {
				data = data[i+1:] // start on a line boundary
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(data)
	})
	http.HandleFunc("/api/suite", func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile(filepath.Join(*out, "suite.json"))
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	})
	serve := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			data, err := pages.ReadFile(name)
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
		}
	}
	http.HandleFunc("/", serve("index.html"))
	http.HandleFunc("/log", serve("log.html"))

	log.Printf("lt-dash: dashboard http://localhost%s/  terminal http://localhost%s/log (out=%s log=%s)", *addr, *addr, *out, *logPath)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
