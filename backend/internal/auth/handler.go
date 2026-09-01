package auth

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/danielliu30/dating-coach/backend/internal/httpx"
)

// Handler is the HTTP layer for /api/v1/auth: it decodes requests, delegates to
// Service and translates its sentinel errors into status codes.
type Handler struct {
	svc     *Service
	limiter *RateLimiter
}

// NewHandler builds the auth handler; cmd/api mounts its Routes.
func NewHandler(svc *Service, limiter *RateLimiter) *Handler {
	return &Handler{svc: svc, limiter: limiter}
}

// Routes mounts the auth endpoints. The credential endpoints are rate limited
// per IP; /me sits behind the authentication middleware.
//
// /refresh is public because it authenticates with the refresh token in its
// body rather than a bearer token: a client whose access token has already
// expired must still be able to obtain the next one.
func (h *Handler) Routes(authenticate func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Group(func(public chi.Router) {
		public.Use(h.limiter.Middleware)
		public.Post("/signup", h.signUp)
		public.Post("/signin", h.signIn)
		public.Post("/refresh", h.refresh)
		public.Post("/verify", h.verify)
		public.Post("/resend-verification", h.resendVerification)
	})
	r.Group(func(private chi.Router) {
		private.Use(authenticate)
		private.Get("/me", h.me)
		private.Delete("/me", h.deleteMe)
	})
	return r
}

// refresh handles POST /refresh and trades a refresh token for a fresh access
// token plus its replacement. A spent, revoked or expired token answers 401, so
// clients treat it the same as an expired session and sign in again.
func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	session, err := h.svc.Refresh(r.Context(), in.RefreshToken)
	switch {
	case errors.Is(err, ErrInvalidToken), errors.Is(err, ErrEmailNotVerified):
		httpx.Error(w, http.StatusUnauthorized, "invalid refresh token")
	case err != nil:
		slog.Error("refresh session", "error", err)
		httpx.Error(w, http.StatusInternalServerError, "could not refresh session")
	default:
		httpx.JSON(w, http.StatusOK, session)
	}
}

// signUp handles POST /signup and returns a session for the new account.
func (h *Handler) signUp(w http.ResponseWriter, r *http.Request) {
	var in SignUpInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	session, err := h.svc.SignUp(r.Context(), in)
	switch {
	case errors.Is(err, ErrInvalidInput):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrEmailTaken):
		httpx.Error(w, http.StatusConflict, err.Error())
	case err != nil:
		slog.Error("sign up", "error", err)
		httpx.Error(w, http.StatusInternalServerError, "could not create account")
	default:
		httpx.JSON(w, http.StatusCreated, session)
	}
}

// signIn handles POST /signin and exchanges credentials for a session. Correct
// credentials on an unverified account answer 403, which the clients use to
// route the user to the verification screen.
func (h *Handler) signIn(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	session, err := h.svc.SignIn(r.Context(), in.Email, in.Password)
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		httpx.Error(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, ErrEmailNotVerified):
		httpx.Error(w, http.StatusForbidden, err.Error())
	case err != nil:
		slog.Error("sign in", "error", err)
		httpx.Error(w, http.StatusInternalServerError, "could not sign in")
	default:
		httpx.JSON(w, http.StatusOK, session)
	}
}

// verify handles POST /verify, consuming the token from the verification
// email. The response carries a session token when the caller is authenticated
// as the account being verified, so the app does not have to sign in again.
func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	session, err := h.svc.VerifyEmail(r.Context(), in.Token, bearerToken(r))
	switch {
	case errors.Is(err, ErrInvalidToken):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case err != nil:
		slog.Error("verify email", "error", err)
		httpx.Error(w, http.StatusInternalServerError, "could not verify email")
	default:
		httpx.JSON(w, http.StatusOK, session)
	}
}

// resendVerification handles POST /resend-verification. Unknown and already
// verified addresses also report success, so the response cannot be used to
// probe for registered emails.
func (h *Handler) resendVerification(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.svc.ResendVerification(r.Context(), in.Email); err != nil {
		slog.Error("resend verification", "error", err)
		httpx.Error(w, http.StatusInternalServerError, "could not resend verification email")
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "sent"})
}

// deleteMe handles DELETE /me. It answers 202 rather than 204: the deletion is
// certain once it returns, but the rows are removed by the worker afterwards. It
// is idempotent, so a caller whose deletion could not be queued can repeat the
// request with the access token it still holds.
func (h *Handler) deleteMe(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if err := h.svc.DeleteAccount(r.Context(), principal.UserID); err != nil {
		slog.Error("delete account", "error", err, "user_id", principal.UserID)
		httpx.Error(w, http.StatusInternalServerError, "could not delete account")
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "deleted"})
}

// me handles GET /me and returns the profile of the authenticated caller.
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	profile, err := h.svc.Profile(r.Context(), principal)
	if errors.Is(err, ErrInvalidToken) {
		httpx.Error(w, http.StatusUnauthorized, "session is no longer valid")
		return
	}
	if err != nil {
		slog.Error("load profile", "error", err)
		httpx.Error(w, http.StatusInternalServerError, "could not load profile")
		return
	}
	httpx.JSON(w, http.StatusOK, profile)
}
