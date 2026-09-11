package analysis

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/httpx"
)

// Handler is the HTTP layer for /api/v1/analysis: it decodes requests, resolves
// the caller and path IDs, and delegates to Service.
type Handler struct {
	svc    *Service
	images *ImageService
}

// NewHandler builds the analysis handler; cmd/api mounts its Routes.
func NewHandler(svc *Service, images *ImageService) *Handler {
	return &Handler{svc: svc, images: images}
}

// maxImageBodyBytes bounds a POST /images body: ten maxImageBytes photos, plus
// the ~33% base64 overhead and headroom for the JSON envelope.
const maxImageBodyBytes = int64(maxImages*maxImageBytes)*4/3 + 64<<10

// Routes mounts the conversation-analysis endpoints (requires auth).
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/conversations", h.submit)
	r.Get("/conversations", h.listConversations)
	r.Get("/conversations/{conversationID}/result", h.latestResult)
	r.Post("/conversations/{conversationID}/reanalyze", h.reanalyze)
	r.Post("/conversations/{conversationID}/label", h.label)
	r.Get("/results/{analysisID}", h.result)
	r.Post("/images", h.analyzeImages)
	return r
}

// analyzeImages handles POST /images: assesses the submitted profile photos
// synchronously and returns the analyzer's verdict. Nothing is stored, so there
// is no ID to poll; an oversized body is a 413 before any decoding happens.
func (h *Handler) analyzeImages(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	var in ImageInput
	if err := decodeBounded(w, r, maxImageBodyBytes, &in); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return
		}
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	result, err := h.images.Analyze(r.Context(), principal.UserID, in)
	if err != nil {
		respondErr(w, err, "could not analyze images")
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

// decodeBounded reads a JSON body of at most limit bytes into dst, rejecting
// unknown fields like httpx.Decode and additionally anything after the first
// value, so trailing junk cannot ride past the size cap. A body over the limit
// surfaces as *http.MaxBytesError whether the overflow is in the value or
// after it; any other trailing content is a plain error.
func decodeBounded(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected data after JSON body")
		}
		return err
	}
	return nil
}

// submit handles POST /conversations: stores the transcript and returns the
// pending analysis, so clients can poll for the score.
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

// reanalyze handles POST /conversations/{conversationID}/reanalyze: queues a
// new scoring run for an already submitted transcript and answers with the
// pending analysis to poll.
func (h *Handler) reanalyze(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	conversationID, ok := pathUUID(w, r, "conversationID")
	if !ok {
		return
	}
	result, err := h.svc.Reanalyze(r.Context(), conversationID, principal.UserID)
	if err != nil {
		respondErr(w, err, "could not queue the analysis again")
		return
	}
	httpx.JSON(w, http.StatusAccepted, result)
}

// listConversations handles GET /conversations with 1-based offset paging.
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

// result handles GET /results/{analysisID}.
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

// latestResult handles GET /conversations/{conversationID}/result.
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

// label handles POST /conversations/{conversationID}/label, recording the
// real-world outcome used as training data.
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

// principalOf returns the authenticated caller, writing 401 when absent. The
// boolean reports whether the handler should continue.
func principalOf(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthenticated")
		return auth.Principal{}, false
	}
	return principal, true
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
	default:
		slog.Error(fallback, "error", err)
		httpx.Error(w, http.StatusInternalServerError, fallback)
	}
}
