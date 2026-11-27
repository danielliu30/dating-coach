package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetRefreshCookie(t *testing.T) {
	t.Run("sets hardened session cookie", func(t *testing.T) {
		rec := httptest.NewRecorder()
		setRefreshCookie(rec, Session{RefreshToken: "tok"})
		res := rec.Result()
		cookies := res.Cookies()
		if len(cookies) != 1 {
			t.Fatalf("Set-Cookie count = %d, want 1", len(cookies))
		}
		c := cookies[0]
		if c.Name != refreshCookieName || c.Value != "tok" {
			t.Fatalf("cookie = %s=%s, want %s=tok", c.Name, c.Value, refreshCookieName)
		}
		if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != refreshCookiePath {
			t.Fatalf("cookie attrs = %+v, want HttpOnly Secure SameSite=Strict Path=%s", c, refreshCookiePath)
		}
		if c.MaxAge != 0 {
			t.Fatalf("MaxAge = %d, want session cookie (0)", c.MaxAge)
		}
	})
	t.Run("skips sessions without a refresh token", func(t *testing.T) {
		rec := httptest.NewRecorder()
		setRefreshCookie(rec, Session{Token: "access-only"})
		if got := rec.Result().Cookies(); len(got) != 0 {
			t.Fatalf("Set-Cookie count = %d, want 0", len(got))
		}
	})
}

func TestRefreshTokenFrom(t *testing.T) {
	t.Run("body wins over cookie", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/refresh", nil)
		r.AddCookie(&http.Cookie{Name: refreshCookieName, Value: "from-cookie"})
		if got := refreshTokenFrom(r, "from-body"); got != "from-body" {
			t.Fatalf("got %q, want body token", got)
		}
	})
	t.Run("falls back to the cookie", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/refresh", nil)
		r.AddCookie(&http.Cookie{Name: refreshCookieName, Value: "from-cookie"})
		if got := refreshTokenFrom(r, ""); got != "from-cookie" {
			t.Fatalf("got %q, want cookie token", got)
		}
	})
	t.Run("empty without either", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/refresh", nil)
		if got := refreshTokenFrom(r, ""); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
}

func TestRefreshEndpointCookieTransport(t *testing.T) {
	svc, _, _ := newTestService(t)
	routes := NewHandler(svc, failOpenLimiter(t)).Routes(rejectAll)

	t.Run("missing token answers 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/refresh", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("forged cookie answers 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/refresh", nil)
		req.AddCookie(&http.Cookie{Name: refreshCookieName, Value: "forged"})
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body %s", rec.Code, rec.Body.String())
		}
	})
}

func TestSignOutClearsRefreshCookie(t *testing.T) {
	rec := httptest.NewRecorder()
	clearRefreshCookie(rec)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != refreshCookieName || cookies[0].MaxAge >= 0 {
		t.Fatalf("cookies = %+v, want one expired %s cookie", cookies, refreshCookieName)
	}
}
