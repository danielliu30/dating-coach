package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// stubAccounts answers UserActive from fixed values, standing in for Postgres.
type stubAccounts struct {
	active bool
	err    error
}

// UserActive satisfies AccountStatus.
func (s stubAccounts) UserActive(context.Context, uuid.UUID) (bool, error) {
	return s.active, s.err
}

// flakyAccounts fails UserActive until failures run out, then delegates to
// next: a transient outage that heals while the socket is open.
type flakyAccounts struct {
	failures int
	err      error
	next     stubAccounts
}

// UserActive satisfies AccountStatus.
func (f *flakyAccounts) UserActive(ctx context.Context, id uuid.UUID) (bool, error) {
	if f.failures > 0 {
		f.failures--
		return false, f.err
	}
	return f.next.UserActive(ctx, id)
}

func TestSessionStatus(t *testing.T) {
	tests := []struct {
		name     string
		accounts stubAccounts
		want     socketVerdict
	}{
		{"active account keeps its socket", stubAccounts{active: true}, socketActive},
		{"deleted account loses its socket", stubAccounts{}, socketRevoked},
		{"unreachable database fails closed but stays retryable", stubAccounts{err: errors.New("dial postgres: connection refused")}, socketUnverifiable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(nil, nil, tc.accounts, nil)
			if got := h.sessionStatus(context.Background(), auth.Principal{UserID: uuid.New()}); got != tc.want {
				t.Fatalf("sessionStatus = %d, want %d", got, tc.want)
			}
		})
	}
}

type stubRevocations struct {
	since time.Time
	err   error
}

func (s stubRevocations) RevokedSince(context.Context, uuid.UUID) (time.Time, error) {
	return s.since, s.err
}

