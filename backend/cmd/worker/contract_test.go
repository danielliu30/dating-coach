package main

import (
	"sort"
	"strings"
	"testing"

	"github.com/danielliu30/dating-coach/backend/internal/contract"
)

// The worker's route table must match the internal surface documented in
// api/openapi.yaml — /pubsub/* and /tasks/*. Both directions, same as the api.
func TestWorkerRouterMatchesOpenAPI(t *testing.T) {
	served := map[string]bool{}
	for _, route := range workerRoutes(nil, nil, 0, nil) {
		method, path, _ := strings.Cut(route.pattern, " ")
		served[method+" "+path] = true
	}

	// /healthz is a shared endpoint both services implement.
	documented := contract.SpecOperations(t, func(p string) bool {
		return strings.HasPrefix(p, "/pubsub") || strings.HasPrefix(p, "/tasks") ||
			p == "/healthz"
	})

	missing, undocumented := contract.DiffOperations(documented, served)
	sort.Strings(missing)
	sort.Strings(undocumented)
	for _, op := range missing {
		t.Errorf("documented in api/openapi.yaml but not routed: %s", op)
	}
	for _, op := range undocumented {
		t.Errorf("routed but missing from api/openapi.yaml: %s", op)
	}
}
