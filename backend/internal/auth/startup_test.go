package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestStartupReplayPreparesFreshRedis(t *testing.T) {
	ctx := context.Background()
	cache := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: cache.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	deny := NewDenylist(rdb, time.Hour)
	user := uuid.New()
	if _, err := deny.RevokedSince(ctx, user); !errors.Is(err, ErrRevocationUnavailable) {
		t.Fatalf("fresh cache: %v", err)
	}
	ReplayRevocationsOnStartup(ctx, "rabbitmq", deny, stubRevocationLister{})
	if _, err := deny.RevokedSince(ctx, user); !errors.Is(err, ErrRevocationUnavailable) {
		t.Fatalf("RabbitMQ must leave replay to its existing loop: %v", err)
	}
	ReplayRevocationsOnStartup(ctx, "pubsub", deny, stubRevocationLister{})
	if since, err := deny.RevokedSince(ctx, user); err != nil || !since.IsZero() {
		t.Fatalf("startup cache readiness: since=%v err=%v", since, err)
	}
	cache.FlushAll()
	_ = rdb.Close()
	ReplayRevocationsOnStartup(ctx, "pubsub", deny, stubRevocationLister{})
}
