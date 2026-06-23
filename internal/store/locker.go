package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"
)

// Locker is a Redis-backed per-key mutual-exclusion lease. It satisfies
// match.Locker (structurally). A unique token per acquisition ensures a holder
// only ever releases its own lock (no accidental release after a TTL expiry +
// re-acquire by someone else).
type Locker struct{ rdb *redis.Client }

func NewLocker(rdb *redis.Client) *Locker { return &Locker{rdb: rdb} }

// releaseScript deletes the key only if it still holds our token.
var releaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

// Lock acquires key for ttl. ok=false (no error) means another holder has it.
func (l *Locker) Lock(ctx context.Context, key string, ttl time.Duration) (func(), bool, error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, false, err
	}
	tok := hex.EncodeToString(token)

	ok, err := l.rdb.SetNX(ctx, key, tok, ttl).Result()
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	release := func() {
		// Best-effort, token-checked release. Use a short independent context so
		// release still runs if the request context was cancelled.
		rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = releaseScript.Run(rctx, l.rdb, []string{key}, tok).Err()
	}
	return release, true, nil
}
