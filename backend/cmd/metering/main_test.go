package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func store(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeLines(t, filepath.Join(dir, "metrics-stats.jsonl"),
		`{"ts":"t1","started":"`+time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)+`","Name":"dating-coach_api.1.x","CPUPerc":"0.50%","MemUsage":"8MiB / 16GiB","NetIO":"85kB / 90kB","MemPerc":"0.05%","PIDs":"15"}`,
		`{"ts":"t2","started":"`+time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)+`","Name":"dating-coach_api.1.x","CPUPerc":"1.00%","MemUsage":"9MiB / 16GiB","NetIO":"86kB / 91kB","MemPerc":"0.06%","PIDs":"15"}`,
		`{"ts":"t2","started":"","Name":"dating-coach_web.1.y","CPUPerc":"0.10%","MemUsage":"1GiB / 16GiB","NetIO":"0B / 0B","MemPerc":"6.25%","PIDs":"3"}`,
	)
	writeLines(t, filepath.Join(dir, "metrics-services.jsonl"),
		`{"ts":"t1","Name":"dating-coach_api","Replicas":"0/1","Image":"img:old"}`,
		`{"ts":"t2","Name":"dating-coach_api","Replicas":"1/1","Image":"img:new"}`,
		`{"ts":"t2","Name":"dating-coach_web","Replicas":"1/1","Image":"img:web"}`,
	)
	writeLines(t, filepath.Join(dir, "metrics-requests.jsonl"),
		`{"ts":"t1","bucket":"api","requests":10,"s2xx":9,"s4xx":1,"s5xx":0}`,
		`{"ts":"t2","bucket":"api","requests":5,"s2xx":4,"s4xx":0,"s5xx":1}`,
		`{"ts":"t2","bucket":"web","requests":3,"s2xx":3,"s4xx":0,"s5xx":0}`,
	)
	writeLines(t, filepath.Join(dir, "deploy-history.jsonl"),
		`{"ts":"t0","sha":"a","result":"ok","seconds":9}`,
		`{"ts":"t1","sha":"b","result":"failed","seconds":4}`,
	)
	return dir
}

func TestCollect(t *testing.T) {
	r := newReader(store(t), 10)
	snap := r.collect()

	if len(snap.Services) != 2 || snap.Services[0].Running != 1 || snap.Services[0].Desired != 1 || snap.Services[0].Image != "img:new" {
		t.Fatalf("services: %+v", snap.Services)
	}
	if snap.Services[0].UptimeS < 3500 {
		t.Fatalf("api uptime from started: %d", snap.Services[0].UptimeS)
	}
	if len(snap.Tasks) != 2 {
		t.Fatalf("tasks: %+v", snap.Tasks)
	}
	api := snap.Tasks[0]
	if api.CPUPerc != 1.00 || api.MemBytes != 9<<20 || api.NetRx != 86e3 || api.Pids != 15 {
		t.Fatalf("api task: %+v", api)
	}
	if h := snap.CPUHist["dating-coach_api.1.x"]; len(h) != 2 || h[1] != 1.00 {
		t.Fatalf("cpu hist: %+v", h)
	}
	if snap.Requests["api"].Requests != 15 || snap.Requests["api"].S5xx != 1 || snap.Requests["web"].Requests != 3 {
		t.Fatalf("requests: %+v", snap.Requests)
	}
	if len(snap.Deploys) != 2 || !strings.Contains(string(snap.Deploys[0]), `"sha":"b"`) {
		t.Fatalf("deploys newest-first: %v", snap.Deploys)
	}
}

func TestParseHelpers(t *testing.T) {
	if parseBytes("8.5MiB") != uint64(8.5*(1<<20)) || parseBytes("85kB") != 85e3 || parseBytes("0B") != 0 {
		t.Fatal("parseBytes")
	}
	if parsePct("1.25%") != 1.25 {
		t.Fatal("parsePct")
	}
	rx, tx := parsePair("1KiB / 2KiB")
	if rx != 1024 || tx != 2048 {
		t.Fatal("parsePair")
	}
}

func TestEmptyStore(t *testing.T) {
	r := newReader(t.TempDir(), 10)
	snap := r.collect()
	if snap.Tasks != nil || len(snap.Services) != 0 || len(snap.Requests) != 0 {
		t.Fatalf("expected empty snapshot, got %+v", snap)
	}
	rec := httptest.NewRecorder()
	r.latest(rec, httptest.NewRequest("GET", "/api/snapshot", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("before first collect want 503, got %d", rec.Code)
	}
}
