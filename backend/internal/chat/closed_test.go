package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// closedThreadFixture is a client/coach pair with one thread between them and
// a service whose hub publishes to an in-memory Redis, so an accepted send
// completes instead of panicking.
type closedThreadFixture struct {
	pool     *pgxpool.Pool
	svc      *Service
	client   uuid.UUID
	threadID uuid.UUID
}

func newClosedThreadFixture(t *testing.T) closedThreadFixture {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	client, coach := insertUser(t, pool, "user"), insertUser(t, pool, "coach")
	if _, err := pool.Exec(ctx, `INSERT INTO coaches (user_id) VALUES ($1)`, coach); err != nil {
		t.Fatalf("insert coach: %v", err)
	}
	var threadID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO chat_threads (user_id, coach_id) VALUES ($1, $2) RETURNING id`, client, coach,
	).Scan(&threadID); err != nil {
		t.Fatalf("insert thread: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return closedThreadFixture{pool: pool, svc: NewService(db.New(pool), NewHub(rdb)), client: client, threadID: threadID}
}

func (f closedThreadFixture) close(t *testing.T) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `UPDATE chat_threads SET status = 'closed' WHERE id = $1`, f.threadID); err != nil {
		t.Fatalf("close thread: %v", err)
	}
}

func (f closedThreadFixture) messageCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM chat_messages WHERE thread_id = $1`, f.threadID,
	).Scan(&n); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	return n
}

// TestSendIntoClosedThreadOverSocket drives a message frame through the
// WebSocket path and expects the correlated rejection and no stored row.
func TestSendIntoClosedThreadOverSocket(t *testing.T) {
	f := newClosedThreadFixture(t)
	f.close(t)
	h := NewHandler(f.svc, nil, stubAccounts{active: true}, nil)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		event := Event{Type: EventMessage, ClientID: "c-closed", Body: "anyone there?"}
		h.handleIncoming(r.Context(), conn, f.threadID, auth.Principal{UserID: f.client}, uuid.New(), event)
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
	if got.Type != EventError || got.ClientID != "c-closed" || got.Body != ErrClosed.Error() {
		t.Fatalf("frame = %+v, want %q error %q for client_id c-closed", got, EventError, ErrClosed)
	}
	if n := f.messageCount(t); n != 0 {
		t.Fatalf("stored %d messages in a closed thread", n)
	}
}

// TestPostMessageIntoClosedThread covers the REST fallback.
func TestPostMessageIntoClosedThread(t *testing.T) {
	f := newClosedThreadFixture(t)
	f.close(t)
	h := NewHandler(f.svc, nil, stubAccounts{active: true}, nil)

	req := httptest.NewRequest(http.MethodPost, "/threads/"+f.threadID.String()+"/messages", strings.NewReader(`{"body":"anyone there?"}`))
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{UserID: f.client}))
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d (%s), want 409", rec.Code, rec.Body)
	}
	if n := f.messageCount(t); n != 0 {
		t.Fatalf("stored %d messages in a closed thread", n)
	}
}

// TestSendIntoActiveThreadStoresIt is the control for the guarded insert: an
// open thread still accepts and stores the message.
func TestSendIntoActiveThreadStoresIt(t *testing.T) {
	f := newClosedThreadFixture(t)
	msg, err := f.svc.Send(context.Background(), f.threadID, f.client, "still open", "")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if msg.ThreadID != f.threadID.String() || msg.SenderID != f.client.String() || msg.Body != "still open" {
		t.Fatalf("message = %+v", msg)
	}
	if n := f.messageCount(t); n != 1 {
		t.Fatalf("stored %d messages, want 1", n)
	}
}

// TestSendRacingCloseStoresNothing closes the thread in a transaction that is
// still open while Send runs, so Send's own read still sees an active thread.
// The INSERT must wait for that transaction and, once it commits, store
// nothing: the status guard has to be part of the write, not a prior read.
func TestSendRacingCloseStoresNothing(t *testing.T) {
	f := newClosedThreadFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `UPDATE chat_threads SET status = 'closed' WHERE id = $1`, f.threadID); err != nil {
		t.Fatalf("close thread in tx: %v", err)
	}

	sent := make(chan error, 1)
	go func() {
		_, err := f.svc.Send(ctx, f.threadID, f.client, "racing the close", "")
		sent <- err
	}()

	waitForLockedInsert(ctx, t, f.pool, sent)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit close: %v", err)
	}
	if err := <-sent; !errors.Is(err, ErrClosed) {
		t.Fatalf("Send = %v, want ErrClosed", err)
	}
	if n := f.messageCount(t); n != 0 {
		t.Fatalf("stored %d messages in a thread closed mid-send", n)
	}
}

// waitForLockedInsert returns once a chat query is blocked on a row lock, which
// the thread row's open UPDATE makes Send's write do, failing the test if Send
// finishes first.
func waitForLockedInsert(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sent <-chan error) {
	t.Helper()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%chat_%' AND pid <> pg_backend_pid())`).Scan(&waiting); err != nil {
			t.Fatalf("poll pg_stat_activity: %v", err)
		}
		if waiting {
			return
		}
		select {
		case err := <-sent:
			t.Fatalf("Send returned %v without waiting for the closing transaction", err)
		case <-ctx.Done():
			t.Fatal("timed out waiting for Send to block on the thread row")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
