package platformcfg

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/platformsign"
	"github.com/agent-arena/arena/internal/store"
	"github.com/redis/go-redis/v9"
)

// redisForTest connects to REDIS_TEST_URL or skips. It flushes the DB so each run
// is isolated (use a dedicated test DB, never a real one).
func redisForTest(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Skip("REDIS_TEST_URL not set; skipping live-Redis integration test")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("redis unreachable: %v", err)
	}
	rdb.FlushDB(ctx)
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// TestConfigPlaneEndToEnd drives the real Redis config plane: an Admin-style
// signed write, then the engine's Source + Provider load/verify/adopt, then a
// forged write that must be rejected, then a pub/sub change signal.
func TestConfigPlaneEndToEnd(t *testing.T) {
	rdb := redisForTest(t)
	ctx := context.Background()

	adminSeed, adminPub, _ := platformsign.GenerateKeypair()
	adminSigner, _ := platformsign.NewSigner(adminSeed)
	engineVerifier, _ := platformsign.NewVerifier(adminPub)

	// Admin publishes a signed snapshot (sig-then-snapshot, as the real publisher).
	snap := &Snapshot{Version: 3, Season: &Season{Status: "live", RankedEnabled: true}, Points: Points{Win: 42}}
	payload, _ := json.Marshal(snap)
	if err := rdb.Set(ctx, "platform:config:sig", adminSigner.Sign(payload), 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, "platform:config:snapshot", payload, 0).Err(); err != nil {
		t.Fatal(err)
	}

	p := New(store.NewPlatformConfigSource(rdb), testDefaults(), engineVerifier, discardLog(), time.Hour)
	p.refresh(ctx)
	if got := p.Get(); got.Version != 3 || got.Points.Win != 42 || !got.RankedAllowed() {
		t.Fatalf("engine did not adopt the signed snapshot: %+v", got)
	}

	// A forged snapshot (valid JSON, wrong signature) must be rejected.
	forged, _ := json.Marshal(&Snapshot{Version: 4, Points: Points{Win: 999}})
	rdb.Set(ctx, "platform:config:snapshot", forged, 0)
	rdb.Set(ctx, "platform:config:sig", "AAAA", 0)
	p.refresh(ctx)
	if p.Get().Version != 3 || p.Get().Points.Win != 42 {
		t.Fatalf("forged snapshot must be rejected; still expect v3/win42, got v%d/win%d", p.Get().Version, p.Get().Points.Win)
	}

	// The change channel delivers a wake-up.
	sub := store.NewPlatformConfigSource(rdb)
	ch, cancel := sub.SubscribeChanges(ctx)
	defer cancel()
	time.Sleep(50 * time.Millisecond) // let the subscription establish
	if err := rdb.Publish(ctx, "platform:config:changed", "5").Err(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive config-change signal")
	}
}
