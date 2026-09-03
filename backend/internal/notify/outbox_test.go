package notify

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

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
	if err := pool.QueryRow(ctx, "SELECT attempts, last_error FROM email_outbox WHERE id = $1", row.ID).Scan(&attempts, &lastErr); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || lastErr == "" {
		t.Fatalf("after failure attempts=%d last_error=%q", attempts, lastErr)
	}

	n.fail = false
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
