// lt-dash serves a tiny live dashboard over the out dir: one HTML page that
// polls the live-<component>.json snapshots each component's Progress
// reporter writes, rendered as a table. Start it before the suite and watch
// load build stage by stage.
package main

import (
	"embed"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed index.html
var page embed.FS

func main() {
	out := flag.String("out", "out", "report directory holding live-*.json")
	addr := flag.String("addr", ":8099", "listen address")
	flag.Parse()

	http.HandleFunc("/api/live", func(w http.ResponseWriter, r *http.Request) {
		paths, _ := filepath.Glob(filepath.Join(*out, "live-*.json"))
		sort.Strings(paths)
		var states []json.RawMessage
		for _, p := range paths {
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			states = append(states, json.RawMessage(data))
		}
		if states == nil {
			states = []json.RawMessage{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(states)
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, err := page.ReadFile("index.html")
		if err != nil || !strings.HasPrefix(r.URL.Path, "/") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})

	log.Printf("lt-dash: live dashboard on http://localhost%s (out=%s)", *addr, *out)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
