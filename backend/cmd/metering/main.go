// Metering is the ops sidecar: it collects swarm service/task state, container
// resource stats and the nginx edge log into an in-memory snapshot served to
// the metrics dashboard. Everything is gathered from outside the app — the app
// carries no metrics code or endpoints.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed static/dashboard.html static/dashboard.js
var dashboard embed.FS

const dockerAPIVersion = "v1.43"

type dockerClient struct {
	http *http.Client
	base string
}

func newDockerClient(socket string) *dockerClient {
	return &dockerClient{base: "http://docker", http: &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return new(net.Dialer).DialContext(ctx, "unix", socket)
		}},
	}}
}

func (d *dockerClient) get(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base+"/"+dockerAPIVersion+path, nil)
	if err != nil {
		return err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("docker api " + resp.Status + " for " + path)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

type service struct {
	ID   string `json:"ID"`
	Spec struct {
		Name         string `json:"Name"`
		TaskTemplate struct {
			ContainerSpec struct {
				Image string `json:"Image"`
			} `json:"ContainerSpec"`
		} `json:"TaskTemplate"`
		Mode struct {
			Replicated *struct {
				Replicas uint64 `json:"Replicas"`
			} `json:"Replicated"`
		} `json:"Mode"`
	} `json:"Spec"`
}

type swarmTask struct {
	ServiceID    string `json:"ServiceID"`
	DesiredState string `json:"DesiredState"`
	Status       struct {
		State string `json:"State"`
	} `json:"Status"`
}

type containerSummary struct {
	ID     string   `json:"Id"`
	Names  []string `json:"Names"`
	Image  string   `json:"Image"`
	State  string   `json:"State"`
	Status string   `json:"Status"`
}

type statsJSON struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     uint32 `json:"online_cpus"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64 `json:"usage"`
		Limit uint64 `json:"limit"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	PidsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

type cpuPrev struct{ total, system uint64 }

type serviceView struct {
	Name    string `json:"name"`
	Desired uint64 `json:"desired"`
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

type logLine struct {
	URI    string `json:"uri"`
	Status int    `json:"status"`
}

type snapshot struct {
	TS       time.Time              `json:"ts"`
	Services []serviceView          `json:"services"`
	Tasks    []taskView             `json:"tasks"`
	Requests map[string]*bucketView `json:"requests"`
	Deploys  []json.RawMessage      `json:"deploys"`
	CPUHist  map[string][]float64   `json:"cpu_hist"`
}

type collector struct {
	docker    *dockerClient
	accessLog string
	deployLog string
	stackName string
	cap       int

	mu       sync.Mutex
	snaps    []snapshot
	prev     map[string]cpuPrev
	prevTask map[string]taskView
	counters map[string]*bucketView
	offset   int64
	buf      string
}

func newCollector(d *dockerClient, accessLog, deployLog, stackName string, cap int) *collector {
	return &collector{
		docker: d, accessLog: accessLog, deployLog: deployLog, stackName: stackName, cap: cap,
		prev:     map[string]cpuPrev{},
		prevTask: map[string]taskView{},
		counters: map[string]*bucketView{"api": {}, "web": {}, "metrics": {}},
	}
}

func bucketFor(uri string) string {
	switch {
	case strings.HasPrefix(uri, "/api/"):
		return "api"
	case strings.HasPrefix(uri, "/metrics"):
		return "metrics"
	default:
		return "web"
	}
}

// countAccessLog reads only the bytes appended since the last pass and folds
// each request line into cumulative per-bucket counters. Rotation-safe: a
// shrunken file restarts the offset without losing counters.
func (c *collector) countAccessLog() {
	f, err := os.Open(c.accessLog)
	if err != nil {
		return
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() < c.offset {
		c.offset, c.buf = 0, ""
	}
	if _, err := f.Seek(c.offset, io.SeekStart); err != nil {
		return
	}
	chunk, err := io.ReadAll(f)
	if err != nil || len(chunk) == 0 {
		return
	}
	if pos, err := f.Seek(0, io.SeekCurrent); err == nil {
		c.offset = pos
	}
	data := c.buf + string(chunk)
	if i := strings.LastIndexByte(data, '\n'); i >= 0 {
		c.buf = data[i+1:]
		data = data[:i+1]
	} else {
		c.buf = data
		return
	}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var l logLine
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			continue
		}
		b := c.counters[bucketFor(l.URI)]
		b.Requests++
		switch {
		case l.Status >= 500:
			b.S5xx++
		case l.Status >= 400:
			b.S4xx++
		default:
			b.S2xx++
		}
	}
}

func (c *collector) readDeploys() []json.RawMessage {
	raw, err := os.ReadFile(c.deployLog)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
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

func (c *collector) collect(ctx context.Context) snapshot {
	snap := snapshot{TS: time.Now().UTC(), CPUHist: map[string][]float64{}}
	now := time.Now()

	var services []service
	if err := c.docker.get(ctx, "/services", &services); err != nil {
		slog.Warn("docker services", "err", err)
	}
	running := map[string]int{}
	var tasks []swarmTask
	if err := c.docker.get(ctx, "/tasks?filters="+url.QueryEscape(`{"desired-state":["running"]}`), &tasks); err != nil {
		slog.Warn("docker tasks", "err", err)
	}
	for _, t := range tasks {
		if t.Status.State == "running" {
			running[t.ServiceID]++
		}
	}
	for _, s := range services {
		v := serviceView{Name: s.Spec.Name, Running: running[s.ID], Image: s.Spec.TaskTemplate.ContainerSpec.Image}
		if s.Spec.Mode.Replicated != nil {
			v.Desired = s.Spec.Mode.Replicated.Replicas
		}
		snap.Services = append(snap.Services, v)
	}

	var containers []containerSummary
	if err := c.docker.get(ctx, "/containers/json", &containers); err != nil {
		slog.Warn("docker containers", "err", err)
	}
	for _, ctr := range containers {
		name := strings.TrimPrefix(ctr.Names[0], "/")
		if c.stackName != "" && !strings.HasPrefix(name, c.stackName+"_") {
			continue
		}
		var st statsJSON
		if err := c.docker.get(ctx, "/containers/"+ctr.ID+"/stats?stream=false&one-shot=true", &st); err != nil {
			continue
		}
		var insp struct {
			State struct {
				StartedAt string `json:"StartedAt"`
			} `json:"State"`
		}
		tv := taskView{Name: name, Pids: st.PidsStats.Current}
		if err := c.docker.get(ctx, "/containers/"+ctr.ID+"/json", &insp); err == nil {
			if t, err := time.Parse(time.RFC3339Nano, insp.State.StartedAt); err == nil {
				tv.StartedAt = t
			}
		}
		tv.MemBytes, tv.MemPerc = st.MemoryStats.Usage, pct(st.MemoryStats.Usage, st.MemoryStats.Limit)
		for _, n := range st.Networks {
			tv.NetRx += n.RxBytes
			tv.NetTx += n.TxBytes
		}
		if p, ok := c.prev[ctr.ID]; ok {
			cpuDelta := st.CPUStats.CPUUsage.TotalUsage - p.total
			sysDelta := st.CPUStats.SystemCPUUsage - p.system
			if sysDelta > 0 {
				cpus := st.CPUStats.OnlineCPUs
				if cpus == 0 {
					cpus = 1
				}
				tv.CPUPerc = float64(cpuDelta) / float64(sysDelta) * float64(cpus) * 100
			}
		}
		c.prev[ctr.ID] = cpuPrev{st.CPUStats.CPUUsage.TotalUsage, st.CPUStats.SystemCPUUsage}
		c.prevTask[ctr.ID] = tv
		snap.Tasks = append(snap.Tasks, tv)
	}
	sort.Slice(snap.Tasks, func(i, j int) bool { return snap.Tasks[i].Name < snap.Tasks[j].Name })
	sort.Slice(snap.Services, func(i, j int) bool { return snap.Services[i].Name < snap.Services[j].Name })

	// Service uptime = age of the oldest running task.
	for i, s := range snap.Services {
		var earliest time.Time
		for _, t := range snap.Tasks {
			if strings.HasPrefix(t.Name, s.Name+".") && !t.StartedAt.IsZero() && (earliest.IsZero() || t.StartedAt.Before(earliest)) {
				earliest = t.StartedAt
			}
		}
		if !earliest.IsZero() {
			snap.Services[i].UptimeS = int64(now.Sub(earliest).Seconds())
		}
	}

	c.countAccessLog()
	snap.Requests = c.counters
	snap.Deploys = c.readDeploys()

	c.mu.Lock()
	c.snaps = append(c.snaps, snap)
	if len(c.snaps) > c.cap {
		c.snaps = c.snaps[len(c.snaps)-c.cap:]
	}
	for _, h := range c.snaps {
		for _, t := range h.Tasks {
			snap.CPUHist[t.Name] = append(snap.CPUHist[t.Name], t.CPUPerc)
		}
	}
	c.mu.Unlock()
	return snap
}

func pct(used, limit uint64) float64 {
	if limit == 0 {
		return 0
	}
	return float64(used) / float64(limit) * 100
}

func (c *collector) loop(ctx context.Context, interval time.Duration) {
	for {
		start := time.Now()
		c.collect(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval - time.Since(start)):
		}
	}
}

func (c *collector) latest(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.snaps) == 0 {
		http.Error(w, "no data yet", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(c.snaps[len(c.snaps)-1])
}

func main() {
	port := envOr("PORT", "9090")
	stack := envOr("STACK_NAME", "dating-coach")
	interval, _ := strconv.Atoi(envOr("SCRAPE_INTERVAL_SECONDS", "15"))
	capN, _ := strconv.Atoi(envOr("HISTORY_CAP", "240"))
	if interval < 1 {
		interval = 15
	}
	if capN < 1 {
		capN = 240
	}
	c := newCollector(newDockerClient(envOr("DOCKER_SOCKET", "/var/run/docker.sock")),
		envOr("ACCESS_LOG", "/logs/metrics-access.log"),
		envOr("DEPLOY_LOG", "/logs/deploy-history.jsonl"),
		stack, capN)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.loop(ctx, time.Duration(interval)*time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /api/snapshot", c.latest)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
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
