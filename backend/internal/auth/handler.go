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
func (h *Handler) Routes(authenticate func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Group(func(public chi.Router) {
		public.Use(h.limiter.Middleware)
		public.Post("/signup", h.signUp)
		public.Post("/signin", h.signIn)
		public.Post("/verify", h.verify)
		public.Post("/resend-verification", h.resendVerification)
	})
	r.Group(func(private chi.Router) {
		private.Use(authenticate)
		private.Get("/me", h.me)
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

// signIn handles POST /signin and exchanges credentials for a session.
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
