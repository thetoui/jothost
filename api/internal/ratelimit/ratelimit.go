// Package ratelimit implements a Redis-backed fixed-window limiter.
package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Result describes the outcome of a limiter check.
type Result struct {
	// Allowed is false once the limit for the window is exhausted.
	Allowed bool
	// Remaining attempts in the current window.
	Remaining int
	// RetryAfter is how long until the window resets. Only meaningful when
	// Allowed is false.
	RetryAfter time.Duration
}

// Limiter enforces a maximum number of attempts per key per window.
//
// A fixed window is used rather than a sliding log: for login throttling the
// worst case (up to 2x the limit across a window boundary) is acceptable, and
// the implementation is two Redis commands with no per-attempt storage growth.
type Limiter struct {
	redis  *redis.Client
	prefix string
	limit  int
	window time.Duration
}

// New builds a Limiter. prefix namespaces the keys, so several limiters can
// share one Redis instance without colliding.
func New(client *redis.Client, prefix string, limit int, window time.Duration) *Limiter {
	return &Limiter{redis: client, prefix: prefix, limit: limit, window: window}
}

// Limit reports the configured maximum.
func (l *Limiter) Limit() int { return l.limit }

// Allow consumes one attempt for key.
//
// A Redis failure returns an error rather than allowing the request. Failing
// closed on the login path is deliberate: an attacker who can disrupt Redis
// must not thereby switch off brute-force protection.
func (l *Limiter) Allow(ctx context.Context, key string) (Result, error) {
	redisKey := l.prefix + key

	pipe := l.redis.TxPipeline()
	incr := pipe.Incr(ctx, redisKey)
	// Set the expiry only when the key is new, so a burst of attempts cannot
	// keep pushing the window out.
	expire := pipe.ExpireNX(ctx, redisKey, l.window)
	if _, err := pipe.Exec(ctx); err != nil {
		return Result{}, fmt.Errorf("rate limit check: %w", err)
	}

	count := incr.Val()
	_ = expire.Val()

	if count > int64(l.limit) {
		ttl, err := l.redis.TTL(ctx, redisKey).Result()
		if err != nil || ttl < 0 {
			ttl = l.window
		}
		return Result{Allowed: false, Remaining: 0, RetryAfter: ttl}, nil
	}

	return Result{Allowed: true, Remaining: l.limit - int(count)}, nil
}

// Reset clears the counter for key, called after a successful login so a user
// who mistyped their password a few times is not throttled afterwards.
func (l *Limiter) Reset(ctx context.Context, key string) error {
	if err := l.redis.Del(ctx, l.prefix+key).Err(); err != nil {
		return fmt.Errorf("reset rate limit: %w", err)
	}
	return nil
}
