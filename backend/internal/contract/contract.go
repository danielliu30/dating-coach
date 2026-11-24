// Package contract loads api/openapi.yaml for the router coverage tests in
// cmd/api and cmd/worker. Only tests import it.
package contract

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// SpecOperations returns "METHOD /path" for every operation the OpenAPI spec
// documents on paths the include predicate accepts.
func SpecOperations(t *testing.T, include func(path string) bool) map[string]bool {
	t.Helper()
	// This file lives at backend/internal/contract; the spec is repo-root/api.
	_, here, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read api/openapi.yaml: %v", err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse api/openapi.yaml: %v", err)
	}
	ops := make(map[string]bool)
	for path, item := range spec.Paths {
		if !include(path) {
			continue
		}
		for method := range item {
			switch m := strings.ToUpper(method); m {
			case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				ops[m+" "+path] = true
			}
		}
	}
	return ops
}

// DiffOperations reports documented-but-not-served and served-but-undocumented
// operations, sorted for stable failure output.
func DiffOperations(documented, served map[string]bool) (missing, undocumented []string) {
	for op := range documented {
		if !served[op] {
			missing = append(missing, op)
		}
	}
	for op := range served {
		if !documented[op] {
			undocumented = append(undocumented, op)
		}
	}
	return missing, undocumented
}
