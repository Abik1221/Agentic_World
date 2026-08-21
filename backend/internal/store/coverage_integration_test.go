package store

// Coverage rollup, tested against a real Postgres.
//
// Skipped unless PYYOL_TEST_DATABASE_URL points at a Postgres; the harness migrates.
//
// The watermark is the dangerous part of this design. Everything else is an aggregate that is
// either right or obviously wrong, but a watermark that advances past a window it did not
// actually cover loses those seats permanently and SILENTLY — the endpoint keeps answering, the
// coverage figure is simply too low forever, and nothing anywhere reports a problem. These tests
// exist for that failure, not for the happy path.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func coveragePool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
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
	return pool, ctx
}

// seedCoverageMatch inserts a finished match with n decisions for one agent.
func seedCoverageMatch(t *testing.T, pool *pgxpool.Pool, ctx context.Context,
	matchID string, agentID int64, finishedAt time.Time, decisions, bound int) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO matches (public_id, game, status, bid, rake_pct, total_rounds,
		                     engine_version, prize_seed_commit, creator_owner_user_id, finished_at)
		VALUES ($1,'goofspiel','finished',0,0,13,'1','covtest',
		        (SELECT id FROM users ORDER BY id LIMIT 1),$2)
		ON CONFLICT (public_id) DO UPDATE SET finished_at = EXCLUDED.finished_at`,
		matchID, finishedAt); err != nil {
		t.Fatalf("seed match: %v", err)
	}
	for i := 0; i < decisions; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO agent_match_decisions (match_id, agent_id, seq, round, action, outcome)
			VALUES ($1,$2,$3,$4,'play','ok') ON CONFLICT DO NOTHING`,
			matchID, agentID, i, i+1); err != nil {
			t.Fatalf("seed decision: %v", err)
		}
	}
	for i := 0; i < bound; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO agent_match_bound_decisions (match_id, agent_id, round)
			VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, matchID, agentID, i+1); err != nil {
			t.Fatalf("seed bound: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_match_coverage WHERE match_id=$1`, matchID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_match_bound_decisions WHERE match_id=$1`, matchID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_match_decisions WHERE match_id=$1`, matchID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM matches WHERE public_id=$1`, matchID)
	})
}

// realAgents returns n existing agent ids. agent_match_decisions has a foreign key, so a
// synthetic id cannot be used and inventing agents would leave rows behind in a shared database.
func realAgents(t *testing.T, pool *pgxpool.Pool, ctx context.Context, n int) []int64 {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT id FROM agents ORDER BY id LIMIT $1`, n)
	if err != nil {
		t.Fatalf("read agents: %v", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan agent: %v", err)
		}
		out = append(out, id)
	}
	if len(out) < n {
		t.Skipf("need %d agents in the test database, found %d", n, len(out))
	}
	return out
}

func resetWatermark(t *testing.T, pool *pgxpool.Pool, ctx context.Context, to time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_match_coverage_watermark (id, covered_through, updated_at)
		VALUES (1,$1,now()) ON CONFLICT (id) DO UPDATE SET covered_through=EXCLUDED.covered_through`,
		to); err != nil {
		t.Fatalf("reset watermark: %v", err)
	}
}

// TestCoverageRefreshCountsExactly. The rollup replaced a live aggregate, so it has to produce
// the same number — a fast wrong answer here is worse than the slow right one it replaced.
func TestCoverageRefreshCountsExactly(t *testing.T) {
	pool, ctx := coveragePool(t)
	repo := NewCoverageRepo(pool)

	base := time.Now().Add(-3 * time.Hour).UTC()
	ag := realAgents(t, pool, ctx, 1)[0]
	seedCoverageMatch(t, pool, ctx, "m_covtest_exact", ag, base, 11, 7)
	resetWatermark(t, pool, ctx, base.Add(-time.Minute))

	if _, err := repo.Refresh(ctx, 24*time.Hour); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	var logged, bound int64
	if err := pool.QueryRow(ctx,
		`SELECT logged_decisions, bound_decisions FROM agent_match_coverage
		  WHERE match_id='m_covtest_exact' AND agent_id=$1`, ag).Scan(&logged, &bound); err != nil {
		t.Fatalf("read rollup: %v", err)
	}
	if logged != 11 || bound != 7 {
		t.Fatalf("rollup says logged=%d bound=%d, want 11 and 7", logged, bound)
	}
}

// TestCoverageWatermarkDoesNotSkipMatches is the one that matters.
//
// A batch bound means one refresh covers only part of the history. If the watermark advances by
// the full batch regardless of what was actually scanned, every match beyond the batch is skipped
// forever — the endpoint still answers, the coverage figure is just permanently too low, and
// nothing surfaces it. Seeds three matches spread wider than one batch, refreshes repeatedly with
// a deliberately small batch, and requires every seat to arrive.
func TestCoverageWatermarkDoesNotSkipMatches(t *testing.T) {
	pool, ctx := coveragePool(t)
	repo := NewCoverageRepo(pool)

	base := time.Now().Add(-10 * time.Hour).UTC()
	ids := []string{"m_covtest_w1", "m_covtest_w2", "m_covtest_w3"}
	ags := realAgents(t, pool, ctx, 3)
	for i, id := range ids {
		seedCoverageMatch(t, pool, ctx, id, ags[i], base.Add(time.Duration(i)*3*time.Hour), 5, 3)
	}
	resetWatermark(t, pool, ctx, base.Add(-time.Minute))

	// Batch far smaller than the span, so it must take several passes.
	for i := 0; i < 20; i++ {
		if _, err := repo.Refresh(ctx, time.Hour); err != nil {
			t.Fatalf("refresh %d: %v", i, err)
		}
	}
	for i, id := range ids {
		var n int64
		if err := pool.QueryRow(ctx,
			`SELECT logged_decisions FROM agent_match_coverage WHERE match_id=$1 AND agent_id=$2`,
			id, ags[i]).Scan(&n); err != nil {
			t.Fatalf("match %s never reached the rollup — the watermark advanced past a window "+
				"it had not covered, and those seats are lost silently: %v", id, err)
		}
		if n != 5 {
			t.Fatalf("match %s logged=%d, want 5", id, n)
		}
	}
}

// TestCoverageRefreshIsIdempotent. The worker re-runs on a timer and a failed tick repeats its
// window, so a second pass over the same range must not double-count.
func TestCoverageRefreshIsIdempotent(t *testing.T) {
	pool, ctx := coveragePool(t)
	repo := NewCoverageRepo(pool)

	base := time.Now().Add(-2 * time.Hour).UTC()
	ag := realAgents(t, pool, ctx, 1)[0]
	seedCoverageMatch(t, pool, ctx, "m_covtest_idem", ag, base, 9, 4)

	for i := 0; i < 3; i++ {
		resetWatermark(t, pool, ctx, base.Add(-time.Minute))
		if _, err := repo.Refresh(ctx, 24*time.Hour); err != nil {
			t.Fatalf("refresh %d: %v", i, err)
		}
	}
	var logged int64
	if err := pool.QueryRow(ctx,
		`SELECT logged_decisions FROM agent_match_coverage
		  WHERE match_id='m_covtest_idem' AND agent_id=$1`, ag).Scan(&logged); err != nil {
		t.Fatalf("read rollup: %v", err)
	}
	if logged != 9 {
		t.Fatalf("logged=%d after three refreshes, want 9 — the refresh is accumulating rather "+
			"than replacing, so every retry inflates a published figure", logged)
	}
}
