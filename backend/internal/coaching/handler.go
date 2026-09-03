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

// Handler is the HTTP layer for the coaching features. It serves two route
// groups: the client-facing endpoints from Routes and the coach-only dashboard
// endpoints from CoachRoutes.
type Handler struct {
	svc *Service
}

// NewHandler builds the coaching handler; cmd/api mounts both route groups.
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
	r.Post("/sessions/{sessionID}/respond", h.respondAsCoach)
	r.Post("/sessions/{sessionID}/status", h.setSessionStatus)
	r.Post("/sessions/{sessionID}/notes", h.setSessionNotes)
	return r
}

// PublicRoutes mounts the endpoints reached from links in emails, which carry
// their own single-use token instead of a bearer token.
func (h *Handler) PublicRoutes() http.Handler {
	r := chi.NewRouter()
	r.Post("/respond", h.respondWithToken)
	return r
}

// respondWithToken handles POST /booking/respond, the target of the confirm and
// decline links in the coach's email: {session_id, token, action}.
func (h *Handler) respondWithToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SessionID string `json:"session_id"`
		Token     string `json:"token"`
		Action    string `json:"action"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	sessionID, err := uuid.Parse(in.SessionID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "session_id must be a uuid")
		return
	}
	session, err := h.svc.RespondWithToken(r.Context(), sessionID, in.Token, in.Action)
	if err != nil {
		respondErr(w, err, "could not respond to request")
		return
	}
	httpx.JSON(w, http.StatusOK, session)
}

// respondAsCoach handles POST /coach/sessions/{sessionID}/respond, the
// dashboard's confirm and decline buttons: {action}.
func (h *Handler) respondAsCoach(w http.ResponseWriter, r *http.Request) {
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
		Action string `json:"action"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	session, err := h.svc.RespondAsCoach(r.Context(), sessionID, principal.UserID, in.Action)
	if err != nil {
		respondErr(w, err, "could not respond to request")
		return
	}
	httpx.JSON(w, http.StatusOK, session)
}

// listCoaches handles GET /coaches, hiding coaches that are not accepting
// clients unless accepting_only=false.
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

// getCoach handles GET /coaches/{coachID}.
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

// coachAvailability handles GET /coaches/{coachID}/availability, returning the
// recurring weekly windows rather than concrete times.
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

// coachSlots handles GET /coaches/{coachID}/slots, defaulting to the next seven
// days. exclude_session_id lets a reschedule screen offer the times taken by the
// session being moved.
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

	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var exclude *uuid.UUID
	if raw := r.URL.Query().Get("exclude_session_id"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "exclude_session_id must be a UUID")
			return
		}
		exclude = &parsed
	}

	slots, err := h.svc.OpenSlots(r.Context(), coachID, principal.UserID, from, to, duration, exclude)
	if err != nil {
		respondErr(w, err, "could not compute slots")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"slots": slots})
}

// bookSession handles POST /sessions.
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

// listMySessions handles GET /sessions for the calling client.
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

// cancelSession handles POST /sessions/{sessionID}/cancel.
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

// rescheduleSession handles POST /sessions/{sessionID}/reschedule.
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

// upsertProfile handles PUT /coach/profile for the calling coach.
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

// setAvailability handles PUT /coach/availability, replacing the whole schedule.
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

// listCoachSessions handles GET /coach/sessions for the coach dashboard.
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

// setSessionStatus handles POST /coach/sessions/{sessionID}/status, which is how
// a coach marks a session completed or a no-show.
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

// setSessionNotes handles POST /coach/sessions/{sessionID}/notes.
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

// pathUUID parses a UUID path parameter, writing 400 when it is malformed.
func pathUUID(w http.ResponseWriter, r *http.Request, param string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, param))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, param+" must be a uuid")
		return uuid.Nil, false
	}
	return id, true
}

// optionalQuery returns a query parameter as a pointer, nil when absent, which
// is how the queries express "no filter".
func optionalQuery(r *http.Request, key string) *string {
	if v := r.URL.Query().Get(key); v != "" {
		return &v
	}
	return nil
}

// respondErr maps the package's sentinel errors onto status codes; anything
// else is logged and reported as a 500 with the fallback message.
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
	case errors.Is(err, ErrExpired):
		httpx.Error(w, http.StatusGone, err.Error())
	default:
		slog.Error(fallback, "error", err)
		httpx.Error(w, http.StatusInternalServerError, fallback)
	}
}
