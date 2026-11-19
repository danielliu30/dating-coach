package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/danielliu30/dating-coach/backend/internal/analysis"
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
	router := workerRouter(nil, nil, 5, tasks)
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
		{"POST", "/tasks/batch?task=a&task=fail&task=b", 500, []string{"a"}},
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
