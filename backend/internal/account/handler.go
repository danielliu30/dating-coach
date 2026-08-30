package account

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/httpx"
)

// Handler is the HTTP layer for /api/v1/account.
type Handler struct {
	svc *Service
}

// NewHandler builds the account handler; cmd/api mounts its Routes behind the
// authentication middleware.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes mounts the account endpoints.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Delete("/", h.deleteAccount)
	return r
}

// deleteAccount handles DELETE /account: it deletes the caller's own account and
// answers 202, since the rows are removed by the worker after the response.
func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if err := h.svc.Delete(r.Context(), principal.UserID); err != nil {
		slog.Error("delete account", "error", err, "user_id", principal.UserID)
		httpx.Error(w, http.StatusInternalServerError, "could not delete account")
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "deletion_scheduled"})
}
