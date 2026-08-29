package coaching

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

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

// Routes mounts the client-facing coaching endpoints (requires auth).
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/coaches", h.listCoaches)
	r.Get("/coaches/{coachID}", h.getCoach)
	r.Get("/coaches/{coachID}/availability", h.coachAvailability)
	r.Get("/coaches/{coachID}/slots", h.coachSlots)
	r.Post("/sessions", h.bookSession)
	r.Get("/sessions", h.listMySessions)
	r.Post("/sessions/{sessionID}/cancel", h.cancelSession)
	r.Post("/sessions/{sessionID}/reschedule", h.rescheduleSession)
	return r
}

// CoachRoutes mounts the coach-side dashboard endpoints (requires coach role).
func (h *Handler) CoachRoutes() http.Handler {
	r := chi.NewRouter()
	r.Put("/profile", h.upsertProfile)
	r.Put("/availability", h.setAvailability)
	r.Get("/sessions", h.listCoachSessions)
	r.Post("/sessions/{sessionID}/status", h.setSessionStatus)
	r.Post("/sessions/{sessionID}/notes", h.setSessionNotes)
	return r
}

func (h *Handler) listCoaches(w http.ResponseWriter, r *http.Request) {
	limit := httpx.QueryInt(r, "limit", 25, 100)
	offset := httpx.QueryInt(r, "offset", 1, 10_000) - 1
	acceptingOnly := r.URL.Query().Get("accepting_only") != "false"

	coaches, err := h.svc.ListCoaches(r.Context(), limit, offset, acceptingOnly)
	if err != nil {
		slog.Error("list coaches", "error", err)
		httpx.Error(w, http.StatusInternalServerError, "could not list coaches")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"coaches": coaches})
}

func (h *Handler) getCoach(w http.ResponseWriter, r *http.Request) {
	coachID, ok := pathUUID(w, r, "coachID")
	if !ok {
		return
	}
	coach, err := h.svc.GetCoach(r.Context(), coachID)
	if err != nil {
		respondErr(w, err, "could not load coach")
		return
	}
	httpx.JSON(w, http.StatusOK, coach)
}

func (h *Handler) coachAvailability(w http.ResponseWriter, r *http.Request) {
	coachID, ok := pathUUID(w, r, "coachID")
	if !ok {
		return
	}
	windows, err := h.svc.ListAvailability(r.Context(), coachID)
	if err != nil {
		respondErr(w, err, "could not load availability")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"availability": windows})
}

func (h *Handler) coachSlots(w http.ResponseWriter, r *http.Request) {
	coachID, ok := pathUUID(w, r, "coachID")
	if !ok {
		return
	}
	from := time.Now()
	if raw := r.URL.Query().Get("from"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "from must be RFC3339")
			return
		}
		from = parsed
	}
	to := from.Add(7 * 24 * time.Hour)
	if raw := r.URL.Query().Get("to"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "to must be RFC3339")
			return
		}
		to = parsed
	}
	duration := httpx.QueryInt(r, "duration_minutes", defaultSessionMinutes, 240)

	slots, err := h.svc.OpenSlots(r.Context(), coachID, from, to, duration)
	if err != nil {
		respondErr(w, err, "could not compute slots")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"slots": slots})
}

func (h *Handler) bookSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var in BookInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	session, err := h.svc.BookSession(r.Context(), principal.UserID, in)
	if err != nil {
		respondErr(w, err, "could not book session")
		return
	}
	httpx.JSON(w, http.StatusCreated, session)
}

func (h *Handler) listMySessions(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	sessions, err := h.svc.ListForUser(r.Context(), principal.UserID, optionalQuery(r, "status"))
	if err != nil {
		respondErr(w, err, "could not list sessions")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (h *Handler) cancelSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	sessionID, ok := pathUUID(w, r, "sessionID")
	if !ok {
		return
	}
	session, err := h.svc.SetStatus(r.Context(), sessionID, principal.UserID, "cancelled")
	if err != nil {
		respondErr(w, err, "could not cancel session")
		return
	}
	httpx.JSON(w, http.StatusOK, session)
}

func (h *Handler) rescheduleSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	sessionID, ok := pathUUID(w, r, "sessionID")
	if !ok {
		return
	}
	var in struct {
		ScheduledTime string `json:"scheduled_time"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	session, err := h.svc.Reschedule(r.Context(), sessionID, principal.UserID, in.ScheduledTime)
	if err != nil {
		respondErr(w, err, "could not reschedule session")
		return
	}
	httpx.JSON(w, http.StatusOK, session)
}

func (h *Handler) upsertProfile(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var in UpsertProfileInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	coach, err := h.svc.UpsertProfile(r.Context(), principal.UserID, in)
	if err != nil {
		respondErr(w, err, "could not save profile")
		return
	}
	httpx.JSON(w, http.StatusOK, coach)
}

func (h *Handler) setAvailability(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var in struct {
		Availability []AvailabilityWindow `json:"availability"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	windows, err := h.svc.SetAvailability(r.Context(), principal.UserID, in.Availability)
	if err != nil {
		respondErr(w, err, "could not save availability")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"availability": windows})
}

func (h *Handler) listCoachSessions(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var fromTime *time.Time
	if raw := r.URL.Query().Get("from"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "from must be RFC3339")
			return
		}
		fromTime = &parsed
	}
	sessions, err := h.svc.ListForCoach(r.Context(), principal.UserID, optionalQuery(r, "status"), fromTime)
	if err != nil {
		respondErr(w, err, "could not list sessions")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (h *Handler) setSessionStatus(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	sessionID, ok := pathUUID(w, r, "sessionID")
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	session, err := h.svc.SetStatus(r.Context(), sessionID, principal.UserID, in.Status)
	if err != nil {
		respondErr(w, err, "could not update session")
		return
	}
	httpx.JSON(w, http.StatusOK, session)
}

func (h *Handler) setSessionNotes(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	sessionID, ok := pathUUID(w, r, "sessionID")
	if !ok {
		return
	}
	var in struct {
		CoachNotes string `json:"coach_notes"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	session, err := h.svc.SetNotes(r.Context(), sessionID, principal.UserID, in.CoachNotes)
	if err != nil {
		respondErr(w, err, "could not update notes")
		return
	}
	httpx.JSON(w, http.StatusOK, session)
}

func pathUUID(w http.ResponseWriter, r *http.Request, param string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, param))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, param+" must be a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func optionalQuery(r *http.Request, key string) *string {
	if v := r.URL.Query().Get(key); v != "" {
		return &v
	}
	return nil
}

func respondErr(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, http.StatusForbidden, "not allowed")
	case errors.Is(err, ErrInvalidInput):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrSlotTaken), errors.Is(err, ErrUnavailable):
		httpx.Error(w, http.StatusConflict, err.Error())
	default:
		slog.Error(fallback, "error", err)
		httpx.Error(w, http.StatusInternalServerError, fallback)
	}
}
