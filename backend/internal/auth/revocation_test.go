package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

func TestDenylistRevokedSince(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	d := NewDenylist(rdb, time.Minute)
	user := uuid.New()

	if since, err := d.RevokedSince(ctx, user); err != nil || !since.IsZero() {
		t.Fatalf("never revoked = %v, %v; want zero, nil", since, err)
	}
	before := time.Now()
	if err := d.RevokeSessions(ctx, user); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	since, err := d.RevokedSince(ctx, user)
	if err != nil || since.Before(before) || since.After(time.Now()) {
		t.Fatalf("revoked since = %v, %v; want between %v and now", since, err, before)
	}

	legacy := uuid.New()
	mr.Set(revokedKeyPrefix+legacy.String(), "1")
	if since, err := d.RevokedSince(ctx, legacy); err != nil || !since.After(time.Now().Add(24*time.Hour)) {
		t.Fatalf("legacy entry = %v, %v; want a time that revokes every token", since, err)
	}

	deleted := uuid.New()
	if err := d.Revoke(ctx, deleted); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := d.RevokeSessions(ctx, deleted); err != nil {
		t.Fatalf("revoke sessions: %v", err)
	}
	if since, err := d.RevokedSince(ctx, deleted); err != nil || !since.After(time.Now().Add(24*time.Hour)) {
		t.Fatalf("account-wide revoke after a sign-out = %v, %v; want it to still revoke every token", since, err)
	}
	if ttl := mr.TTL(revokedKeyPrefix + deleted.String()); ttl != time.Minute+revocationGrace {
		t.Fatalf("entry ttl = %v, want %v", ttl, time.Minute+revocationGrace)
	}

	newer := time.Now()
	if err := d.write(ctx, user, strconv.FormatInt(newer.UnixNano(), 10), true); err != nil {
		t.Fatalf("write newer cutoff: %v", err)
	}
	if err := d.write(ctx, user, strconv.FormatInt(newer.Add(-time.Second).UnixNano(), 10), true); err != nil {
		t.Fatalf("write older cutoff: %v", err)
	}
	if since, err := d.RevokedSince(ctx, user); err != nil || !since.Equal(time.Unix(0, newer.UnixNano())) {
		t.Fatalf("cutoff after a delayed older write = %v, %v; want %v", since, err, newer)
	}

	mr.Close()
	if _, err := d.RevokedSince(ctx, user); err == nil {
		t.Fatal("redis down returned no error; want one so callers fail closed")
	}
}

// fakeRevocations answers RevokedSince from a fixed map, or with err when set.
type fakeRevocations struct {
	since map[uuid.UUID]time.Time
	err   error
}

func (f fakeRevocations) RevokedSince(_ context.Context, userID uuid.UUID) (time.Time, error) {
	return f.since[userID], f.err
}

func TestMiddlewareRefusesRevokedSessions(t *testing.T) {
	issuer := NewTokenIssuer("test-secret")
	user := uuid.New()
	token, _, err := issuer.Issue(user, "a@example.com", RoleUser, ScopeSession, time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	issued := time.Now()
	ticketOwner := Principal{UserID: user, Scope: ScopeSession, IssuedAt: issued.Add(-time.Hour)}
	redeemer := fakeRedeemer{tickets: map[string]Principal{"tkt": ticketOwner}}

	tests := []struct {
		name   string
		revs   Revocations
		ticket bool
		want   int
	}{
		{"never revoked passes", fakeRevocations{}, false, http.StatusNoContent},
		{"token issued before the revocation is refused", fakeRevocations{since: map[uuid.UUID]time.Time{user: issued.Add(time.Second)}}, false, http.StatusUnauthorized},
		{"token issued after the revocation passes", fakeRevocations{since: map[uuid.UUID]time.Time{user: issued.Add(-time.Minute)}}, false, http.StatusNoContent},
		{"redis down fails closed", fakeRevocations{err: errors.New("redis down")}, false, http.StatusServiceUnavailable},
		{"ticket minted from a revoked token is refused", fakeRevocations{since: map[uuid.UUID]time.Time{user: issued.Add(-time.Minute)}}, true, http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
			req := httptest.NewRequest(http.MethodGet, "/chat/threads/t1/ws", nil)
			if tc.ticket {
				req = httptest.NewRequest(http.MethodGet, "/chat/threads/t1/ws?ticket=tkt", nil)
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "websocket")
			} else {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			rec := httptest.NewRecorder()
			Middleware(issuer, redeemer, tc.revs)(next).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

type stubRevocationLister []db.ListSessionRevocationsSinceRow

func (s stubRevocationLister) ListSessionRevocationsSince(context.Context, time.Time) ([]db.ListSessionRevocationsSinceRow, error) {
	return s, nil
}

// TestReplayRevocationsRestoresLostCutoffs covers a Redis that lost (or never
// received) a cutoff recorded in Postgres: the replay writes it back, never
// weakens a stronger entry, and announces nothing, so it cannot drop sockets
// opened by a sign-in after the cutoff.
func TestReplayRevocationsRestoresLostCutoffs(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	d := NewDenylist(rdb, time.Minute)

	signedOut, deleted := uuid.New(), uuid.New()
	cutoff := time.Now().Add(-time.Second).Truncate(time.Microsecond)
	if err := d.Revoke(ctx, deleted); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	lister := stubRevocationLister{
		{ID: signedOut, SessionsRevokedAt: &cutoff},
		{ID: deleted, SessionsRevokedAt: &cutoff},
	}

	announcements := rdb.Subscribe(ctx, revokedChannel)
	t.Cleanup(func() { _ = announcements.Close() })
	if _, err := announcements.Receive(ctx); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	for range 2 {
		if err := d.ReplayRevocations(ctx, lister); err != nil {
			t.Fatalf("replay: %v", err)
		}
	}
	if since, err := d.RevokedSince(ctx, signedOut); err != nil || !since.Equal(cutoff) {
		t.Fatalf("replayed cutoff = %v, %v; want %v", since, err, cutoff)
	}
	if since, err := d.RevokedSince(ctx, deleted); err != nil || !since.Equal(revokeEverything) {
		t.Fatalf("deleted account after replay = %v, %v; want every token revoked", since, err)
	}

	if msg, err := announcements.ReceiveTimeout(ctx, 200*time.Millisecond); err == nil {
		t.Fatalf("unexpected announcement %v: replays must not drop sockets opened after the cutoff", msg)
	}
}

func TestSignInInTheSignOutSecondPasses(t *testing.T) {
	issuer := NewTokenIssuer("test-secret")
	user := uuid.New()
	cutoff := time.Now()
	time.Sleep(time.Millisecond)
	token, _, err := issuer.Issue(user, "a@example.com", RoleUser, ScopeSession, time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	Middleware(issuer, nil, fakeRevocations{since: map[uuid.UUID]time.Time{user: cutoff}})(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}
