package chat

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/httpx"
)

const (
	presenceRefresh   = 20 * time.Second
	revocationRefresh = 15 * time.Second
	writeTimeout      = 10 * time.Second
	historyOnJoin     = 50
)

// Handler is the HTTP layer for /api/v1/chat: the REST endpoints plus the
// WebSocket endpoint that streams a thread in both directions.
type Handler struct {
	svc            *Service
	hub            *Hub
	revocations    auth.Revocations
	originPatterns []string
}

// NewHandler builds the chat handler. originPatterns are the origins allowed to
// open a socket, and mirror the API's CORS configuration. revocations is the
// same denylist the REST middleware consults, re-checked for the lifetime of a
// socket because a connection authenticated once outlives the token that opened
// it.
func NewHandler(svc *Service, hub *Hub, revocations auth.Revocations, originPatterns []string) *Handler {
	return &Handler{svc: svc, hub: hub, revocations: revocations, originPatterns: originPatterns}
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

// startThread handles POST /threads, reusing the caller's active thread with
// the coach when one exists.
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

// listThreads handles GET /threads for the client side.
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

// listCoachThreads handles GET /coach/threads for the coach dashboard.
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

// history handles GET /threads/{threadID}/messages with 1-based offset paging.
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

// closeThread handles POST /threads/{threadID}/close.
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

	// A revocation announced while this socket is open closes it at once; the
	// ticker below is the backstop for announcements that never arrive.
	revoked := make(chan struct{})
	drop := sync.OnceFunc(func() { close(revoked) })
	defer h.hub.Register(principal.UserID, connID, drop)()

	// Re-check after registering, never before: an announcement made while this
	// socket was still being set up reached an index that did not list it yet.
	if verdict := h.sessionStatus(ctx, principal.UserID); verdict != socketActive {
		closeSocket(conn, verdict)
		return
	}

	incoming := make(chan Event, 8)
	go h.readLoop(ctx, cancel, conn, incoming)

	ticker := time.NewTicker(presenceRefresh)
	defer ticker.Stop()

	revocationTicker := time.NewTicker(revocationRefresh)
	defer revocationTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-revoked:
			closeSocket(conn, socketRevoked)
			return
		case <-revocationTicker.C:
			if verdict := h.sessionStatus(ctx, principal.UserID); verdict != socketActive {
				closeSocket(conn, verdict)
				return
			}
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
			if !h.handleIncoming(ctx, conn, threadID, principal, connID, event) {
				return
			}
		}
	}
}

// socketVerdict is the outcome of re-checking the account behind a live socket.
type socketVerdict int

const (
	socketActive socketVerdict = iota
	socketRevoked
	socketUnverifiable
)

// sessionStatus re-checks the account behind a socket. It is the
// socket-lifetime counterpart of auth.RequireActive, which only runs once, at
// the upgrade: without this a connection opened before an account was deleted
// would keep working until its token expired on its own.
//
// It fails closed, so a denylist it cannot read ends the connection rather than
// serving an account whose status is unknown. That case is reported as
// socketUnverifiable rather than socketRevoked, so a Redis outage does not tell
// clients their session is gone for good.
func (h *Handler) sessionStatus(ctx context.Context, userID uuid.UUID) socketVerdict {
	revoked, err := h.revocations.Revoked(ctx, userID)
	switch {
	case err != nil:
		slog.Error("check socket revocation", "error", err, "user_id", userID)
		return socketUnverifiable
	case revoked:
		return socketRevoked
	}
	return socketActive
}

// closeSocket drops a connection with the close code matching verdict, so a
// client can tell a session that is gone for good, which it answers by clearing
// its credentials, from one worth reconnecting to. Passing socketActive is a
// no-op.
func closeSocket(conn *websocket.Conn, verdict socketVerdict) {
	status, reason := websocket.StatusTryAgainLater, "could not verify session"
	switch verdict {
	case socketActive:
		return
	case socketRevoked:
		status, reason = websocket.StatusPolicyViolation, "session revoked"
	case socketUnverifiable:
	}
	if err := conn.Close(status, reason); err != nil {
		slog.Debug("close socket", "error", err, "reason", reason)
	}
}

// handleIncoming dispatches one client event: sending a message, relaying a
// typing indicator or refreshing presence. Rejected sends are reported back on
// the socket instead of closing it. It returns false once the caller's account
// can no longer act, having closed the socket: every client event is a
// privileged action, so none is dispatched without a fresh revocation check
// rather than waiting for the next heartbeat.
func (h *Handler) handleIncoming(ctx context.Context, conn *websocket.Conn, threadID uuid.UUID, principal auth.Principal, connID uuid.UUID, event Event) bool {
	if verdict := h.sessionStatus(ctx, principal.UserID); verdict != socketActive {
		closeSocket(conn, verdict)
		return false
	}
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
	return true
}

// readLoop decodes client frames onto out until the socket fails, then cancels
// the connection context so websocket returns. Undecodable frames are skipped.
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

// write sends one event with a bounded deadline, so a stalled client cannot
// block the connection's event loop.
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
	case errors.Is(err, ErrClosed):
		httpx.Error(w, http.StatusConflict, err.Error())
	default:
		slog.Error(fallback, "error", err)
		httpx.Error(w, http.StatusInternalServerError, fallback)
	}
}
