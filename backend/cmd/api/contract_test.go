package main

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/contract"
)

// The api router must serve exactly the edge operations documented in
// api/openapi.yaml — /metrics/* is nginx's, /pubsub/* and /tasks/* are the
// worker's. The check runs both directions so neither side can drift.
func TestAPIRouterMatchesOpenAPI(t *testing.T) {
	served := map[string]bool{}
	routes, ok := testRouter(t, auth.NewTokenIssuer("test-secret")).(chi.Routes)
	if !ok {
		t.Fatal("test router is not a chi router")
	}
	err := chi.Walk(routes,
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			// chi renders Mount("/") under Route("/x") as /x/*/...; the URL it
			// actually serves is /x/...
			served[method+" "+strings.ReplaceAll(route, "/*/", "/")] = true
			return nil
		})
	if err != nil {
		t.Fatalf("walk router: %v", err)
	}

	documented := contract.SpecOperations(t, func(p string) bool {
		return !strings.HasPrefix(p, "/metrics") &&
			!strings.HasPrefix(p, "/pubsub") &&
			!strings.HasPrefix(p, "/tasks")
	})
	// Config-gated: mounted only when PAYMENTS_ENABLED is on; the test router
	// runs with payments off.
	delete(documented, "POST /api/v1/payments/webhook")

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
