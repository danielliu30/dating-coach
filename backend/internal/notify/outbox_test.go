package notify

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/config"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// stalledSMTP accepts connections and never speaks, standing in for a server
// that hangs mid-handshake. It returns the port it listens on.
func stalledSMTP(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestSendGivesUpOnStalledServer(t *testing.T) {
	svc := New(&config.Config{SMTPHost: "127.0.0.1", SMTPPort: stalledSMTP(t), SMTPTimeout: 200 * time.Millisecond, MailFrom: "from@example.test"})
	start := time.Now()
	err := svc.Send(context.Background(), Message{To: "to@example.test", Subject: "s", Body: "b"})
	if err == nil {
		t.Fatal("Send succeeded against a server that never answers")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Send took %s, timeout not enforced", took)
	}
}

func TestSendStopsWhenContextCancelled(t *testing.T) {
	svc := New(&config.Config{SMTPHost: "127.0.0.1", SMTPPort: stalledSMTP(t), SMTPTimeout: time.Minute, MailFrom: "from@example.test"})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if err := svc.Send(ctx, Message{To: "to@example.test", Subject: "s", Body: "b"}); err == nil {
		t.Fatal("Send succeeded after cancellation")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Send took %s after cancel, connection not closed", took)
	}
}

func TestEncodeAttachesInviteWithMethod(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nMETHOD:CANCEL\r\nEND:VCALENDAR\r\n"
	raw, err := encode("from@example.test", Message{To: "to@example.test", Subject: "s", Body: "hello", ICS: ics})
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse message: %v", err)
	}
	mediaType, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" {
		t.Fatalf("content-type = %q (%v), want multipart/mixed", m.Header.Get("Content-Type"), err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	var types []string
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, p.Header.Get("Content-Type"))
	}
	if len(types) != 2 || !strings.HasPrefix(types[0], "text/plain") || types[1] != "text/calendar; charset=utf-8; method=CANCEL" {
		t.Fatalf("parts = %v", types)
	}
}

func TestRetryDelayBacksOffAndCaps(t *testing.T) {
	if d := retryDelay(0); d != minRetryDelay {
		t.Fatalf("first retry = %v, want %v", d, minRetryDelay)
	}
	if d := retryDelay(3); d != 8*minRetryDelay {
		t.Fatalf("fourth retry = %v, want %v", d, 8*minRetryDelay)
	}
	if d := retryDelay(MaxAttempts); d != maxRetryDelay {
		t.Fatalf("late retry = %v, want cap %v", d, maxRetryDelay)
	}
	var total time.Duration
	for i := int32(0); i < MaxAttempts; i++ {
		total += retryDelay(i)
	}
	if total < 24*time.Hour {
		t.Fatalf("attempts span %v, want at least a day of retries", total)
	}
}

func TestEncodePlainWithoutInvite(t *testing.T) {
	raw, err := encode("from@example.test", Message{To: "to@example.test", Subject: "s", Body: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("Content-Type: text/plain")) || bytes.Contains(raw, []byte("multipart")) {
		t.Fatalf("plain message = %q", raw)
	}
}

// recordingNotifier remembers what it sent; fail makes every send fail.
type recordingNotifier struct {
	sent []Message
	fail bool
}

// Send records msg, or fails when configured to.
func (n *recordingNotifier) Send(_ context.Context, msg Message) error {
	if n.fail {
		return errors.New("smtp is down")
	}
	n.sent = append(n.sent, msg)
	return nil
}

// Email is unused by the relay.
func (n *recordingNotifier) Email(context.Context, string, string, string) error { return nil }

// Push is unused by the relay.
func (n *recordingNotifier) Push(context.Context, string, string, string) error { return nil }

// testQueries connects to TEST_DATABASE_URL, skipping the test when it is unset
// so the suite still runs without Postgres.
func testQueries(t *testing.T) (*db.Queries, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	return db.New(pool), pool
}

func TestDrainSendsOnceAndRetriesFailures(t *testing.T) {
	q, pool := testQueries(t)
	ctx := context.Background()
	to := "relay-" + t.Name() + "@example.test"
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM email_outbox WHERE to_email = $1", to) })
	ics := "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"
	row, err := q.EnqueueEmail(ctx, db.EnqueueEmailParams{ToEmail: to, Subject: "s", Body: "b", Ics: &ics})
	if err != nil {
		t.Fatal(err)
	}

	n := &recordingNotifier{fail: true}
	relay := NewRelay(q, n)
	if sent, err := relay.Drain(ctx); err != nil || sent != 0 {
		t.Fatalf("failing drain = %d, %v", sent, err)
	}
	var attempts int32
	var lastErr string
	var deferred bool
	if err := pool.QueryRow(ctx, "SELECT attempts, last_error, next_attempt_at > now() FROM email_outbox WHERE id = $1", row.ID).Scan(&attempts, &lastErr, &deferred); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || lastErr == "" || !deferred {
		t.Fatalf("after failure attempts=%d last_error=%q deferred=%v", attempts, lastErr, deferred)
	}

	n.fail = false
	if _, err := relay.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	for _, m := range n.sent {
		if m.To == to {
			t.Fatal("a failed email was retried before its backoff elapsed")
		}
	}
	if _, err := pool.Exec(ctx, "UPDATE email_outbox SET next_attempt_at = now() WHERE id = $1", row.ID); err != nil {
		t.Fatal(err)
	}
	if sent, err := relay.Drain(ctx); err != nil || sent < 1 {
		t.Fatalf("drain = %d, %v; want the queued email sent", sent, err)
	}
	found := false
	for _, m := range n.sent {
		if m.To == to && m.ICS == ics {
			found = true
		}
	}
	if !found {
		t.Fatalf("sent = %v, want message to %s with its invite", n.sent, to)
	}
	n.sent = nil
	if _, err := relay.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	for _, m := range n.sent {
		if m.To == to {
			t.Fatal("a sent email was delivered again")
		}
	}
}
