package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// "Staked but unranked", against a REAL Postgres.
//
// # The policy
//
// An agent that never routes a model call may play staked tables and win coins. It is excluded
// from the ranked SURFACES, because the arena cannot say a model chose its moves. The incentive
// to verify is reputational rather than financial.
//
// # What this pins
//
// Two things, and the second is the one that is easy to get wrong.
//
//  1. An unverified agent does not appear on the published ladder, even when its Elo would put
//     it first. This is not hypothetical: on the lab database at the time of writing, the entire
//     top EIGHT of the Goofspiel ladder was unverified, led by an agent on 1791.
//
//  2. Filtering the ladder does not change any VERIFIED agent's rating. The rejected alternative
//     was to exclude unverified agents from rating altogether, which would have been cleanest
//     conceptually and worst statistically — Glicko/Elo quality depends on a connected
//     comparison graph, and 55 of 75 rated agents here are unverified. So the filter is applied
//     to the SELECT and never to the maths, and this test holds that line: the unverified agent
//     keeps its rating row, its wins and its coins after the ladder has excluded it.
//
// A unit test cannot reach this. The rule is a SQL predicate shared by three statements
// (the board, the rank snapshots and the agent's own standing), and the failure mode is a
// perfectly well-formed query returning the wrong POPULATION.
func TestPublishedLadderExcludesUnverifiedWithoutTouchingRatings(t *testing.T) {
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL to a migrated Postgres to run this")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	repo := NewRatingRepo(pool)

	const (
		verifiedID   = "ag_ladder_verified"
		unverifiedID = "ag_ladder_unverified"
		game         = "goofspiel"
		// A season of its own, so this test sees only its own two agents no matter what else
		// the database holds. Asserting "row 1 is mine" against the shared season would pass or
		// fail on unrelated play.
		season = 990001
	)
	cleanup := func() {
		for _, id := range []string{verifiedID, unverifiedID} {
			_, _ = pool.Exec(ctx, `DELETE FROM rating_rank_snapshots WHERE agent_id IN
				(SELECT id FROM agents WHERE public_id = $1)`, id)
			// agent_model_calls cascades on agent delete; ratings does not.
			_, _ = pool.Exec(ctx, `DELETE FROM ratings WHERE agent_id IN
				(SELECT id FROM agents WHERE public_id = $1)`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM agents WHERE public_id = $1`, id)
		}
	}
	// BEFORE seeding as well as after: leftovers from a failed run would otherwise make the
	// next run's assertions describe the wrong rows.
	cleanup()
	t.Cleanup(cleanup)

	var ownerID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&ownerID); err != nil {
		t.Skipf("no users in this database to own an agent: %v", err)
	}

	seedAgent := func(publicID string, elo int, wins int, coins int64) int64 {
		t.Helper()
		var id int64
		if err := pool.QueryRow(ctx,
			`INSERT INTO agents (public_id, slug, name, owner_user_id)
			 VALUES ($1, $1, 'ladder itest', $2) RETURNING id`, publicID, ownerID).Scan(&id); err != nil {
			t.Fatalf("seed agent %s: %v", publicID, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO ratings (agent_id, game, season, elo, wins, losses, ties, coins_earned)
			 VALUES ($1, $2, $3, $4, $5, 0, 0, $6)`,
			id, game, season, elo, wins, coins); err != nil {
			t.Fatalf("seed rating %s: %v", publicID, err)
		}
		return id
	}

	// The unverified agent is deliberately the STRONGER one. If the filter were missing, or were
	// applied only to the tail of the page, it would take rank 1 — so "the board has one row and
	// it is the verified agent" cannot pass by accident.
	unverifiedDBID := seedAgent(unverifiedID, 1900, 40, 12345)
	verifiedDBID := seedAgent(verifiedID, 1500, 3, 250)

	// What makes an agent published: a call the gateway PROVED belonged to a decision, naming a
	// model. Same fact the model board's `no_verified_model` exclusion reads.
	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_model_calls (agent_id, match_id, round, bound, provider, model, status)
		 VALUES ($1, 'm_ladder_itest', 1, true, 'anthropic', 'claude-sonnet-5', 200)`,
		verifiedDBID); err != nil {
		t.Fatalf("seed bound model call: %v", err)
	}
	// The unverified agent DID call a model — it just never proved the call belonged to a
	// decision. An unbound call is exactly what an agent that ignores the gateway's turn proof
	// produces, and it must not be enough to reach the ladder.
	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_model_calls (agent_id, match_id, round, bound, provider, model, status)
		 VALUES ($1, 'm_ladder_itest', 1, false, 'anthropic', 'claude-sonnet-5', 200)`,
		unverifiedDBID); err != nil {
		t.Fatalf("seed unbound model call: %v", err)
	}

	// --- 1. The board publishes only the verified agent ---------------------------------------

	rows, err := repo.Leaderboard(ctx, game, season, 0, 50)
	if err != nil {
		t.Fatalf("Leaderboard: %v", err)
	}
	if len(rows) != 1 {
		names := make([]string, 0, len(rows))
		for _, r := range rows {
			names = append(names, r.AgentPublicID)
		}
		t.Fatalf("published ladder has %d rows %v, want exactly 1 (the unverified agent outranks "+
			"the verified one on Elo, so a missing filter shows up here first)", len(rows), names)
	}
	if rows[0].AgentPublicID != verifiedID {
		t.Errorf("published agent = %s, want %s", rows[0].AgentPublicID, verifiedID)
	}

	// --- 2. The excluded agent still has its rating, wins and coins ----------------------------
	//
	// The whole design: filter the publication, never the computation.

	var elo, wins int
	var coins int64
	if err := pool.QueryRow(ctx,
		`SELECT elo, wins, coins_earned FROM ratings WHERE agent_id = $1 AND game = $2 AND season = $3`,
		unverifiedDBID, game, season).Scan(&elo, &wins, &coins); err != nil {
		t.Fatalf("unverified agent's rating row must survive the filter: %v", err)
	}
	if elo != 1900 || wins != 40 || coins != 12345 {
		t.Errorf("excluded agent = elo %d, %d wins, %d coins; want 1900/40/12345 unchanged — "+
			"an unverified agent plays staked and wins coins, it is only unpublished", elo, wins, coins)
	}

	// --- 3. The verified agent's own numbers are untouched by the filter -----------------------

	if rows[0].Elo != 1500 {
		t.Errorf("verified agent's published Elo = %d, want 1500 — the filter narrows which rows "+
			"are shown and must never alter one", rows[0].Elo)
	}

	// --- 4. Standing reports rank over the PUBLISHED population, and says which side you are on -

	st, ok, err := repo.AgentStanding(ctx, season, game, verifiedID)
	if err != nil || !ok {
		t.Fatalf("AgentStanding(verified): ok=%v err=%v", ok, err)
	}
	if !st.Ranked {
		t.Error("verified agent reports ranked=false")
	}
	if st.Total != 1 || st.Rank != 1 {
		t.Errorf("verified standing = rank %d of %d, want 1 of 1 — Total must count the published "+
			"population, or a developer is told they are Nth of a ladder that has fewer rows",
			st.Rank, st.Total)
	}

	st, ok, err = repo.AgentStanding(ctx, season, game, unverifiedID)
	if err != nil || !ok {
		t.Fatalf("AgentStanding(unverified): ok=%v err=%v", ok, err)
	}
	if st.Ranked {
		t.Error("unverified agent reports ranked=true — its own card must say it is not published, " +
			"otherwise it shows a rank and then cannot be found on the ladder")
	}
	if st.Elo != 1900 {
		t.Errorf("unverified standing Elo = %d, want 1900: the agent still has a rating", st.Elo)
	}

	// --- 5. Rank snapshots cover the same population as the board ------------------------------
	//
	// The board's `trend` column subtracts yesterday's snapshot rank from today's board rank. If
	// the snapshot were taken over the unfiltered population, every published agent would appear
	// to have climbed by however many unverified agents used to sit above it.

	if _, err := repo.SnapshotRanks(ctx, time.Now()); err != nil {
		t.Fatalf("SnapshotRanks: %v", err)
	}
	var snapshotted int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM rating_rank_snapshots WHERE game = $1 AND season = $2`,
		game, season).Scan(&snapshotted); err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	if snapshotted != 1 {
		t.Errorf("snapshotted %d agents for this season, want 1 — the snapshot must cover the same "+
			"population as the board, or the trend column reports movement nobody made", snapshotted)
	}
}
