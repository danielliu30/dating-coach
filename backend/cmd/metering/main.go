// Metering is the ops read-side service: it reads the metrics data store
// (JSONL files the collector appends to the shared app-logs volume — docker
// stats, service replicas, aggregated edge requests and deploy events),
// aggregates them into a snapshot and serves it with the embedded dashboard.
// It never touches the docker socket or the app services.
package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed static/dashboard.html static/dashboard.js
var dashboard embed.FS

// statsLine is one docker stats row emitted by metrics-collect.sh
// (docker stats --format '{{json .}}' with ts/started injected).
type statsLine struct {
	TS       string `json:"ts"`
	Started  string `json:"started"`
	Name     string `json:"Name"`
	CPUPerc  string `json:"CPUPerc"`
	MemUsage string `json:"MemUsage"` // "8.2MiB / 16GiB"
	NetIO    string `json:"NetIO"`    // "85kB / 89kB"
	MemPerc  string `json:"MemPerc"`
	PIDs     string `json:"PIDs"`
}

type svcLine struct {
	Name     string `json:"Name"`
	Replicas string `json:"Replicas"` // "1/1"
	Image    string `json:"Image"`
}

type reqLine struct {
	Bucket   string `json:"bucket"`
	Requests uint64 `json:"requests"`
	S2xx     uint64 `json:"s2xx"`
	S4xx     uint64 `json:"s4xx"`
	S5xx     uint64 `json:"s5xx"`
}

type serviceView struct {
	Name    string `json:"name"`
	Desired int    `json:"desired"`
	Running int    `json:"running"`
	Image   string `json:"image"`
	UptimeS int64  `json:"uptime_seconds"`
}

type taskView struct {
	Name      string    `json:"name"`
	StartedAt time.Time `json:"started_at"`
	CPUPerc   float64   `json:"cpu_perc"`
	MemBytes  uint64    `json:"mem_bytes"`
	MemPerc   float64   `json:"mem_perc"`
	NetRx     uint64    `json:"net_rx"`
	NetTx     uint64    `json:"net_tx"`
	Pids      uint64    `json:"pids"`
}

type bucketView struct {
	Requests uint64 `json:"requests"`
	S2xx     uint64 `json:"s2xx"`
	S4xx     uint64 `json:"s4xx"`
	S5xx     uint64 `json:"s5xx"`
}

type snapshot struct {
	TS       time.Time              `json:"ts"`
	Services []serviceView          `json:"services"`
	Tasks    []taskView             `json:"tasks"`
	Requests map[string]*bucketView `json:"requests"`
	Deploys  []json.RawMessage      `json:"deploys"`
	CPUHist  map[string][]float64   `json:"cpu_hist"`
}

func parsePct(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	return v
}

func parseBytes(s string) uint64 {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	v, _ := strconv.ParseFloat(s[:i], 64)
	switch strings.TrimSpace(s[i:]) {
	case "KiB":
		v *= 1 << 10
	case "MiB":
		v *= 1 << 20
	case "GiB":
		v *= 1 << 30
	case "TiB":
		v *= 1 << 40
	case "kB":
		v *= 1e3
	case "MB":
		v *= 1e6
	case "GB":
		v *= 1e9
	case "TB":
		v *= 1e12
	}
	return uint64(v)
}

func parsePair(s string) (uint64, uint64) {
	parts := strings.SplitN(s, " / ", 2)
	if len(parts) != 2 {
		return parseBytes(s), 0
	}
	return parseBytes(parts[0]), parseBytes(parts[1])
}

func parseUint(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return n
}

