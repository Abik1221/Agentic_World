package store

import (
	"context"
	"errors"

	"github.com/redis/go-redis/v9"
)

// Platform config plane keys/channels — see docs/architecture/platform-config-bus.md.
// The Super Admin writes the snapshot and publishes a version on change; the
// engine (internal/platformcfg) reads the snapshot and treats the channel as a
// pure wake-up.
const (
	keyPlatformConfigSnapshot = "platform:config:snapshot"
	keyPlatformConfigSig      = "platform:config:sig"
	chanPlatformConfigChanged = "platform:config:changed"
)

// PlatformConfigSource is the Redis-backed platformcfg.Source: it reads the
// authoritative config snapshot and subscribes to change signals. It carries no
// state of its own; the snapshot key is the single source of truth.
type PlatformConfigSource struct{ rdb *redis.Client }

// NewPlatformConfigSource wraps the shared Redis client.
func NewPlatformConfigSource(rdb *redis.Client) *PlatformConfigSource {
	return &PlatformConfigSource{rdb: rdb}
}

// LoadSnapshot returns the raw snapshot JSON and its detached Ed25519 signature,
// or (nil, "", nil) when the Super Admin has not published one yet (so the engine
// stays on its defaults). The two keys are written snapshot-then-sig by the
// publisher; a torn read (new snapshot, old sig) simply fails verification and is
// retried on the next signal/tick — never adopted.
func (s *PlatformConfigSource) LoadSnapshot(ctx context.Context) ([]byte, string, error) {
	b, err := s.rdb.Get(ctx, keyPlatformConfigSnapshot).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	sig, err := s.rdb.Get(ctx, keyPlatformConfigSig).Result()
	if errors.Is(err, redis.Nil) {
		sig = "" // unsigned publisher (dev); verification is disabled to match
	} else if err != nil {
		return nil, "", err
	}
	return b, sig, nil
}

// SubscribeChanges returns a channel that fires once per config-change signal and
// a cancel func that releases the subscription. Sends are non-blocking and
// coalescing: the signal only tells the consumer to re-read the snapshot, so a
// dropped or duplicated message affects latency, never correctness. Mirrors
// Notifier.Subscribe.
func (s *PlatformConfigSource) SubscribeChanges(ctx context.Context) (<-chan struct{}, func()) {
	subCtx, cancel := context.WithCancel(ctx)
	ps := s.rdb.Subscribe(subCtx, chanPlatformConfigChanged)
	out := make(chan struct{}, 1)

	go func() {
		defer close(out)
		ch := ps.Channel()
		for {
			select {
			case <-subCtx.Done():
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
