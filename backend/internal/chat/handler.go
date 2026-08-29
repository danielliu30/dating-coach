package chat

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/httpx"
)

const (
	presenceRefresh = 20 * time.Second
	writeTimeout    = 10 * time.Second
	historyOnJoin   = 50
)

type Handler struct {
	svc            *Service
	hub            *Hub
	originPatterns []string
}

func NewHandler(svc *Service, hub *Hub, originPatterns []string) *Handler {
	return &Handler{svc: svc, hub: hub, originPatterns: originPatterns}
}

// Routes mounts the chat endpoints. All of them require authentication; the
// WebSocket route accepts the token as a query parameter.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/threads", h.startThread)
	r.Get("/threads", h.listThreads)
	r.Get("/threads/{threadID}/messages", h.history)
	r.Post("/threads/{threadID}/messages", h.postMessage)
	r.Post("/threads/{threadID}/close", h.closeThread)
	r.Get("/threads/{threadID}/ws", h.websocket)
	r.Route("/coach", func(coach chi.Router) {
		coach.Use(auth.RequireCoach)
		coach.Get("/threads", h.listCoachThreads)
	})
	return r
}

func (h *Handler) startThread(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	var in struct {
		CoachID   string  `json:"coach_id"`
		SessionID *string `json:"session_id"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	coachID, err := uuid.Parse(in.CoachID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "coach_id must be a uuid")
		return
	}
	var sessionID *uuid.UUID
	if in.SessionID != nil && *in.SessionID != "" {
		parsed, err := uuid.Parse(*in.SessionID)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "session_id must be a uuid")
			return
		}
		sessionID = &parsed
	}

	thread, err := h.svc.StartThread(r.Context(), principal.UserID, coachID, sessionID)
	if err != nil {
		respondErr(w, err, "could not start chat")
		return
	}
	httpx.JSON(w, http.StatusCreated, thread)
}

func (h *Handler) listThreads(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	threads, err := h.svc.ListForUser(r.Context(), principal.UserID)
	if err != nil {
		respondErr(w, err, "could not list chats")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"threads": threads})
}

func (h *Handler) listCoachThreads(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	var status *string
	if v := r.URL.Query().Get("status"); v != "" {
		status = &v
	}
	threads, err := h.svc.ListForCoach(r.Context(), principal.UserID, status)
	if err != nil {
		respondErr(w, err, "could not list chats")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"threads": threads})
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	threadID, ok := pathUUID(w, r, "threadID")
	if !ok {
		return
	}
	limit := httpx.QueryInt(r, "limit", 50, 200)
	offset := httpx.QueryInt(r, "offset", 1, 100_000) - 1

	messages, err := h.svc.History(r.Context(), threadID, principal.UserID, limit, offset)
	if err != nil {
		respondErr(w, err, "could not load messages")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"messages": messages})
}

// postMessage is the REST fallback for clients without an open socket.
func (h *Handler) postMessage(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	threadID, ok := pathUUID(w, r, "threadID")
	if !ok {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	msg, err := h.svc.Send(r.Context(), threadID, principal.UserID, in.Body)
	if err != nil {
		respondErr(w, err, "could not send message")
		return
	}
	httpx.JSON(w, http.StatusCreated, msg)
}

func (h *Handler) closeThread(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	threadID, ok := pathUUID(w, r, "threadID")
	if !ok {
		return
	}
	thread, err := h.svc.Close(r.Context(), threadID, principal.UserID)
	if err != nil {
		respondErr(w, err, "could not close chat")
		return
	}
	httpx.JSON(w, http.StatusOK, thread)
}

// websocket upgrades the connection and streams thread events both ways.
func (h *Handler) websocket(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	threadID, ok := pathUUID(w, r, "threadID")
	if !ok {
		return
	}
	if _, err := h.svc.Thread(r.Context(), threadID, principal.UserID); err != nil {
		respondErr(w, err, "could not open chat")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.originPatterns})
	if err != nil {
		slog.Error("websocket accept", "error", err)
		return
	}
	defer conn.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	events, unsubscribe := h.hub.Subscribe(threadID)
	defer unsubscribe()

	connID := uuid.New()
	if err := h.hub.MarkOnline(ctx, principal.UserID, connID); err != nil {
		slog.Error("mark online", "error", err)
	}
	defer func() {
		if err := h.hub.MarkOffline(context.WithoutCancel(ctx), principal.UserID, connID); err != nil {
			slog.Error("mark offline", "error", err)
		}
	}()

	if history, err := h.svc.History(ctx, threadID, principal.UserID, historyOnJoin, 0); err == nil {
		h.write(ctx, conn, Event{Type: EventHistory, ThreadID: threadID.String(), Messages: history})
	}

	incoming := make(chan Event, 8)
	go h.readLoop(ctx, cancel, conn, incoming)

	ticker := time.NewTicker(presenceRefresh)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := h.hub.MarkOnline(ctx, principal.UserID, connID); err != nil {
				slog.Error("refresh presence", "error", err)
			}
		case event, open := <-events:
			if !open {
				return
			}
			h.write(ctx, conn, event)
		case event, open := <-incoming:
			if !open {
				return
			}
			h.handleIncoming(ctx, conn, threadID, principal, connID, event)
		}
	}
}

func (h *Handler) handleIncoming(ctx context.Context, conn *websocket.Conn, threadID uuid.UUID, principal auth.Principal, connID uuid.UUID, event Event) {
	switch event.Type {
	case EventMessage:
		if _, err := h.svc.Send(ctx, threadID, principal.UserID, event.Body); err != nil {
			h.write(ctx, conn, Event{Type: EventError, ThreadID: threadID.String(), Body: err.Error()})
		}
	case EventTyping:
		if err := h.svc.Typing(ctx, threadID, principal.UserID, event.Typing); err != nil {
			slog.Error("publish typing", "error", err)
		}
	case EventPresence:
		if err := h.hub.MarkOnline(ctx, principal.UserID, connID); err != nil {
			slog.Error("refresh presence", "error", err)
		}
	default:
		h.write(ctx, conn, Event{Type: EventError, Body: "unknown event type"})
	}
}

func (h *Handler) readLoop(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, out chan<- Event) {
	defer cancel()
	defer close(out)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var event Event
		if err := json.Unmarshal(data, &event); err != nil {
			continue
		}
		select {
		case out <- event:
		case <-ctx.Done():
			return
		}
	}
}

func (h *Handler) write(ctx context.Context, conn *websocket.Conn, event Event) {
	payload, err := json.Marshal(event)
	if err != nil {
		slog.Error("encode chat event", "error", err)
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := conn.Write(writeCtx, websocket.MessageText, payload); err != nil {
		slog.Debug("write chat event", "error", err)
	}
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
	case errors.Is(err, ErrClosed):
		httpx.Error(w, http.StatusConflict, err.Error())
	default:
		slog.Error(fallback, "error", err)
		httpx.Error(w, http.StatusInternalServerError, fallback)
	}
}
