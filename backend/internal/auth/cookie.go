package auth

import "net/http"

// refreshCookieName carries the refresh token to browsers as an httpOnly
// cookie, so the web client can keep it out of JS-readable storage. The path
// scopes it to the auth routes; SameSite=Strict plus the path scope means it
// is only ever sent to /refresh and /signout (both POST /api/v1/auth/*) and
// never cross-site. Max-Age is omitted deliberately: the cookie is a session
// cookie, the token's own expiry is enforced server-side, and a cookie that
// never touches disk shrinks the theft surface further. Secure still applies
// on http://localhost (a secure context); native clients ignore it and keep
// using the JSON body.
const (
	refreshCookieName = "refresh_token"
	refreshCookiePath = "/api/v1/auth"
)

// setRefreshCookie attaches the session's refresh token as an httpOnly cookie
// when the response opens a full session. Responses without a refresh token
// (sign-up, verify of an unauthenticated caller) leave the cookie alone.
func setRefreshCookie(w http.ResponseWriter, session Session) {
	if session.RefreshToken == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    session.RefreshToken,
		Path:     refreshCookiePath,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

// clearRefreshCookie expires the cookie on sign-out and account deletion.
func clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Path:     refreshCookiePath,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// refreshTokenFrom falls back to the httpOnly cookie when the JSON body did
// not carry a refresh token (web clients no longer keep it in storage).
func refreshTokenFrom(r *http.Request, body string) string {
	if body != "" {
		return body
	}
	if c, err := r.Cookie(refreshCookieName); err == nil {
		return c.Value
	}
	return ""
}
