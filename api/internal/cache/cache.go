// Package cache owns the Redis client used for session tokens and rate limits.
package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// Options configures the Redis client.
type Options struct {
	// URL is the connection string. It may carry a password and is never logged.
	URL string
	// ConnectTimeout bounds the initial ping.
	ConnectTimeout time.Duration
}

// Connect opens the client and verifies it with a ping.
func Connect(ctx context.Context, opts Options) (*redis.Client, error) {
	cfg, err := redis.ParseURL(opts.URL)
	if err != nil {
		// The parse error echoes the URL, which may contain a password.
		return nil, errors.New("invalid REDIS_URL")
	}

	cfg.DialTimeout = opts.ConnectTimeout
	cfg.ReadTimeout = 3 * time.Second
	cfg.WriteTimeout = 3 * time.Second

	client := redis.NewClient(cfg)

	pingCtx, cancel := context.WithTimeout(ctx, opts.ConnectTimeout)
	defer cancel()

	if err := client.Ping(pingCtx).Err(); err != nil {
		if closeErr := client.Close(); closeErr != nil {
			return nil, errors.Join(errors.New("redis unreachable"), closeErr)
		}
		return nil, errors.New("redis unreachable")
	}
	return client, nil
}
