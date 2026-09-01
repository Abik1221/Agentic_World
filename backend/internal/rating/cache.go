package rating

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// A short server-side cache for the two boards that scan the whole benchmark table.
//
// # Why these endpoints needed one
//
// /v1/benchmark/models and /v1/benchmark/developers took 10.6s and 13.8s cold, 4-5s warm.
// Profiling put only ~0.9s of that in the core join, and — the part that decides the fix —
// the season window matched EVERY row: 608k of 608k benchmark rows. So there is no index to
// add. A predicate that selects the whole table is correctly served by a sequential scan, and
// the honest reading is that the query is not slow, it is just large.
//
// Large and repeated. The endpoints already advertised `Cache-Control: max-age=30`, which
// tells a browser the data is stale-tolerant for half a minute but does nothing for the
// server: every cold visitor, every CDN miss and every uncached client paid the full scan
// again. The header was a promise the origin never kept for itself.
//
// # What this does, and the two things it must not do
//
// A value is computed at most once per TTL per distinct parameter set. singleflight collapses
// concurrent misses, so a burst of visitors after expiry produces ONE scan rather than one
// each — which is the failure this is really for. A leaderboard is exactly the shape that
// gets hit by many people at once immediately after something changes.
//
// It must not blur parameters: the key carries game and min_games, because serving one
// arena's board for another is a wrong answer rendered confidently, which is worse than a
// slow one.
//
// It must not cache failures. A transient database error cached for a minute turns one bad
// second into a bad minute, and the endpoints already surface errors properly.
type resultCache[T any] struct {
	ttl   time.Duration
	now   func() time.Time
	sf    singleflight.Group
	mu    sync.RWMutex
	items map[string]cacheItem[T]
}

type cacheItem[T any] struct {
	val T
	at  time.Time
}

func newResultCache[T any](ttl time.Duration) *resultCache[T] {
	return &resultCache[T]{ttl: ttl, now: time.Now, items: map[string]cacheItem[T]{}}
}

// get returns a fresh cached value, or computes one.
//
// The compute call deliberately does NOT inherit the caller's context. Whoever wins the
// singleflight is computing on behalf of everyone waiting, and if that one caller disconnects
// mid-scan the others would all fail with its cancellation — turning a shared optimisation
// into a shared outage. It gets its own bounded context instead.
func (c *resultCache[T]) get(key string, compute func(context.Context) (T, error)) (T, error) {
	c.mu.RLock()
	it, ok := c.items[key]
	c.mu.RUnlock()
	if ok && c.now().Sub(it.at) < c.ttl {
		return it.val, nil
	}

	v, err, _ := c.sf.Do(key, func() (any, error) {
		// Re-check under the flight: a concurrent winner may have just filled it.
		c.mu.RLock()
		it, ok := c.items[key]
		c.mu.RUnlock()
		if ok && c.now().Sub(it.at) < c.ttl {
			return it.val, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		fresh, err := compute(ctx)
		if err != nil {
			return fresh, err // NOT cached
		}
		c.mu.Lock()
		c.items[key] = cacheItem[T]{val: fresh, at: c.now()}
		c.mu.Unlock()
		return fresh, nil
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return v.(T), nil
}
