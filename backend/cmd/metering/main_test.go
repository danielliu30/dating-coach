package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDocker serves the docker HTTP API paths metering scrapes.
func fakeDocker(t *testing.T) *dockerClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.43/services", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{
			"ID":   "svc1",
			"Spec": map[string]any{"Name": "dating-coach_api", "Mode": map[string]any{"Replicated": map[string]any{"Replicas": 1}}, "TaskTemplate": map[string]any{"ContainerSpec": map[string]any{"Image": "img:tag"}}},
		}})
	})
	mux.HandleFunc("/v1.43/tasks", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{
			"ServiceID": "svc1", "DesiredState": "running", "Status": map[string]string{"State": "running"},
		}})
	})
	mux.HandleFunc("/v1.43/containers/json", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{
			"Id": "abc", "Names": []string{"/dating-coach_api.1.x"}, "State": "running",
		}})
	})
	mux.HandleFunc("/v1.43/containers/abc/stats", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"cpu_stats":    map[string]any{"cpu_usage": map[string]any{"total_usage": 300}, "system_cpu_usage": 1000, "online_cpus": 2},
			"memory_stats": map[string]any{"usage": 512, "limit": 1024},
			"networks":     map[string]any{"eth0": map[string]any{"rx_bytes": 7, "tx_bytes": 9}},
			"pids_stats":   map[string]any{"current": 5},
		})
	})
	mux.HandleFunc("/v1.43/containers/abc/json", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"State": map[string]any{"StartedAt": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &dockerClient{http: srv.Client(), base: srv.URL}
}

func TestCollect(t *testing.T) {
	dir := t.TempDir()
	access := filepath.Join(dir, "access.log")
	os.WriteFile(access, []byte(
		`{"uri":"/api/v1/auth/signin","status":200}`+"\n"+
			`{"uri":"/api/v1/coaches","status":500}`+"\n"+
			`{"uri":"/metrics/dashboard.js","status":200}`+"\n"+
			`{"uri":"/app.js","status":404}`+"\n"), 0o644)
	deploys := filepath.Join(dir, "deploys.jsonl")
	os.WriteFile(deploys, []byte(`{"ts":"t1","sha":"abc","result":"ok","seconds":9}`+"\n"), 0o644)

	c := newCollector(fakeDocker(t), access, deploys, "dating-coach", 10)
	snap := c.collect(context.Background())

	if len(snap.Services) != 1 || snap.Services[0].Running != 1 || snap.Services[0].Desired != 1 {
		t.Fatalf("services: %+v", snap.Services)
	}
	if snap.Services[0].UptimeS < 3500 {
		t.Fatalf("uptime: %d", snap.Services[0].UptimeS)
	}
	if len(snap.Tasks) != 1 || snap.Tasks[0].MemPerc != 50 || snap.Tasks[0].Pids != 5 {
		t.Fatalf("tasks: %+v", snap.Tasks)
	}
	if snap.Requests["api"].Requests != 2 || snap.Requests["api"].S5xx != 1 || snap.Requests["metrics"].Requests != 1 || snap.Requests["web"].S4xx != 1 {
		t.Fatalf("requests: %+v", snap.Requests)
	}
	if len(snap.Deploys) != 1 {
		t.Fatalf("deploys: %+v", snap.Deploys)
	}
}

func TestAccessLogDeltaAndRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	c := newCollector(fakeDocker(t), path, filepath.Join(t.TempDir(), "none"), "", 10)

	os.WriteFile(path, []byte(`{"uri":"/api/a","status":200}`+"\n"), 0o644)
	c.countAccessLog()
	c.countAccessLog() // no new bytes -> no double count
	if c.counters["api"].Requests != 1 {
		t.Fatalf("after first pass: %+v", c.counters["api"])
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"uri":"/api/b","status":503}` + "\n")
	f.Close()
	c.countAccessLog()
	if c.counters["api"].Requests != 2 || c.counters["api"].S5xx != 1 {
		t.Fatalf("after append: %+v", c.counters["api"])
	}
	// rotation: smaller file -> offset resets, counters keep accumulating
	os.WriteFile(path, []byte(`{"uri":"/x","status":200}`+"\n"), 0o644)
	c.countAccessLog()
	if c.counters["api"].Requests != 2 || c.counters["web"].Requests != 1 {
		t.Fatalf("after rotation: %+v", c.counters)
	}
}

func TestBucketFor(t *testing.T) {
	for uri, want := range map[string]string{"/api/v1/x": "api", "/metrics/y": "metrics", "/": "web", "/app.js": "web"} {
		if got := bucketFor(uri); got != want {
			t.Fatalf("%s -> %s, want %s", uri, got, want)
		}
	}
}

func TestLatestEmpty(t *testing.T) {
	c := newCollector(fakeDocker(t), filepath.Join(t.TempDir(), "none"), filepath.Join(t.TempDir(), "none2"), "", 10)
	rec := httptest.NewRecorder()
	c.latest(rec, httptest.NewRequest("GET", "/api/snapshot", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "no data") {
		t.Fatalf("code %d body %q", rec.Code, rec.Body.String())
	}
}
