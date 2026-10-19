// Package live serves the notifications WebSocket: a server-to-client channel
// pushing a user's notification events, fanned out through the notify.Hub.
package live

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/httpx"
	"github.com/danielliu30/dating-coach/backend/internal/notify"
)

const (
	accountRefresh = 15 * time.Second
	writeTimeout   = 10 * time.Second
)

// readyTimeout bounds how long a new socket waits for its Redis subscription
// before announcing itself anyway, so the client refetches without waiting on
// Redis; a second ready frame follows once the subscription is live. A
// variable so tests can shorten it.
var readyTimeout = 5 * time.Second

// AccountStatus answers whether an account may still act, from wherever that
// is recorded durably, so sockets can be exercised without a database.
type AccountStatus interface {
	// UserActive reports whether the account exists and is not marked deleted.
	UserActive(ctx context.Context, id uuid.UUID) (bool, error)
}

// Handler serves /api/v1/notifications: a server-to-client WebSocket that
// pushes the caller's notification events as they are published.
type Handler struct {
	hub            *notify.Hub
	accounts       AccountStatus
	originPatterns []string
}

// NewHandler builds the notifications handler. originPatterns are the origins
// allowed to open a socket and mirror the API's CORS configuration; accounts is
// re-read for the lifetime of a socket, because a connection authenticated once
// outlives the token that opened it.
func NewHandler(hub *notify.Hub, accounts AccountStatus, originPatterns []string) *Handler {
	return &Handler{hub: hub, accounts: accounts, originPatterns: originPatterns}
}

// Routes mounts the notification endpoints; the caller must already be
// authenticated, with the WebSocket token accepted as a query parameter.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/ws", h.websocket)
	return r
}

// websocket upgrades the connection and streams the caller's notifications
// until the client leaves, the token expires or the account is revoked. Once
// the Redis subscription is live it writes a notify.EventReady frame, the
// client's cue to refetch whatever finished while it was not listening. Client
// frames are read only to notice the close.
func (h *Handler) websocket(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.originPatterns})
	if err != nil {
		slog.Error("notifications websocket accept", "error", err)
		return
	}
	defer conn.CloseNow()

	ctx := conn.CloseRead(r.Context())

	events, ready, unsubscribe := h.hub.Subscribe(principal.UserID)
	defer unsubscribe()

	// A revocation announced while this socket is open closes it at once; the
	// ticker below is the backstop for announcements that never arrive.
	connID := uuid.New()
	revoked := make(chan struct{})
	drop := sync.OnceFunc(func() { close(revoked) })
	defer h.hub.Register(principal.UserID, connID, drop)()

	// Re-check after registering, never before: an announcement made while this
	// socket was still being set up reached an index that did not list it yet.
	if verdict := h.sessionStatus(ctx, principal.UserID); verdict != socketActive {
		closeSocket(conn, verdict, principal.UserID, connID, "connect")
		return
	}

	expiry := expiryTimer(principal.ExpiresAt)
	defer expiry.Stop()

	readyTimer := time.NewTimer(readyTimeout)
	defer readyTimer.Stop()
	// Set when ready is announced early: anything published before the Redis
	// subscription is live never reaches this socket, so its arrival is a
	// second cue to refetch.
	var lateReady <-chan struct{}
	select {
	case <-ctx.Done():
		return
	case <-revoked:
		closeSocket(conn, socketRevoked, principal.UserID, connID, "announcement")
		return
	case <-expiry.C:
		closeSocket(conn, socketExpired, principal.UserID, connID, "expiry")
		return
	case <-ready:
	case <-readyTimer.C:
		slog.Warn("notification subscription not confirmed; announcing socket anyway", "user_id", principal.UserID)
		lateReady = ready
	}
	h.write(ctx, conn, notify.Event{Type: notify.EventReady})

	accountTicker := time.NewTicker(accountRefresh)
	defer accountTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-revoked:
			closeSocket(conn, socketRevoked, principal.UserID, connID, "announcement")
			return
		case <-expiry.C:
			closeSocket(conn, socketExpired, principal.UserID, connID, "expiry")
			return
		case <-lateReady:
			lateReady = nil
			h.write(ctx, conn, notify.Event{Type: notify.EventReady})
		case <-accountTicker.C:
			if verdict := h.sessionStatus(ctx, principal.UserID); verdict != socketActive {
				closeSocket(conn, verdict, principal.UserID, connID, "heartbeat")
				return
			}
		case event, open := <-events:
			if !open {
				return
			}
			h.write(ctx, conn, event)
		}
	}
}

// socketVerdict is the outcome of re-checking the account behind a live socket.
type socketVerdict int

const (
	socketActive socketVerdict = iota
	socketRevoked
	socketExpired
	socketUnverifiable
)

// expiryTimer fires when a token expires, at once for a deadline already
// passed, and never for the zero time a token without an expiry claim leaves on
// the Principal.
func expiryTimer(expiresAt time.Time) *time.Timer {
	if expiresAt.IsZero() {
		return time.NewTimer(math.MaxInt64)
	}
	return time.NewTimer(time.Until(expiresAt))
}

// sessionStatus re-checks the account behind a socket against its durable
// record. It fails closed: a status it cannot read is socketUnverifiable, which
// ends the connection without telling the client its session is gone for good.
func (h *Handler) sessionStatus(ctx context.Context, userID uuid.UUID) socketVerdict {
	active, err := h.accounts.UserActive(ctx, userID)
	switch {
	case err != nil:
		slog.Error("check notification socket account status", "error", err, "user_id", userID)
		return socketUnverifiable
	case !active:
		return socketRevoked
	}
	return socketActive
}

// closeSocket drops a connection with the close code matching verdict, the same
// codes and reasons the chat socket uses, so one client rule tells a session
// worth renewing ("session expired"), one that is gone ("session revoked") and
// a transient failure apart. trigger names what noticed and is logged. Passing
// socketActive is a no-op.
func closeSocket(conn *websocket.Conn, verdict socketVerdict, userID, connID uuid.UUID, trigger string) {
	status, reason := websocket.StatusTryAgainLater, "could not verify session"
	switch verdict {
	case socketActive:
		return
	case socketRevoked:
		status, reason = websocket.StatusPolicyViolation, "session revoked"
	case socketExpired:
		status, reason = websocket.StatusPolicyViolation, "session expired"
	case socketUnverifiable:
	}
	slog.Info(
		"close notification socket",
		"reason", reason,
		"status", int(status),
		"trigger", trigger,
		"user_id", userID,
		"conn_id", connID,
	)
	if err := conn.Close(status, reason); err != nil {
		slog.Debug("close notification socket", "error", err, "reason", reason)
	}
}

// write sends one event with a bounded deadline, so a stalled client cannot
// block the socket's loop. Failures are logged, not returned: a socket that
// can no longer be written to is about to close anyway.
func (h *Handler) write(ctx context.Context, conn *websocket.Conn, event notify.Event) {
	payload, err := json.Marshal(event)
	if err != nil {
		slog.Error("encode notification event", "error", err)
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := conn.Write(writeCtx, websocket.MessageText, payload); err != nil {
		slog.Debug("write notification event", "error", err)
	}
}
