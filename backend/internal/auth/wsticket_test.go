package auth

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
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func newTestTickets(t *testing.T) (*WSTickets, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewWSTickets(rdb, WSTicketTTL), mr
}

func TestWSTicketRedeemsOnceWithPrincipal(t *testing.T) {
	ctx := context.Background()
	tickets, mr := newTestTickets(t)
	want := Principal{UserID: uuid.New(), Email: "a@example.com", Role: RoleUser, Scope: ScopeSession, ExpiresAt: time.Now().Add(time.Minute).UTC().Truncate(time.Second)}

	ticket, err := tickets.Issue(ctx, want)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	for _, key := range mr.Keys() {
		if strings.Contains(key, ticket) {
			t.Fatalf("redis key %q holds the raw ticket", key)
		}
	}
	got, err := tickets.Redeem(ctx, ticket)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if got.UserID != want.UserID || got.Email != want.Email || got.Role != want.Role || got.Scope != want.Scope || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("principal = %+v, want %+v", got, want)
	}
	if _, err := tickets.Redeem(ctx, ticket); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("second redeem err = %v, want ErrInvalidTicket", err)
	}
}

func TestWSTicketRejectsExpiredAndUnknown(t *testing.T) {
	ctx := context.Background()
	tickets, mr := newTestTickets(t)
	ticket, err := tickets.Issue(ctx, Principal{UserID: uuid.New(), Scope: ScopeSession})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	mr.FastForward(WSTicketTTL + time.Second)
	if _, err := tickets.Redeem(ctx, ticket); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("expired ticket err = %v, want ErrInvalidTicket", err)
	}
	if _, err := tickets.Redeem(ctx, "made-up"); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("unknown ticket err = %v, want ErrInvalidTicket", err)
	}

	expired, err := tickets.Issue(ctx, Principal{UserID: uuid.New(), Scope: ScopeSession, ExpiresAt: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := tickets.Redeem(ctx, expired); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("ticket for an expired token err = %v, want ErrInvalidTicket", err)
	}
}

func TestWSTicketRedisDownIsNotInvalid(t *testing.T) {
	tickets, mr := newTestTickets(t)
	mr.Close()
	_, err := tickets.Redeem(context.Background(), "anything")
	if err == nil || errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("err = %v, want a Redis error distinct from ErrInvalidTicket", err)
	}
}

// fakeRedeemer answers Redeem from a fixed map, or with err when set.
type fakeRedeemer struct {
	tickets map[string]Principal
	err     error
}

func (f fakeRedeemer) Redeem(_ context.Context, ticket string) (Principal, error) {
	if f.err != nil {
		return Principal{}, f.err
	}
	p, ok := f.tickets[ticket]
	if !ok {
		return Principal{}, ErrInvalidTicket
	}
	return p, nil
}

func TestMiddlewareTicketOnWebSocketUpgrade(t *testing.T) {
	issuer := NewTokenIssuer("test-secret")
	owner := Principal{UserID: uuid.New(), Role: RoleUser, Scope: ScopeSession}
	good := fakeRedeemer{tickets: map[string]Principal{"good": owner}}
	tests := []struct {
		name     string
		redeemer TicketRedeemer
		ticket   string
		upgrade  bool
		want     int
	}{
		{"valid ticket on upgrade authenticates", good, "good", true, http.StatusNoContent},
		{"unknown ticket is unauthorized", good, "bad", true, http.StatusUnauthorized},
		{"ticket on plain HTTP is ignored", good, "good", false, http.StatusUnauthorized},
		{"redis failure fails closed", fakeRedeemer{err: errors.New("redis down")}, "good", true, http.StatusServiceUnavailable},
		{"tickets disabled ignores the ticket", nil, "good", true, http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var seen Principal
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen, _ = PrincipalFrom(r.Context())
				w.WriteHeader(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodGet, "/chat/threads/t1/ws?ticket="+tc.ticket, nil)
			if tc.upgrade {
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "websocket")
			}
			rec := httptest.NewRecorder()
			Middleware(issuer, tc.redeemer)(next).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want == http.StatusNoContent && seen.UserID != owner.UserID {
				t.Fatalf("principal = %+v, want %+v", seen, owner)
			}
		})
	}
}

func TestWSTicketEndpoint(t *testing.T) {
	tickets, _ := newTestTickets(t)
	svc := NewService(nil, nil, nil, nil, 0, "", nil, nil, &stubPublisher{}, 15*time.Minute, time.Minute, time.Hour)
	h := NewHandler(svc, failOpenLimiter(t))
	h.SetWSTickets(tickets)
	owner := Principal{UserID: uuid.New(), Role: RoleUser, Scope: ScopeSession}
	asScope := func(scope string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p := owner
				p.Scope = scope
				next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
			})
		}
	}

	rec := httptest.NewRecorder()
	h.Routes(asScope(ScopeVerify)).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ws-ticket", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("verify-scoped status = %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.Routes(asScope(ScopeSession)).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ws-ticket", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var body struct {
		Ticket    string `json:"ticket"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ExpiresIn != int(WSTicketTTL/time.Second) {
		t.Fatalf("expires_in = %d", body.ExpiresIn)
	}
	got, err := tickets.Redeem(context.Background(), body.Ticket)
	if err != nil || got.UserID != owner.UserID {
		t.Fatalf("redeem minted ticket = %+v, %v", got, err)
	}
}
