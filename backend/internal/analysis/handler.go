package analysis

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/httpx"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes mounts the conversation-analysis endpoints (requires auth).
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/conversations", h.submit)
	r.Get("/conversations", h.listConversations)
	r.Get("/conversations/{conversationID}/result", h.latestResult)
	r.Post("/conversations/{conversationID}/label", h.label)
	r.Get("/results/{analysisID}", h.result)
	return r
}

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	var in SubmitInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	result, err := h.svc.Submit(r.Context(), principal.UserID, in)
	if err != nil {
		respondErr(w, err, "could not submit conversation")
		return
	}
	httpx.JSON(w, http.StatusAccepted, result)
}

func (h *Handler) listConversations(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	limit := httpx.QueryInt(r, "limit", defaultListLimit, 100)
	offset := httpx.QueryInt(r, "offset", 1, 100_000) - 1

	conversations, err := h.svc.ListConversations(r.Context(), principal.UserID, limit, offset)
	if err != nil {
		respondErr(w, err, "could not list conversations")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"conversations": conversations})
}

func (h *Handler) result(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	analysisID, ok := pathUUID(w, r, "analysisID")
	if !ok {
		return
	}
	result, err := h.svc.Get(r.Context(), analysisID, principal.UserID)
	if err != nil {
		respondErr(w, err, "could not load analysis")
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

func (h *Handler) latestResult(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	conversationID, ok := pathUUID(w, r, "conversationID")
	if !ok {
		return
	}
	result, err := h.svc.LatestForConversation(r.Context(), conversationID, principal.UserID)
	if err != nil {
		respondErr(w, err, "could not load analysis")
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

func (h *Handler) label(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	conversationID, ok := pathUUID(w, r, "conversationID")
	if !ok {
		return
	}
	var in LabelInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	example, err := h.svc.Label(r.Context(), conversationID, principal.UserID, in)
	if err != nil {
		respondErr(w, err, "could not save label")
		return
	}
	httpx.JSON(w, http.StatusCreated, example)
}

func principalOf(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthenticated")
		return auth.Principal{}, false
	}
	return principal, true
}

func pathUUID(w http.ResponseWriter, r *http.Request, param string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, param))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, param+" must be a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func respondErr(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, http.StatusForbidden, "not allowed")
	case errors.Is(err, ErrInvalidInput):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	default:
		slog.Error(fallback, "error", err)
		httpx.Error(w, http.StatusInternalServerError, fallback)
	}
}
