package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/events"
	"github.com/agent-arena/arena/internal/platformsign"
	"github.com/redis/go-redis/v9"
)

// TestEventStreamEndToEnd publishes a signed event through the real Redis stream
// and reads it back, verifying the signature with the engine's public key — the
// exact check the Admin consumer performs.
func TestEventStreamEndToEnd(t *testing.T) {
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Skip("REDIS_TEST_URL not set; skipping live-Redis integration test")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opt)
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("redis unreachable: %v", err)
	}
	rdb.FlushDB(ctx)
	defer rdb.Close()

	engineSeed, enginePub, _ := platformsign.GenerateKeypair()
	signer, _ := platformsign.NewSigner(engineSeed)
	verifier, _ := platformsign.NewVerifier(enginePub)

	stream := NewPlatformEventStream(rdb, signer)
	evt := events.Event{
		ID:        "evt_int_1",
		Type:      events.TypeMatchFinished,
		Payload:   json.RawMessage(`{"match":"m1","winner":"agt_a"}`),
		CreatedAt: time.Unix(1720099200, 0),
	}
	if err := stream.Publish(ctx, evt); err != nil {
		t.Fatal(err)
	}

	msgs, err := rdb.XRange(ctx, "platform:events", "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 stream entry, got %d", len(msgs))
	}
	v := msgs[0].Values
	id, _ := v["id"].(string)
	typ, _ := v["type"].(string)
	payload, _ := v["payload"].(string)
	ts, _ := v["ts"].(string)
	sig, _ := v["sig"].(string)
	ver, _ := v["v"].(string) // envelope schema version — part of the signed input

	if id != "evt_int_1" || typ != events.TypeMatchFinished {
		t.Fatalf("wrong fields on the wire: id=%q type=%q", id, typ)
	}
	if !verifier.Verify(eventSigningInput(id, typ, payload, ts, ver), sig) {
		t.Fatal("consumer-side verification of the published event failed")
	}
	// A tampered payload must fail verification.
	if verifier.Verify(eventSigningInput(id, typ, `{"winner":"attacker"}`, ts, ver), sig) {
		t.Fatal("tampered payload verified — signature does not cover payload")
	}
}
