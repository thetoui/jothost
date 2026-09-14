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

// consume counts one attempt and starts the window if none is running, in a
// single atomic step.
//
// This was a MULTI of INCR and EXPIRE ... NX. NX - set the expiry only when
// the key has none, so a burst of attempts cannot keep pushing the window
// out - arrived in Redis 7.0. Ubuntu 22.04 ships Redis 6.0, which rejects the
// option, discards the transaction, and makes every call here an error. The
// limiter fails closed on purpose, so on that release nobody could sign in:
// every login, right password or wrong, answered 500.
//
// The script gives the same meaning on any Redis since 2.6. "No expiry" is
// PTTL == -1, which is exactly the condition NX tests, and it also covers a
// counter that somehow exists without one - the window is started rather
// than the key being left to lock an account out for ever.
var consume = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if redis.call('PTTL', KEYS[1]) == -1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`)

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

	count, err := consume.Run(ctx, l.redis, []string{redisKey}, l.window.Milliseconds()).Int64()
	if err != nil {
		return Result{}, fmt.Errorf("rate limit check: %w", err)
	}

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
