package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// wsTicketKeyPrefix namespaces WebSocket tickets in Redis. Keys hold the
// ticket's SHA-256, never the ticket itself, so a Redis dump cannot be replayed.
const wsTicketKeyPrefix = "auth:wsticket:"

// WSTicketTTL bounds how long a ticket can wait between being minted and being
// redeemed by the WebSocket upgrade that follows it.
const WSTicketTTL = 30 * time.Second

// WSTicketsPerMinute caps tickets minted per user per minute: above the app's
// reconnect rate (backoff starts at 1s), far below a Redis-flooding loop.
const WSTicketsPerMinute = 60

// ErrInvalidTicket means a ticket is unknown, already redeemed or expired. The
// three are deliberately indistinguishable to the caller.
var ErrInvalidTicket = errors.New("invalid websocket ticket")

// TicketRedeemer exchanges a WebSocket ticket for the principal it was issued
// to. Middleware depends on the interface so it can be tested without Redis.
type TicketRedeemer interface {
	Redeem(ctx context.Context, ticket string) (Principal, error)
}

// WSTickets mints and redeems single-use, short-lived opaque tickets that let a
// browser WebSocket authenticate without putting its JWT in the URL. A ticket
// that leaks into an access log is already spent or about to expire.
type WSTickets struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewWSTickets returns a ticket store whose tickets live for ttl.
func NewWSTickets(rdb *redis.Client, ttl time.Duration) *WSTickets {
	return &WSTickets{rdb: rdb, ttl: ttl}
}

// ticketPrincipal is the stored form of a Principal.
type ticketPrincipal struct {
	UserID    string    `json:"uid"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Scope     string    `json:"scope"`
	ExpiresAt time.Time `json:"exp"`
	IssuedAt  time.Time `json:"iat"`
}

func ticketKey(ticket string) string {
	sum := sha256.Sum256([]byte(ticket))
	return wsTicketKeyPrefix + hex.EncodeToString(sum[:])
}

// Issue mints a ticket for p. The principal keeps the expiry of the token it
// was authenticated with, so a socket opened with the ticket still closes when
// that token would have expired.
func (t *WSTickets) Issue(ctx context.Context, p Principal) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate websocket ticket: %w", err)
	}
	ticket := base64.RawURLEncoding.EncodeToString(raw)
	payload, err := json.Marshal(ticketPrincipal{UserID: p.UserID.String(), Email: p.Email, Role: p.Role, Scope: p.Scope, ExpiresAt: p.ExpiresAt, IssuedAt: p.IssuedAt})
	if err != nil {
		return "", fmt.Errorf("encode websocket ticket: %w", err)
	}
	if err := t.rdb.Set(ctx, ticketKey(ticket), payload, t.ttl).Err(); err != nil {
		return "", fmt.Errorf("store websocket ticket: %w", err)
	}
	return ticket, nil
}

// Redeem consumes ticket atomically and returns its principal, so a ticket can
// open at most one connection. It returns ErrInvalidTicket for an unknown,
// spent or expired ticket and a wrapped error when Redis is unreachable.
func (t *WSTickets) Redeem(ctx context.Context, ticket string) (Principal, error) {
	payload, err := t.rdb.GetDel(ctx, ticketKey(ticket)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Principal{}, ErrInvalidTicket
	}
	if err != nil {
		return Principal{}, fmt.Errorf("redeem websocket ticket: %w", err)
	}
	var stored ticketPrincipal
	if err := json.Unmarshal(payload, &stored); err != nil {
		return Principal{}, ErrInvalidTicket
	}
	p := Principal{Email: stored.Email, Role: stored.Role, Scope: stored.Scope, ExpiresAt: stored.ExpiresAt, IssuedAt: stored.IssuedAt}
	if err := p.UserID.UnmarshalText([]byte(stored.UserID)); err != nil {
		return Principal{}, ErrInvalidTicket
	}
	if !p.ExpiresAt.IsZero() && !p.ExpiresAt.After(time.Now()) {
		return Principal{}, ErrInvalidTicket
	}
	return p, nil
}