// tailLines returns up to maxBytes of complete lines from the end of path.
// The store files are append-only JSONL, so the tail is what the dashboard
// needs; a rotated/truncated file just yields fewer lines.
func tailLines(path string, maxBytes int64) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var off int64
	if info, err := f.Stat(); err == nil && info.Size() > maxBytes {
		off = info.Size() - maxBytes
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	if off > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		} else {
			return nil
		}
	}
	lines := strings.Split(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func taskFromStats(l statsLine) taskView {
	tv := taskView{Name: l.Name, CPUPerc: parsePct(l.CPUPerc), MemPerc: parsePct(l.MemPerc), Pids: parseUint(l.PIDs)}
	tv.MemBytes, _ = parsePair(l.MemUsage)
	tv.NetRx, tv.NetTx = parsePair(l.NetIO)
	if t, err := time.Parse(time.RFC3339Nano, l.Started); err == nil {
		tv.StartedAt = t
	}
	return tv
}

type reader struct {
	statsLog, servicesLog, requestsLog, deployLog string
	histCap                                       int

	mu   sync.Mutex
	snap snapshot
}

func newReader(dir string, histCap int) *reader {
	return &reader{
		statsLog:    filepath.Join(dir, "metrics-stats.jsonl"),
		servicesLog: filepath.Join(dir, "metrics-services.jsonl"),
		requestsLog: filepath.Join(dir, "metrics-requests.jsonl"),
		deployLog:   filepath.Join(dir, "deploy-history.jsonl"),
		histCap:     histCap,
	}
}

// collect aggregates the data-store files into one snapshot.
func (r *reader) collect() snapshot {
	snap := snapshot{TS: time.Now().UTC(), CPUHist: map[string][]float64{}, Requests: map[string]*bucketView{}}

	latest := map[string]taskView{}
	for _, line := range tailLines(r.statsLog, 8<<20) {
		var l statsLine
		if err := json.Unmarshal([]byte(line), &l); err != nil || l.Name == "" {
			continue
		}
		tv := taskFromStats(l)
		latest[l.Name] = tv
		snap.CPUHist[l.Name] = append(snap.CPUHist[l.Name], tv.CPUPerc)
	}
	for _, t := range latest {
		snap.Tasks = append(snap.Tasks, t)
	}
	for name, h := range snap.CPUHist {
		if len(h) > r.histCap {
			snap.CPUHist[name] = h[len(h)-r.histCap:]
		}
	}

	svcLatest := map[string]svcLine{}
	for _, line := range tailLines(r.servicesLog, 2<<20) {
		var l svcLine
		if err := json.Unmarshal([]byte(line), &l); err != nil || l.Name == "" {
			continue
		}
		svcLatest[l.Name] = l
	}
	now := time.Now()
	for _, l := range svcLatest {
		v := serviceView{Name: l.Name, Image: l.Image}
		if parts := strings.SplitN(l.Replicas, "/", 2); len(parts) == 2 {
			v.Running = int(parseUint(parts[0]))
			v.Desired = int(parseUint(parts[1]))
		}
		var earliest time.Time
		for _, t := range snap.Tasks {
			if strings.HasPrefix(t.Name, l.Name+".") && !t.StartedAt.IsZero() && (earliest.IsZero() || t.StartedAt.Before(earliest)) {
				earliest = t.StartedAt
			}
		}
		if !earliest.IsZero() {
			v.UptimeS = int64(now.Sub(earliest).Seconds())
		}
		snap.Services = append(snap.Services, v)
	}
	sort.Slice(snap.Tasks, func(i, j int) bool { return snap.Tasks[i].Name < snap.Tasks[j].Name })
	sort.Slice(snap.Services, func(i, j int) bool { return snap.Services[i].Name < snap.Services[j].Name })

	for _, line := range tailLines(r.requestsLog, 4<<20) {
		var l reqLine
		if err := json.Unmarshal([]byte(line), &l); err != nil || l.Bucket == "" {
			continue
		}
		b := snap.Requests[l.Bucket]
		if b == nil {
			b = &bucketView{}
			snap.Requests[l.Bucket] = b
		}
		b.Requests += l.Requests
		b.S2xx += l.S2xx
		b.S4xx += l.S4xx
		b.S5xx += l.S5xx
	}
	snap.Deploys = r.readDeploys()
	return snap
}

func (r *reader) readDeploys() []json.RawMessage {
	lines := tailLines(r.deployLog, 1<<20)
	if len(lines) > 30 {
		lines = lines[len(lines)-30:]
	}
	deploys := make([]json.RawMessage, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- { // newest first
		if json.Valid([]byte(lines[i])) {
			deploys = append(deploys, json.RawMessage(lines[i]))
		}
	}
	return deploys
}

func (r *reader) loop(interval time.Duration) {
	for {
		start := time.Now()
		snap := r.collect()
		r.mu.Lock()
		r.snap = snap
		r.mu.Unlock()
		time.Sleep(interval - time.Since(start))
	}
}

func (r *reader) latest(w http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snap.TS.IsZero() {
		http.Error(w, "no data yet", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(r.snap)
}

func main() {
	port := envOr("PORT", "9090")
	interval, _ := strconv.Atoi(envOr("SCRAPE_INTERVAL_SECONDS", "15"))
	capN, _ := strconv.Atoi(envOr("HISTORY_CAP", "240"))
	if interval < 1 {
		interval = 15
	}
	if capN < 1 {
		capN = 240
	}
	r := newReader(envOr("METRICS_DIR", "/logs"), capN)
	go r.loop(time.Duration(interval) * time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /api/snapshot", r.latest)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/" {
			http.NotFound(w, req)
			return
		}
		serve(w, "static/dashboard.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("GET /dashboard.js", func(w http.ResponseWriter, _ *http.Request) {
		serve(w, "static/dashboard.js", "text/javascript; charset=utf-8")
	})
	slog.Info("metering listening", "port", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}

func serve(w http.ResponseWriter, name, contentType string) {
	b, err := dashboard.ReadFile(name)
	if err != nil {
		http.Error(w, "asset missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Write(b)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
