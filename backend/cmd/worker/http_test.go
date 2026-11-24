package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/danielliu30/dating-coach/backend/internal/analysis"
	"github.com/danielliu30/dating-coach/backend/internal/httpx"
)

func TestPushHandler(t *testing.T) {
	for _, tt := range []struct {
		name, body         string
		fail, called, last bool
		status             int
	}{
		{"success", `{"message":{"data":"e30="},"deliveryAttempt":1}`, false, true, false, 204},
		{"missing attempt", `{"message":{"data":"e30="}}`, false, true, false, 204},
		{"final", `{"message":{"data":"e30="},"deliveryAttempt":5}`, false, true, true, 204},
		{"beyond final", `{"message":{"data":"e30="},"deliveryAttempt":6}`, false, true, true, 204},
		{"retry", `{"message":{"data":"e30="}}`, true, true, false, 500},
		{"bad envelope", `{`, false, false, false, 204},
		{"bad base64", `{"message":{"data":"!"}}`, false, false, false, 204},
		{"bad job", `{"message":{"data":"ew=="}}`, false, false, false, 204},
		{"empty", `{}`, false, false, false, 204},
		{"trailing", `{"message":{"data":"e30="}} {}`, false, false, false, 204},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			h := pushHandler(func(ctx context.Context, j analysis.Job, last bool) error {
				called = true
				if last != tt.last {
					t.Errorf("last=%v", last)
				}
				if tt.fail {
					return errors.New("failure")
				}
				return nil
			}, 5, false)
			w := httptest.NewRecorder()
			h(w, httptest.NewRequest("POST", "/", strings.NewReader(tt.body)))
			if w.Code != tt.status || called != tt.called {
				t.Fatalf("status=%d called=%v", w.Code, called)
			}
		})
	}
}

func TestWorkerTasks(t *testing.T) {
	var calls []string
	tasks := map[string]task{}
	for _, name := range []string{"a", "b", "relay-emails"} {
		tasks[name] = func(ctx context.Context) (any, error) { calls = append(calls, name); return 1, nil }
	}
	tasks["fail"] = func(context.Context) (any, error) { return nil, errors.New("failed") }
	router := workerRouter(nil, nil, 5, tasks, httpx.NewStats(nil))
	for _, tt := range []struct {
		method, path string
		status       int
		want         []string
	}{
		{"GET", "/healthz", 200, nil},
		{"POST", "/tasks/a", 200, []string{"a"}},
		{"POST", "/tasks/batch?task=a&task=b", 200, []string{"a", "b"}},
		{"POST", "/tasks/batch", 400, nil},
		{"POST", "/tasks/unknown", 404, nil},
		{"POST", "/tasks/sweep-payments", 404, nil},
		{"POST", "/tasks/batch?task=a&task=unknown", 404, nil},
		{"POST", "/tasks/batch?task=a&task=fail&task=b", 500, []string{"a", "b"}},
		{"GET", "/tasks/a", 405, nil},
		{"POST", "/pubsub/email-outbox", 204, []string{"relay-emails"}},
	} {
		t.Run(tt.path, func(t *testing.T) {
			calls = nil
			w := httptest.NewRecorder()
			body := fmt.Sprintf(`{"message":{"data":%q}}`, base64.StdEncoding.EncodeToString([]byte(`{}`)))
			router.ServeHTTP(w, httptest.NewRequest(tt.method, tt.path, strings.NewReader(body)))
			if w.Code != tt.status || !reflect.DeepEqual(calls, tt.want) {
				t.Fatalf("status=%d calls=%v", w.Code, calls)
			}
		})
	}
	tasks["relay-emails"] = tasks["fail"]
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/pubsub/email-outbox", strings.NewReader(`{"message":{"data":"e30="}}`)))
	if w.Code != 500 {
		t.Fatalf("email failure status=%d", w.Code)
	}
}

func TestWorkerBatchReportsEveryTask(t *testing.T) {
	for _, tt := range []struct {
		name   string
		names  []string
		status int
		want   []map[string]any
	}{
		{
			name: "success", names: []string{"a", "b"}, status: 200,
			want: []map[string]any{{"task": "a", "result": float64(1)}, {"task": "b", "result": float64(1)}},
		},
		{
			name: "continues after failures", names: []string{"fail", "a", "fail-empty", "b"}, status: 500,
			want: []map[string]any{
				{"task": "fail", "result": float64(2), "error": "partial failure"},
				{"task": "a", "result": float64(1)},
				{"task": "fail-empty", "result": nil, "error": "failed"},
				{"task": "b", "result": float64(1)},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			tasks := map[string]task{}
			for _, name := range []string{"a", "b", "fail", "fail-empty"} {
				tasks[name] = func(context.Context) (any, error) {
					calls = append(calls, name)
					switch name {
					case "fail":
						return 2, errors.New("partial failure")
					case "fail-empty":
						return nil, errors.New("failed")
					default:
						return 1, nil
					}
				}
			}
			w := httptest.NewRecorder()
			path := "/tasks/batch?task=" + strings.Join(tt.names, "&task=")
			workerRouter(nil, nil, 5, tasks, httpx.NewStats(nil)).ServeHTTP(w, httptest.NewRequest("POST", path, nil))
			if w.Code != tt.status || !reflect.DeepEqual(calls, tt.names) {
				t.Fatalf("status=%d calls=%v", w.Code, calls)
			}
			if got := w.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("content type=%q", got)
			}
			var got []map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("results=%v, want %v", got, tt.want)
			}
		})
	}
}
