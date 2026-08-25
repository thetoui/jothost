package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/testsupport"
)

func newLimiter(t *testing.T, limit int, window time.Duration) (*Limiter, context.Context) {
	t.Helper()
	deps := testsupport.Require(t)
	return New(deps.Redis, "test:rl:", limit, window), context.Background()
}

func TestAllowsUpToTheLimit(t *testing.T) {
	limiter, ctx := newLimiter(t, 3, time.Minute)

	for i := 1; i <= 3; i++ {
		result, err := limiter.Allow(ctx, "key")
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if !result.Allowed {
			t.Fatalf("attempt %d must be allowed", i)
		}
		if want := 3 - i; result.Remaining != want {
			t.Fatalf("attempt %d: remaining = %d, want %d", i, result.Remaining, want)
		}
	}
}

func TestBlocksBeyondTheLimit(t *testing.T) {
	limiter, ctx := newLimiter(t, 2, time.Minute)

	for i := 0; i < 2; i++ {
		if _, err := limiter.Allow(ctx, "key"); err != nil {
			t.Fatalf("Allow: %v", err)
		}
	}

	result, err := limiter.Allow(ctx, "key")
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if result.Allowed {
		t.Fatal("the attempt beyond the limit must be blocked")
	}
	if result.Remaining != 0 {
		t.Fatalf("remaining = %d, want 0", result.Remaining)
	}
	if result.RetryAfter <= 0 || result.RetryAfter > time.Minute {
		t.Fatalf("RetryAfter = %v, want a value within the window", result.RetryAfter)
	}
}

func TestKeysAreIndependent(t *testing.T) {
	limiter, ctx := newLimiter(t, 1, time.Minute)

	if _, err := limiter.Allow(ctx, "alice"); err != nil {
		t.Fatalf("Allow: %v", err)
	}

	// Exhausting one account's budget must not throttle another.
	result, err := limiter.Allow(ctx, "bob")
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !result.Allowed {
		t.Fatal("a different key must have its own budget")
	}
}

func TestPrefixesAreIndependent(t *testing.T) {
	deps := testsupport.Require(t)
	ctx := context.Background()

	userLimiter := New(deps.Redis, "test:rl:user:", 1, time.Minute)
	ipLimiter := New(deps.Redis, "test:rl:ip:", 1, time.Minute)

	if _, err := userLimiter.Allow(ctx, "same"); err != nil {
		t.Fatalf("Allow: %v", err)
	}

	result, err := ipLimiter.Allow(ctx, "same")
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !result.Allowed {
		t.Fatal("limiters with different prefixes must not share counters")
	}
}

func TestResetClearsTheCounter(t *testing.T) {
	limiter, ctx := newLimiter(t, 1, time.Minute)

	if _, err := limiter.Allow(ctx, "key"); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if result, err := limiter.Allow(ctx, "key"); err != nil || result.Allowed {
		t.Fatalf("expected the second attempt to be blocked (err=%v)", err)
	}

	if err := limiter.Reset(ctx, "key"); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	result, err := limiter.Allow(ctx, "key")
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !result.Allowed {
		t.Fatal("the counter must be clear after Reset")
	}
}

func TestWindowExpires(t *testing.T) {
	limiter, ctx := newLimiter(t, 1, time.Second)

	if _, err := limiter.Allow(ctx, "key"); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if result, _ := limiter.Allow(ctx, "key"); result.Allowed {
		t.Fatal("the second attempt in the window must be blocked")
	}

	// The window must actually elapse, or a lockout would be permanent.
	time.Sleep(1200 * time.Millisecond)

	result, err := limiter.Allow(ctx, "key")
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !result.Allowed {
		t.Fatal("a new window must grant a fresh budget")
	}
}

func TestBurstDoesNotExtendTheWindow(t *testing.T) {
	limiter, ctx := newLimiter(t, 2, time.Second)

	// Hammering the key must not keep pushing the expiry out, which would
	// make the lockout last as long as the attacker keeps trying.
	for i := 0; i < 10; i++ {
		if _, err := limiter.Allow(ctx, "key"); err != nil {
			t.Fatalf("Allow: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	time.Sleep(700 * time.Millisecond)

	result, err := limiter.Allow(ctx, "key")
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !result.Allowed {
		t.Fatal("the window must expire on schedule regardless of attempt volume")
	}
}

func TestLimitAccessor(t *testing.T) {
	limiter, _ := newLimiter(t, 7, time.Minute)
	if limiter.Limit() != 7 {
		t.Fatalf("Limit() = %d, want 7", limiter.Limit())
	}
}