func TestSessionStatusHonoursRevocationCutoff(t *testing.T) {
	signedOut := time.Now()
	tests := []struct {
		name        string
		revocations stubRevocations
		issuedAt    time.Time
		want        socketVerdict
	}{
		{"never revoked keeps its socket", stubRevocations{}, signedOut, socketActive},
		{"token from before the sign-out loses its socket", stubRevocations{since: signedOut}, signedOut.Add(-time.Minute), socketRevoked},
		{"token minted at the cutoff loses its socket", stubRevocations{since: signedOut}, signedOut, socketRevoked},
		{"token from a later sign-in keeps its socket", stubRevocations{since: signedOut}, signedOut.Add(time.Second), socketActive},
		{"unreachable redis fails closed but stays retryable", stubRevocations{err: errors.New("dial redis: connection refused")}, signedOut, socketUnverifiable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(nil, nil, stubAccounts{active: true}, nil)
			h.SetRevocations(tc.revocations)
			principal := auth.Principal{UserID: uuid.New(), IssuedAt: tc.issuedAt}
			if got := h.sessionStatus(context.Background(), principal); got != tc.want {
				t.Fatalf("sessionStatus = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestHandleIncomingClosesInactiveSocket drives one client event through a live
// socket whose account no longer checks out, and asserts the event is not
// dispatched (the handler has no service or hub, so acting on it would panic)
// and that the client can tell a revoked session from an unverifiable one.
func TestHandleIncomingClosesInactiveSocket(t *testing.T) {
	tests := []struct {
		name      string
		accounts  stubAccounts
		expiresAt time.Time
		want      websocket.StatusCode
	}{
		{"deleted account is told to stop reconnecting", stubAccounts{}, time.Time{}, websocket.StatusPolicyViolation},
		{"expired token stops acting", stubAccounts{active: true}, time.Now().Add(-time.Minute), websocket.StatusPolicyViolation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertSocketClosed(t, tc.accounts, tc.expiresAt, tc.want)
		})
	}
}

// TestHandleIncomingToleratesTransientUnverifiable exercises a socket through
// a short outage: fewer unverifiable checks than maxUnverifiableStreak leave
// it open and dispatching, and the next healthy check resets the streak.
func TestHandleIncomingToleratesTransientUnverifiable(t *testing.T) {
	flaky := &flakyAccounts{
		failures: maxUnverifiableStreak - 1,
		err:      errors.New("dial postgres: connection refused"),
		next:     stubAccounts{active: true},
	}
	h := NewHandler(NewService(nil, nil), nil, flaky, nil)

	dispatched := make(chan bool, maxUnverifiableStreak)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		principal := auth.Principal{UserID: uuid.New()}
		guard := &socketGuard{}
		for i := 0; i < maxUnverifiableStreak; i++ {
			// The whitespace send is rejected and echoed as an error event,
			// which doubles as proof on the wire that the socket stayed open.
			dispatched <- h.handleIncoming(r.Context(), conn, uuid.New(), principal, uuid.New(), guard,
				Event{Type: EventMessage, ClientID: "c", Body: " "})
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	for i := 0; i < maxUnverifiableStreak; i++ {
		if !<-dispatched {
			t.Fatalf("event %d dropped the socket on a %d-check outage", i+1, maxUnverifiableStreak-1)
		}
		if _, _, err := conn.Read(ctx); err != nil {
			t.Fatalf("socket closed on a %d-check outage: %v", maxUnverifiableStreak-1, websocket.CloseStatus(err))
		}
	}
}

// TestHandleIncomingClosesAfterUnverifiableStreak exercises the ceiling: an
// outage that never heals still closes the socket once the streak reaches
// maxUnverifiableStreak, with the retryable status rather than a revocation.
func TestHandleIncomingClosesAfterUnverifiableStreak(t *testing.T) {
	h := NewHandler(NewService(nil, nil), nil,
		stubAccounts{err: errors.New("dial postgres: connection refused")}, nil)

	dispatched := make(chan bool, maxUnverifiableStreak)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		principal := auth.Principal{UserID: uuid.New()}
		guard := &socketGuard{}
		for i := 0; i < maxUnverifiableStreak; i++ {
			dispatched <- h.handleIncoming(r.Context(), conn, uuid.New(), principal, uuid.New(), guard,
				Event{Type: EventMessage, ClientID: "c", Body: " "})
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	// Read from one goroutine so a read is always in flight when the close
	// frame arrives: reads issued after the peer's close is processed can
	// return the lock error rather than the close status.
	reads := make(chan error, maxUnverifiableStreak+1)
	go func() {
		for {
			_, _, err := conn.Read(ctx)
			reads <- err
			if err != nil {
				return
			}
		}
	}()

	for i := 0; i < maxUnverifiableStreak-1; i++ {
		if !<-dispatched {
			t.Fatalf("event %d closed before the streak bound", i+1)
		}
		if err := <-reads; err != nil {
			t.Fatalf("socket closed before the streak bound: %v", websocket.CloseStatus(err))
		}
	}
	if <-dispatched {
		t.Fatal("socket stayed open past maxUnverifiableStreak")
	}
	if err := <-reads; websocket.CloseStatus(err) != websocket.StatusTryAgainLater {
		t.Fatalf("close status = %v, want %v", websocket.CloseStatus(err), websocket.StatusTryAgainLater)
	}
}

// TestExpiryTimer checks that a socket only carries a deadline when its token
// has one, and that an already expired token does not wait for it.
func TestExpiryTimer(t *testing.T) {
	past := expiryTimer(time.Now().Add(-time.Minute))
	defer past.Stop()
	select {
	case <-past.C:
	case <-time.After(time.Second):
		t.Fatal("an expired token kept its socket")
	}

	never := expiryTimer(time.Time{})
	defer never.Stop()
	select {
	case <-never.C:
		t.Fatal("a token without an expiry lost its socket")
	case <-time.After(100 * time.Millisecond):
	}
}

// assertSocketClosed serves one socket with a handler backed by accounts and a
// caller whose token expires at expiresAt, feeds it a message event and fails
// the test unless the socket was closed with want and the event went
// undispatched.
func assertSocketClosed(t *testing.T, accounts stubAccounts, expiresAt time.Time, want websocket.StatusCode) {
	t.Helper()
	h := NewHandler(nil, nil, accounts, nil)

	dispatched := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		event := Event{Type: EventMessage, Body: "still here"}
		principal := auth.Principal{UserID: uuid.New(), ExpiresAt: expiresAt}
		dispatched <- h.handleIncoming(r.Context(), conn, uuid.New(), principal, uuid.New(), &socketGuard{}, event)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != want {
		t.Fatalf("close status = %v, want %v", websocket.CloseStatus(err), want)
	}
	if <-dispatched {
		t.Fatal("handleIncoming kept the socket open for an inactive account")
	}
}

// TestSessionStatusReadsTheAccountRow runs the check against Postgres, which is
// the point of it: deletion marks the row before anything is announced, so a
// socket's verdict follows the mark with no cache in between.
func TestSessionStatusReadsTheAccountRow(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	var userID uuid.UUID
	if err := pool.QueryRow(
		ctx,
		`INSERT INTO users (email, password_hash, display_name) VALUES ($1, 'hash', 'Live') RETURNING id`,
		uuid.NewString()+"@example.test",
	).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	})

	h := NewHandler(nil, nil, db.New(pool), nil)
	if got := h.sessionStatus(ctx, auth.Principal{UserID: userID}); got != socketActive {
		t.Fatalf("sessionStatus of a live account = %d, want %d", got, socketActive)
	}

	if _, err := pool.Exec(ctx, "UPDATE users SET deleted_at = now() WHERE id = $1", userID); err != nil {
		t.Fatalf("mark user deleted: %v", err)
	}
	if got := h.sessionStatus(ctx, auth.Principal{UserID: userID}); got != socketRevoked {
		t.Fatalf("sessionStatus of a deleted account = %d, want %d", got, socketRevoked)
	}
}

// TestHandleIncomingRejectionEchoesClientID sends an empty message, which the
// service refuses before touching storage, and asserts the error frame names
// the client's own id for that send.
func TestHandleIncomingRejectionEchoesClientID(t *testing.T) {
	h := NewHandler(NewService(nil, nil), nil, stubAccounts{active: true}, nil)
	threadID := uuid.New()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		event := Event{Type: EventMessage, ClientID: "c-42", Body: "   "}
		principal := auth.Principal{UserID: uuid.New()}
		h.handleIncoming(r.Context(), conn, threadID, principal, uuid.New(), &socketGuard{}, event)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	if got.Type != EventError || got.ClientID != "c-42" || got.ThreadID != threadID.String() {
		t.Fatalf("error frame = %+v, want type %q with client_id c-42 for thread %s", got, EventError, threadID)
	}
}

// TestCloseSockets verifies shutdown draining: an accepted socket registered
// with track is closed with a normal closure status when CloseSockets runs,
// which is what lets clients reconnect cleanly during a deploy instead of
// waiting for the task to be killed.
func TestCloseSockets(t *testing.T) {
	h := NewHandler(nil, nil, stubAccounts{active: true}, nil)

	accepted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		defer h.track(conn)()
		close(accepted)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	<-accepted

	h.CloseSockets()

	// Fresh deadline: CloseSockets may block on the peer's handshake reply.
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	if _, _, err := conn.Read(readCtx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("close status = %v, want %v", websocket.CloseStatus(err), websocket.StatusNormalClosure)
	}
}
