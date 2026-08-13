package store

// Timing samples must be attributable to the match that produced them.
//
// WHY THIS FILE EXISTS. InsertSample took a matchPublicID and threw it away — literally
// `_ = matchPublicID`, under the comment "match_id stays NULL until the matches table
// exists (Stage 3 resolves it)". The matches table exists, and so does the foreign key
// (agent_timing_samples.fk_timing_match → matches(id)). Thousands of rows were written with
// the column NULL while every caller was supplying a value.
//
// These samples ARE the timing profile that decides whether a HUMAN is playing a match by
// hand. Without a match id, the one question that diagnoses a bad measurement — "what did
// this agent's think-times look like in THAT match?" — cannot be asked at all. A real
// wrong-think-time bug took a live database poll to find for exactly that reason.
//
// Asserted against real SQL because the defect WAS the SQL. A fake repository accepts the
// argument and can be made to look correct while the statement ignores it, which is the
// same reason the round-start guard needed a Postgres test rather than an in-memory one.
//
//	PYYOL_TEST_DATABASE_URL=postgres://… go test ./internal/store/ -run TimingSample

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTimingSampleRecordsTheMatchItCameFrom(t *testing.T) {
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

	repo := &VerificationRepo{db: pool}

	const agentPub = "ag_timing_itest"
	const matchPub = "m_timing_itest"
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM agent_timing_samples WHERE agent_id IN (SELECT id FROM agents WHERE public_id = $1)`, agentPub)
		_, _ = pool.Exec(ctx, `DELETE FROM match_players WHERE match_id IN (SELECT id FROM matches WHERE public_id = $1)`, matchPub)
		_, _ = pool.Exec(ctx, `DELETE FROM matches WHERE public_id = $1`, matchPub)
		_, _ = pool.Exec(ctx, `DELETE FROM agents WHERE public_id = $1`, agentPub)
	}
	cleanup() // before seeding, so a previous run cannot collide
	t.Cleanup(cleanup)

	if _, err := pool.Exec(ctx,
		`INSERT INTO agents (public_id, name, slug, owner_user_id, status)
		 SELECT $1, 'timing-itest', 'timing-itest', (SELECT id FROM users ORDER BY id LIMIT 1), 'active'
		 ON CONFLICT (public_id) DO NOTHING`, agentPub); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	var matchID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO matches (public_id, game, status, mode, bid, rake_pct, total_rounds,
		     engine_version, prize_seed_commit, fairness_mode, creator_owner_user_id)
		 SELECT $1, 'goofspiel', 'active', 'competitive', 0, 5, 13, 'test', 'deadbeef',
		     'commit_reveal', (SELECT id FROM users ORDER BY id LIMIT 1)
		 RETURNING id`, matchPub).Scan(&matchID); err != nil {
		t.Fatalf("seed match: %v", err)
	}

	// ── a sample WITH a match must carry it ───────────────────────────────────────
	mp := matchPub
	if err := repo.InsertSample(ctx, agentPub, &mp, 7500); err != nil {
		t.Fatalf("InsertSample(with match): %v", err)
	}
	var gotMatch *int64
	if err := pool.QueryRow(ctx,
		`SELECT match_id FROM agent_timing_samples
		  WHERE agent_id = (SELECT id FROM agents WHERE public_id = $1) AND response_ms = 7500`,
		agentPub).Scan(&gotMatch); err != nil {
		t.Fatalf("read sample: %v", err)
	}
	if gotMatch == nil {
		t.Fatal("match_id is NULL for a sample recorded WITH a match id — the argument is " +
			"being discarded, so no timing sample can ever be attributed to the match that " +
			"produced it")
	}
	if *gotMatch != matchID {
		t.Fatalf("match_id = %d, want the seeded match %d", *gotMatch, matchID)
	}

	// ── a sample with NO match must still be recorded ──────────────────────────────
	//
	// The response time is the point; the match id is attribution. An inner join here would
	// silently drop real evidence whenever the match lookup missed, which is the same class
	// of quiet loss this fix removes.
	if err := repo.InsertSample(ctx, agentPub, nil, 1234); err != nil {
		t.Fatalf("InsertSample(no match): %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_timing_samples
		  WHERE agent_id = (SELECT id FROM agents WHERE public_id = $1) AND response_ms = 1234`,
		agentPub).Scan(&n); err != nil {
		t.Fatalf("count nil-match sample: %v", err)
	}
	if n != 1 {
		t.Fatalf("nil match id recorded %d rows, want 1 — a lookup miss must not lose a real "+
			"response time from the sample set a fraud control reads", n)
	}

	// ── an UNKNOWN match must also still record ───────────────────────────────────
	unknown := "m_does_not_exist_itest"
	if err := repo.InsertSample(ctx, agentPub, &unknown, 4321); err != nil {
		t.Fatalf("InsertSample(unknown match): %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_timing_samples
		  WHERE agent_id = (SELECT id FROM agents WHERE public_id = $1) AND response_ms = 4321`,
		agentPub).Scan(&n); err != nil {
		t.Fatalf("count unknown-match sample: %v", err)
	}
	if n != 1 {
		t.Fatalf("an unknown match id recorded %d rows, want 1", n)
	}
}
