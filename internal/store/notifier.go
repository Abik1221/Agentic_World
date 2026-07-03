package store

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// Notifier is a Redis pub/sub wake-up channel for long-polling agents. It
// satisfies match.Notifier (structurally). Notify publishes a tiny message on a
// per-match channel; Subscribe returns a Go channel that fires once per published
// wake-up. It is cross-instance: a move handled on instance A wakes a waiter on
// instance B. Pure signalling — it carries no game state (the waiter re-reads the
// authoritative snapshot), so a dropped or duplicated message only affects latency,
// never correctness (the caller's safety-tick backstop covers a missed wake-up).
type Notifier struct{ rdb *redis.Client }

func NewNotifier(rdb *redis.Client) *Notifier { return &Notifier{rdb: rdb} }

func wakeChannel(matchPublicID string) string { return "match:wake:" + matchPublicID }

// Notify publishes a wake-up for everyone waiting on this match. Best-effort and
// fire-and-forget: a short independent timeout keeps a slow/needed publish from
// ever blocking the match loop, and a publish error is intentionally ignored
// (the subscriber's safety tick still makes progress).
func (n *Notifier) Notify(matchPublicID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = n.rdb.Publish(ctx, wakeChannel(matchPublicID), "1").Err()
}

// Subscribe returns a channel that receives once per wake-up and a cancel func
// that releases the underlying subscription. The pump goroutine exits when cancel
// is called. Sends are non-blocking (buffered, drop-if-full): a wake-up is a
// coalescable signal, not a queue, so the waiter never blocks the pump.
func (n *Notifier) Subscribe(matchPublicID string) (<-chan struct{}, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	ps := n.rdb.Subscribe(ctx, wakeChannel(matchPublicID))
	out := make(chan struct{}, 1)

	go func() {
		defer close(out)
		ch := ps.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				select {
				case out <- struct{}{}:
				default: // a pending wake-up is already queued; coalesce
				}
			}
		}
	}()

	return out, func() {
		cancel()
		_ = ps.Close()
	}
}
