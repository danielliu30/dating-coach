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
// /refresh is one of the rate limited public routes rather than an
// authenticated one: it is the endpoint a client reaches for once its access
// token has expired, so requiring a usable one would defeat it. The refresh
// token in the body is the credential.
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

// refresh handles POST /refresh, rotating the caller's refresh token into a
// fresh session. A rejected token answers 401, which clients treat as "sign in
// again" rather than as something to retry.
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
	case errors.Is(err, ErrInvalidRefreshToken):
		httpx.Error(w, http.StatusUnauthorized, err.Error())
	case err != nil:
		slog.Error("refresh session", "error", err)
		httpx.Error(w, http.StatusInternalServerError, "could not refresh session")
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
// certain once it returns, but the rows are removed by the worker afterwards and
// the caller's access token works until it expires, which is minutes away, and
// cannot be renewed. It is idempotent, so a caller whose deletion could not be
// queued can repeat the request with the same token.
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
	if err != nil {
		slog.Error("load profile", "error", err)
		httpx.Error(w, http.StatusInternalServerError, "could not load profile")
		return
	}
	httpx.JSON(w, http.StatusOK, profile)
}
