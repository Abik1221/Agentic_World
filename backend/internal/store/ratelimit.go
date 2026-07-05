package store

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RateLimiter is a Redis fixed-window rate limiter. It satisfies
// middleware.Limiter (structurally — no import needed). A fixed window is cheap
// and sufficient for coarse protections like registration (5/hour/IP); hot-path
// limits can later swap to a sliding-window ZSET behind the same interface.
type RateLimiter struct {
	rdb *redis.Client
}

func NewRateLimiter(rdb *redis.Client) *RateLimiter { return &RateLimiter{rdb: rdb} }

// allowScript atomically increments the counter, sets the TTL on first hit, and
// returns {count, pttl_ms} so the caller can decide and compute Retry-After.
var allowScript = redis.NewScript(`
local c = redis.call('INCR', KEYS[1])
if c == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
local ttl = redis.call('PTTL', KEYS[1])
return {c, ttl}
`)

// Allow implements middleware.Limiter.
func (r *RateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	res, err := allowScript.Run(ctx, r.rdb, []string{key}, window.Milliseconds()).Slice()
	if err != nil {
		return false, 0, err
	}
	count, _ := res[0].(int64)
	ttlMS, _ := res[1].(int64)
	retryAfter := time.Duration(ttlMS) * time.Millisecond
	if int(count) > limit {
		return false, retryAfter, nil
	}
	return true, 0, nil
}
