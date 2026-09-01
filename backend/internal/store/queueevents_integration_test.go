package store

// The queue funnel, against real SQL.
//
// These queries exist to answer questions the live queue tables cannot — "how many agents
// waited and never got a game", "how many went unreachable after their developer started
// them", "where do agents get stuck". They are CTEs with FILTER aggregates and a NOT EXISTS
// correlated on time, which is exactly the kind of SQL that returns a plausible number for
// the wrong reason. A fake cannot check any of it.
//
//	PYYOL_TEST_DATABASE_URL=postgres://… go test ./internal/store/ -run QueueFunnel

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestQueueFunnelSeparatesMatchedFromNeverMatched(t *testing.T) {
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL to a migrated Postgres to run this")
	}
	if err := Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	repo := NewQueueEventsRepo(pool, nil)

	// Three agents covering the three outcomes that matter.
	lucky := "ag_qf_lucky" // enqueued → matched
	stuck := "ag_qf_stuck" // enqueued, never matched — the number nothing else can produce
	dodgy := "ag_qf_dodgy" // enqueued → asked → dropped → requeued → matched
	agents := []string{lucky, stuck, dodgy}

	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM queue_events WHERE agent_id IN (SELECT id FROM agents WHERE public_id = ANY($1))`, agents)
		_, _ = pool.Exec(ctx, `DELETE FROM agents WHERE public_id = ANY($1)`, agents)
	}
	cleanup()
	t.Cleanup(cleanup)

	for i, pub := range agents {
		if _, err := pool.Exec(ctx,
			`INSERT INTO agents (public_id, name, slug, owner_user_id, status)
			 SELECT $1, $2, $2, (SELECT id FROM users ORDER BY id LIMIT 1), 'active'
			 ON CONFLICT (public_id) DO NOTHING`, pub, "qf-itest-"+string(rune('a'+i))); err != nil {
			t.Fatalf("seed agent %s: %v", pub, err)
		}
	}

	rec := func(agent, kind string, waited time.Duration) {
		repo.Record(ctx, QueueEvent{
			AgentPublicID: agent, Game: "goofspiel", Queue: QueueTwoPlayer,
			Kind: kind, Waited: waited,
		})
	}

	rec(lucky, QueueEnqueued, 0)
	rec(lucky, QueueMatched, 4*time.Second)

	rec(stuck, QueueEnqueued, 0) // and nothing else, ever

	// The ordering that breaks a naive query: this agent has an enqueue with no match, THEN a
	// later enqueue that does match. Counting "enqueued without any match in the window" would
	// call it never-matched, which is wrong — it got a game in the end.
	rec(dodgy, QueueEnqueued, 0)
	rec(dodgy, QueueReadyAsked, 0)
	rec(dodgy, QueueDropped, 0)
	rec(dodgy, QueueRequeued, 0)
	rec(dodgy, QueueEnqueued, 0)
	rec(dodgy, QueueMatched, 30*time.Second)

	f, err := repo.Funnel(ctx, time.Hour)
	if err != nil {
		t.Fatalf("Funnel: %v", err)
	}

	// Only assert on this test's own rows where possible; the lab database has other traffic,
	// so absolute totals are lower bounds rather than equalities.
	if f.Enqueued < 4 {
		t.Errorf("enqueued = %d, want at least the 4 recorded here", f.Enqueued)
	}
	if f.Matched < 2 {
		t.Errorf("matched = %d, want at least 2", f.Matched)
	}
	if f.Dropped < 1 {
		t.Errorf("dropped = %d, want at least 1 — this is the unreachable-after-start count", f.Dropped)
	}

	// The load-bearing assertion. `stuck` must be counted and `dodgy` must NOT be: a requeued
	// agent that eventually paired is not a never-matched agent, and conflating them inflates
	// the one number a developer would escalate over.
	neverMatched, err := neverMatchedFor(ctx, pool, agents)
	if err != nil {
		t.Fatalf("per-agent never-matched: %v", err)
	}
	if !neverMatched[stuck] {
		t.Error("the agent that only ever enqueued was not counted as never-matched — " +
			"this is the one question the live queue tables cannot answer at all")
	}
	if neverMatched[dodgy] {
		t.Error("an agent that was dropped, requeued and THEN matched was counted as " +
			"never-matched; the NOT EXISTS must be correlated on the LAST enqueue, not the window")
	}
	if neverMatched[lucky] {
		t.Error("a straightforwardly matched agent was counted as never-matched")
	}

	// Waits are over MATCHED events only. Mixing unmatched entries in would report a wait for
	// a thing that never happened.
	if f.WaitP50Ms <= 0 {
		t.Errorf("wait p50 = %d, want a positive measured wait", f.WaitP50Ms)
	}

	// Per-owner breakdown must attribute all three (they share the seeded owner).
	rows, err := repo.ByOwner(ctx, time.Hour, 50)
	if err != nil {
		t.Fatalf("ByOwner: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("ByOwner returned nothing despite recorded events with an owner")
	}
	var sawEnqueued bool
	for _, r := range rows {
		if r.Enqueued > 0 {
			sawEnqueued = true
		}
		if r.OwnerPublicID == "" {
			t.Error("a per-owner row has no owner public id; the join dropped attribution")
		}
	}
	if !sawEnqueued {
		t.Error("no per-owner row shows any enqueue — the FILTER aggregates are not matching")
	}
}

// neverMatchedFor applies the same rule as Funnel, per agent, so the test can assert WHICH
// agents were counted rather than only how many. A count alone would pass with the right total
// reached the wrong way.
func neverMatchedFor(ctx context.Context, pool *pgxpool.Pool, agents []string) (map[string]bool, error) {
	rows, err := pool.Query(ctx,
		`WITH last_enqueue AS (
		     SELECT e.agent_id, a.public_id, max(e.created_at) AS at
		       FROM queue_events e JOIN agents a ON a.id = e.agent_id
		      WHERE e.kind = 'enqueued' AND a.public_id = ANY($1)
		      GROUP BY e.agent_id, a.public_id
		 )
		 SELECT le.public_id, NOT EXISTS (
		     SELECT 1 FROM queue_events m
		      WHERE m.agent_id = le.agent_id AND m.kind = 'matched' AND m.created_at >= le.at
		 ) FROM last_enqueue le`, agents)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var pub string
		var never bool
		if err := rows.Scan(&pub, &never); err != nil {
			return nil, err
		}
		out[pub] = never
	}
	return out, rows.Err()
}
