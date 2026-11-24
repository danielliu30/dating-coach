package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/analysis"
	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/chat"
	"github.com/danielliu30/dating-coach/backend/internal/coaching"
	"github.com/danielliu30/dating-coach/backend/internal/config"
	"github.com/danielliu30/dating-coach/backend/internal/httpx"
	"github.com/danielliu30/dating-coach/backend/internal/payments"
)

func TestRouterSecurityHeaders(t *testing.T) {
	router := testRouter(t, auth.NewTokenIssuer("test-secret"))
	for _, tc := range []struct {
		method, path string
		status       int
		cache        string
	}{
		{http.MethodGet, "/healthz", http.StatusOK, ""},
		{http.MethodGet, "/api/v1/auth/me", http.StatusUnauthorized, "no-store"},
		{http.MethodGet, "/api/v1/auth/missing", http.StatusNotFound, "no-store"},
		{http.MethodOptions, "/api/v1/auth/login", http.StatusOK, "no-store"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.method == http.MethodOptions {
			req.Header.Set("Origin", "https://example.com")
			req.Header.Set("Access-Control-Request-Method", http.MethodPost)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != tc.status || rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != tc.cache {
			t.Fatalf("%s %s: status=%d headers=%v", tc.method, tc.path, rec.Code, rec.Header())
		}
	}
}

func TestRouterBodyLimits(t *testing.T) {
	issuer := auth.NewTokenIssuer("test-secret")
	router := testRouter(t, issuer)
	token, _, err := issuer.Issue(uuid.New(), "user@example.com", auth.RoleUser, auth.ScopeSession, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		size int
		want int
	}{
		{"/api/v1/booking/respond", httpx.MaxJSONBodyBytes, http.StatusBadRequest},
		{"/api/v1/booking/respond", httpx.MaxJSONBodyBytes + 1, http.StatusRequestEntityTooLarge},
		{"/api/v1/analysis/conversations", 2 << 20, http.StatusBadRequest},
		{"/api/v1/analysis/conversations", httpx.MaxTranscriptBodyBytes + 1, http.StatusRequestEntityTooLarge},
	} {
		field := "token"
		if tc.path == "/api/v1/analysis/conversations" {
			field = "unknown"
		}
		prefix := `{"` + field + `":"`
		body := prefix + strings.Repeat("a", tc.size-len(prefix)-2) + `"}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body))
		req.Header.Set("Origin", "https://example.com")
		req.Header.Set("Authorization", "Bearer "+token)
		req.ContentLength = -1
		router.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("path=%s size=%d status=%d want=%d", tc.path, tc.size, rec.Code, tc.want)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") == "" {
			t.Fatal("body-limit response missing CORS headers")
		}
	}
}

type unreadBody struct{ reads int }

func (b *unreadBody) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }
func (*unreadBody) Close() error               { return nil }

func TestPrivateRequestsAuthenticateBeforeReadingBodies(t *testing.T) {
	issuer := auth.NewTokenIssuer("test-secret")
	router := testRouter(t, issuer)
	verifyToken, _, err := issuer.Issue(uuid.New(), "user@example.com", auth.RoleUser, auth.ScopeVerify, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/analysis/conversations", "/api/v1/coaching/sessions", "/api/v1/auth/me"} {
		for _, token := range []string{"", verifyToken} {
			if path == "/api/v1/auth/me" && token != "" {
				continue
			}
			body := &unreadBody{}
			req := httptest.NewRequest(http.MethodPatch, path, nil)
			if path != "/api/v1/auth/me" {
				req.Method = http.MethodPost
			}
			req.Body = body
			req.ContentLength = httpx.MaxTranscriptBodyBytes + 1
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			want := http.StatusUnauthorized
			if token != "" {
				want = http.StatusForbidden
			}
			if rec.Code != want || body.reads != 0 {
				t.Fatalf("%s status=%d want=%d body reads=%d", path, rec.Code, want, body.reads)
			}
		}
	}
}

func TestRouterDoesNotTrustForwardedHeadersByDefault(t *testing.T) {
	router := testRouter(t, auth.NewTokenIssuer("test-secret"))
	router.(chi.Router).Get("/client-ip", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(auth.ClientIP(r)))
	})
	req := httptest.NewRequest(http.MethodGet, "/client-ip", nil)
	req.RemoteAddr = "198.51.100.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.1")
	req.Header.Set("X-Real-IP", "203.0.113.2")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "198.51.100.1" {
		t.Fatalf("status=%d client IP=%s", rec.Code, rec.Body)
	}
}

// testRouter builds the real routing tree over handlers with no backing
// services: every case below is decided by the auth middleware, so a request
// that reaches a handler fails loudly instead of returning a plausible status.
func testRouter(t *testing.T, issuer *auth.TokenIssuer) http.Handler {
	t.Helper()

	hub := chat.NewHub(nil)
	return newRouter(
		&config.Config{Env: "test", CORSOrigins: []string{"*"}},
		auth.Middleware(issuer, nil, nil),
		httpx.NewStats(hub.SocketCount),
		handlers{
			auth:     auth.NewHandler(auth.NewService(nil, nil, issuer, nil, 4, "", nil, nil, nil, nil, 15*time.Minute, 30*time.Minute, time.Hour), auth.NewRateLimiter(nil, 100, time.Minute)),
			coaching: coaching.NewHandler(coaching.NewService(nil, nil, payments.Disabled{}, 15*time.Minute, "", "", nil), nil),
			chat:     chat.NewHandler(chat.NewService(nil, hub), hub, nil, []string{"*"}),
			analysis: analysis.NewHandler(analysis.NewService(nil, nil, nil), false),
		},
	)
}

// TestPrivateRoutesRejectVerifyScope pins the wiring in newRouter: the token
// minted by sign-up must reach the auth endpoints it needs to verify an address
// and nothing else, whatever feature is mounted under the private group.
func TestPrivateRoutesRejectVerifyScope(t *testing.T) {
	issuer := auth.NewTokenIssuer("test-secret")
	router := testRouter(t, issuer)

	verifyToken, _, err := issuer.Issue(uuid.New(), "user@example.com", auth.RoleCoach, auth.ScopeVerify, time.Hour)
	if err != nil {
		t.Fatalf("issue verify token: %v", err)
	}
	sessionToken, _, err := issuer.Issue(uuid.New(), "user@example.com", auth.RoleCoach, auth.ScopeSession, time.Hour)
	if err != nil {
		t.Fatalf("issue session token: %v", err)
	}

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/coaching/coaches"},
		{http.MethodGet, "/api/v1/coaching/sessions"},
		{http.MethodPost, "/api/v1/coaching/sessions"},
		{http.MethodGet, "/api/v1/chat/threads"},
		{http.MethodPost, "/api/v1/chat/threads"},
		{http.MethodGet, "/api/v1/chat/coach/threads"},
		{http.MethodGet, "/api/v1/analysis/conversations"},
		{http.MethodGet, "/api/v1/analysis/profile-score"},
		{http.MethodPost, "/api/v1/analysis/conversations"},
		// An unparsable id keeps the request in the handler's own validation,
		// so the unbacked service is never called.
		{http.MethodPost, "/api/v1/analysis/conversations/not-a-uuid/reanalyze"},
		{http.MethodGet, "/api/v1/coach/sessions"},
		{http.MethodPut, "/api/v1/coach/profile"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			if got := status(router, tc.method, tc.path, verifyToken); got != http.StatusForbidden {
				t.Fatalf("verify-scoped token: status = %d, want %d", got, http.StatusForbidden)
			}
			// The same request with a session token gets past the middleware and
			// into the (unbacked) handler, so only the scope check can 403 here.
			if got := status(router, tc.method, tc.path, sessionToken); got == http.StatusForbidden || got == http.StatusUnauthorized {
				t.Fatalf("session-scoped token: status = %d, want it past authentication", got)
			}
		})
	}

	t.Run("GET /api/v1/auth/me stays reachable", func(t *testing.T) {
		if got := status(router, http.MethodGet, "/api/v1/auth/me", verifyToken); got == http.StatusForbidden {
			t.Fatal("verify-scoped token cannot reach /auth/me, so it cannot drive the verify screen")
		}
	})

	t.Run("no token is unauthorized", func(t *testing.T) {
		if got := status(router, http.MethodGet, "/api/v1/coaching/coaches", ""); got != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", got, http.StatusUnauthorized)
		}
	})
}

// TestAdminRoutesRequireAdminRole pins the /admin mount: only an admin session
// token gets past the middleware; users and coaches are refused with 403 and
// unauthenticated callers with 401.
func TestAdminRoutesRequireAdminRole(t *testing.T) {
	issuer := auth.NewTokenIssuer("test-secret")
	router := testRouter(t, issuer)
	tokenFor := func(role string) string {
		tok, _, err := issuer.Issue(uuid.New(), role+"@example.com", role, auth.ScopeSession, time.Hour)
		if err != nil {
			t.Fatalf("issue %s token: %v", role, err)
		}
		return tok
	}
	// A malformed id keeps the admin request inside pathUUID, so the unbacked
	// service is never reached and the admin case answers 400, not a panic.
	const path = "/api/v1/admin/coaches/not-a-uuid/approve"
	for role, want := range map[string]int{
		auth.RoleUser:  http.StatusForbidden,
		auth.RoleCoach: http.StatusForbidden,
		auth.RoleAdmin: http.StatusBadRequest,
	} {
		if got := status(router, http.MethodPost, path, tokenFor(role)); got != want {
			t.Errorf("%s POST %s = %d, want %d", role, path, got, want)
		}
	}
	if got := status(router, http.MethodGet, "/api/v1/admin/coaches", tokenFor(auth.RoleCoach)); got != http.StatusForbidden {
		t.Errorf("coach GET /admin/coaches = %d, want 403", got)
	}
	if got := status(router, http.MethodGet, "/api/v1/admin/coaches", ""); got != http.StatusUnauthorized {
		t.Errorf("anonymous GET /admin/coaches = %d, want 401", got)
	}
}

// status serves one request against router and returns its status code, sending
// the bearer token when it is not empty.
func status(router http.Handler, method, path, token string) int {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec.Code
}
