package auth

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestCache(t *testing.T) (*VerificationCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewVerificationCache(rdb, time.Minute), mr
}

func TestVerificationCache_StoreAndGet(t *testing.T) {
	ctx := context.Background()
	cache, _ := newTestCache(t)

	if err := cache.Store(ctx, "user-1", "123456"); err != nil {
		t.Fatalf("store: %v", err)
	}

	code, attempts, err := cache.Get(ctx, "user-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if code != "123456" {
		t.Errorf("code = %q, want %q", code, "123456")
	}
	if attempts != 0 {
		t.Errorf("attempts = %d, want 0", attempts)
	}
}

func TestVerificationCache_GetMissing(t *testing.T) {
	ctx := context.Background()
	cache, _ := newTestCache(t)

	code, attempts, err := cache.Get(ctx, "missing-user")
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if code != "" {
		t.Errorf("code = %q, want empty", code)
	}
	if attempts != 0 {
		t.Errorf("attempts = %d, want 0", attempts)
	}
}

func TestVerificationCache_Expire(t *testing.T) {
	ctx := context.Background()
	cache, mr := newTestCache(t)

	if err := cache.Store(ctx, "user-1", "123456"); err != nil {
		t.Fatalf("store: %v", err)
	}
	mr.FastForward(2 * time.Minute)

	code, _, err := cache.Get(ctx, "user-1")
	if err != nil {
		t.Fatalf("get after expiry: %v", err)
	}
	if code != "" {
		t.Errorf("code still present after expiry: %q", code)
	}
}

func TestVerificationCache_IncrementAttempts(t *testing.T) {
	ctx := context.Background()
	cache, _ := newTestCache(t)

	if err := cache.Store(ctx, "user-1", "123456"); err != nil {
		t.Fatalf("store: %v", err)
	}

	for i := int64(1); i <= 3; i++ {
		count, err := cache.IncrementAttempts(ctx, "user-1")
		if err != nil {
			t.Fatalf("increment %d: %v", i, err)
		}
		if count != i {
			t.Errorf("attempt count after increment %d = %d, want %d", i, count, i)
		}
	}
}

func TestVerificationCache_Invalidate(t *testing.T) {
	ctx := context.Background()
	cache, _ := newTestCache(t)

	if err := cache.Store(ctx, "user-1", "123456"); err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, err := cache.IncrementAttempts(ctx, "user-1"); err != nil {
		t.Fatalf("increment: %v", err)
	}

	if err := cache.Invalidate(ctx, "user-1"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	code, attempts, err := cache.Get(ctx, "user-1")
	if err != nil {
		t.Fatalf("get after invalidate: %v", err)
	}
	if code != "" {
		t.Errorf("code = %q after invalidate", code)
	}
	if attempts != 0 {
		t.Errorf("attempts = %d after invalidate", attempts)
	}
}

func TestRandomCode(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		code, err := randomCode()
		if err != nil {
			t.Fatalf("randomCode: %v", err)
		}
		if len(code) != 6 {
			t.Errorf("code length = %d, want 6", len(code))
		}
		for _, r := range code {
			if r < '0' || r > '9' {
				t.Errorf("code contains non-digit: %q", code)
				break
			}
		}
		if _, ok := seen[code]; ok {
			t.Fatalf("duplicate code generated: %q", code)
		}
		seen[code] = struct{}{}
	}
}
