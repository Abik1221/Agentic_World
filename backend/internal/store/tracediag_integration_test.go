package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DIAGNOSTIC, not an assertion suite.
//
// Prints exactly what a developer's match trace contains for a realistic match in each
// arena, so a claim about what the trace UI is missing can be checked against the
// database instead of guessed at from the rendering code. Run with -v.
//
//	PYYOL_TEST_DATABASE_URL=… go test ./internal/store -run TestTraceDiagnostic -v

func seedTracedMatch(t *testing.T, pool *pgxpool.Pool, game, matchPub string, agentPub string, ownerID int64, events []struct {
	Type    string
	Payload map[string]any
}) {
	t.Helper()
	ctx := context.Background()
	var mid int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO matches (public_id, game, status, bid, engine_version, prize_seed_commit,
		                      creator_owner_user_id, started_at, finished_at)
		 VALUES ($1,$2,'finished',100,'diag','diag',$3, now() - interval '3 minutes', now())
		 ON CONFLICT (public_id) DO UPDATE SET finished_at = now() RETURNING id`,
		matchPub, game, ownerID).Scan(&mid); err != nil {
		t.Fatalf("match: %v", err)
	}
	var aid int64
	if err := pool.QueryRow(ctx, `SELECT id FROM agents WHERE public_id = $1`, agentPub).Scan(&aid); err != nil {
		t.Fatalf("agent: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO match_players (match_id, agent_id, owner_user_id, seat)
		 VALUES ($1,$2,$3,0) ON CONFLICT DO NOTHING`, mid, aid, ownerID); err != nil {
		t.Fatalf("seat: %v", err)
	}
	for i, e := range events {
		raw, _ := json.Marshal(e.Payload)
		if _, err := pool.Exec(ctx,
			`INSERT INTO match_events (match_id, seq, type, payload) VALUES ($1,$2,$3,$4::jsonb)
			 ON CONFLICT (match_id, seq) DO NOTHING`, mid, i+1, e.Type, string(raw)); err != nil {
			t.Fatalf("event %d (%s): %v", i, e.Type, err)
		}
	}
}

func TestTraceDiagnostic(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	_, run := isolate()

	agent, owner := mkUserAgent(t, pool, "trace-"+run)
	var ownerID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE public_id = $1`, owner).Scan(&ownerID); err != nil {
		t.Fatalf("owner: %v", err)
	}

	type ev = struct {
		Type    string
		Payload map[string]any
	}

	// A goofspiel match the way the drive loop actually writes one: a round result, the
	// agent's rationale relayed as a chat event, and the finish.
	seedTracedMatch(t, pool, "goofspiel", "m_diag_goof_"+run, agent, ownerID, []ev{
		{"match_created", map[string]any{"players": 2}},
		{"round_revealed", map[string]any{"round": 1, "prize": 11, "cards": []int{9, 4}, "winner": 0}},
		{"agent_says", map[string]any{"round": 1, "seat": 0, "kind": "rationale", "text": "the 11 is worth overpaying for"}},
		{"round_revealed", map[string]any{"round": 2, "prize": 3, "cards": []int{1, 8}, "winner": 1}},
		{"agent_says", map[string]any{"round": 2, "seat": 0, "kind": "rationale", "text": "dumping my lowest on a cheap prize"}},
		{"agent_says", map[string]any{"round": 2, "seat": 0, "kind": "say", "text": "nice one"}},
		{"match_finished", map[string]any{"scores": []int{11, 3}, "winner": 0}},
	})

	// A mafia match, using the event TYPES AND PAYLOAD SHAPES the engine really emits
	// (internal/engine/mafia/events.go: phase/message/vote/eliminate/victory) rather
	// than invented ones — a fixture that does not match the producer proves nothing
	// about what a developer sees.
	//
	// Note what is absent and cannot be added here: the mafia push loop records each
	// decision's rationale into the benchmark Recorder and emits a Lens DecisionEvent,
	// but writes no rationale event to Postgres. There is no event type to seed.
	seedTracedMatch(t, pool, "mafia", "m_diag_mafia_"+run, agent, ownerID, []ev{
		{"phase", map[string]any{"phase": "day", "day": 1}},
		{"message", map[string]any{"from": 0, "tone": "accuse", "text": "seat 3 was quiet all night", "target": 3}},
		{"vote", map[string]any{"from": 0, "target": 3}},
		{"eliminate", map[string]any{"target": 3, "cause": "vote"}},
		{"victory", map[string]any{"winner": "town"}},
	})

	for _, m := range []string{"m_diag_goof_" + run, "m_diag_mafia_" + run} {
		rows, err := pool.Query(ctx,
			`SELECT me.seq, me.type, me.payload::text
			   FROM match_events me JOIN matches mm ON mm.id = me.match_id
			  WHERE mm.public_id = $1 ORDER BY me.seq`, m)
		if err != nil {
			t.Fatalf("read %s: %v", m, err)
		}
		t.Logf("── %s ──", m)
		for rows.Next() {
			var seq int
			var typ, payload string
			if err := rows.Scan(&seq, &typ, &payload); err != nil {
				t.Fatal(err)
			}
			t.Logf("   %2d  %-18s %s", seq, typ, payload)
		}
		rows.Close()
	}

	// What the per-decision benchmark fact holds for the SAME match — the token, latency
	// and outcome detail that never becomes a trace row.
	t.Logf("── what agent_match_benchmark stores per (match, agent) ──")
	cols, err := pool.Query(ctx,
		`SELECT column_name, data_type FROM information_schema.columns
		  WHERE table_name = 'agent_match_benchmark' ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	defer cols.Close()
	var names []string
	for cols.Next() {
		var n, d string
		if err := cols.Scan(&n, &d); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	t.Logf("   %s", fmt.Sprint(names))

	// And whether ANY table holds a per-ROUND record with reasoning attached.
	var perRound int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM information_schema.columns
		  WHERE column_name IN ('rationale','reasoning') AND table_schema = 'public'`).Scan(&perRound); err != nil {
		t.Fatal(err)
	}
	t.Logf("── columns named rationale/reasoning anywhere in the schema: %d ──", perRound)
	_ = time.Now
}
