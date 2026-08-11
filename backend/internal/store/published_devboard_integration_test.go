package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The developer board is the same policy as the published ladder, one level up.
//
// # The defect this pins
//
// The model board ranks PROVEN decisions and excludes a seat whose model was never verified
// (`no_verified_model`). The developer board — described as the same dataset seen another way —
// ranked whoever held a developer_pindex row. So a developer whose agents never routed one
// proven model call could hold a public rank derived from self-reported attribution, while the
// model board simultaneously refused to name the model they claimed. Two surfaces, two
// definitions of "ranked", and the number was on the weaker one.
//
// # What this pins, and the parts that are easy to get wrong
//
//  1. An unpublished developer is off the board even when their P-Index would put them first.
//  2. Filtering does not touch the COMPUTATION: they keep their P-Index row and value.
//  3. Rank() ranks over the PUBLISHED population, so the global_rank a developer is shown is a
//     position on the board they actually appear on — the lesson SnapshotRanks already learned
//     for the agent ladder.
//  4. Rank() CLEARS a stale rank rather than leaving it out of the UPDATE. Skipping the row is
//     the subtle version of this bug: the rank simply never changes, and a developer keeps a
//     number the board no longer backs.
//  5. The directory's `ranked` flag answers the same question the board does, so a developer is
//     never told they are ranked by one endpoint and omitted by the other.
//
// A unit test cannot reach any of this. The rule is a SQL predicate shared across the board,
// the ranker and the directory, and the failure mode is a well-formed query over the wrong
// POPULATION.
func TestDeveloperBoardExcludesUnpublishedWithoutTouchingPIndex(t *testing.T) {
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL to a migrated Postgres to run this")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Registered FIRST so LIFO ordering closes the pool LAST, after the data cleanups —
	// a cleanup running against a closed pool silently does nothing.
	t.Cleanup(pool.Close)

	const (
		pubUser   = "usr_devboard_published"
		unpubUser = "usr_devboard_unpublished"
		// A season of its own so this test sees only its own two developers no matter what
		// else the database holds.
		season = 990002
	)
	// Agents do NOT cascade from users (agents_owner_user_id_fkey is NO ACTION), so the
	// agents have to go first or the user delete fails. That ordering is load-bearing and
	// was got wrong once: these Execs discard their errors, so a blocked delete leaves the
	// rows behind, the NEXT run trips the users.public_id unique constraint, and the test
	// fails during SEEDING. It still reports failure, so it looks like the guard working —
	// which is the "passes (or fails) for the wrong reason" trap. Errors are checked.
	cleanup := func() {
		t.Helper()
		for _, id := range []string{pubUser, unpubUser} {
			if _, err := pool.Exec(ctx,
				`DELETE FROM agents WHERE owner_user_id = (SELECT id FROM users WHERE public_id = $1)`,
				id); err != nil {
				t.Errorf("cleanup agents for %s: %v", id, err)
			}
			if _, err := pool.Exec(ctx, `DELETE FROM users WHERE public_id = $1`, id); err != nil {
				t.Errorf("cleanup user %s: %v", id, err)
			}
		}
	}
	// BEFORE seeding as well as after: leftovers from a failed run would make the next run's
	// assertions describe the wrong rows.
	cleanup()
	t.Cleanup(cleanup)

	seedDeveloper := func(publicID string, pIndex float64, staleRank int) int64 {
		t.Helper()
		var uid int64
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (public_id, display_name, status, segment)
			 VALUES ($1, 'devboard itest', 'active', 'individual') RETURNING id`,
			publicID).Scan(&uid); err != nil {
			t.Fatalf("seed user %s: %v", publicID, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO developer_pindex (user_id, season, p_index, global_rank, config_version)
			 VALUES ($1, $2, $3, $4, 1)`, uid, season, pIndex, staleRank); err != nil {
			t.Fatalf("seed pindex %s: %v", publicID, err)
		}
		return uid
	}

	seedAgent := func(ownerID int64, publicID string, bound bool) {
		t.Helper()
		var agentID int64
		if err := pool.QueryRow(ctx,
			`INSERT INTO agents (public_id, slug, name, owner_user_id)
			 VALUES ($1, $1, 'devboard itest', $2) RETURNING id`, publicID, ownerID).Scan(&agentID); err != nil {
			t.Fatalf("seed agent %s: %v", publicID, err)
		}
		// What makes an agent published: a call the gateway PROVED belonged to a decision,
		// naming a model. The unpublished developer's agent DID call a model — it just never
		// proved the call belonged to a decision, which is exactly what an agent ignoring the
		// gateway's turn proof produces.
		if _, err := pool.Exec(ctx,
			`INSERT INTO agent_model_calls (agent_id, match_id, round, bound, provider, model, status)
			 VALUES ($1, 'm_devboard_itest', 1, $2, 'anthropic', 'claude-sonnet-5', 200)`,
			agentID, bound); err != nil {
			t.Fatalf("seed model call %s: %v", publicID, err)
		}
	}

	// The UNPUBLISHED developer is deliberately the stronger one, and carries a stale rank 1
	// from before the filter existed. If the board's filter were missing, or applied only to
	// the tail of the page, they take row 1 — so "one row, and it is the published developer"
	// cannot pass by accident. The stale rank makes assertion 4 meaningful.
	unpubID := seedDeveloper(unpubUser, 91.50, 1)
	pubID := seedDeveloper(pubUser, 42.25, 2)
	seedAgent(unpubID, "ag_devboard_unpublished", false)
	seedAgent(pubID, "ag_devboard_published", true)

	devRepo := NewDevProfileRepo(pool)
	pxRepo := NewPIndexRepo(pool)

	// --- 1. The board publishes only the verified developer ------------------------------------

	rows, err := devRepo.Leaderboard(ctx, season, "all", 0, 50, 0)
	if err != nil {
		t.Fatalf("Leaderboard: %v", err)
	}
	if len(rows) != 1 {
		got := make([]string, 0, len(rows))
		for _, r := range rows {
			got = append(got, r.Developer)
		}
		t.Fatalf("developer board has %d rows %v, want exactly 1 — the unpublished developer's "+
			"P-Index outranks the published one, so a missing filter shows up here first",
			len(rows), got)
	}
	if rows[0].Developer != pubUser {
		t.Errorf("published developer = %s, want %s", rows[0].Developer, pubUser)
	}

	// --- 2. The excluded developer keeps their P-Index -----------------------------------------
	//
	// The whole design: filter the publication, never the computation.

	var pIndex float64
	if err := pool.QueryRow(ctx,
		`SELECT p_index FROM developer_pindex WHERE user_id = $1 AND season = $2`,
		unpubID, season).Scan(&pIndex); err != nil {
		t.Fatalf("unpublished developer's P-Index row must survive the filter: %v", err)
	}
	if pIndex != 91.50 {
		t.Errorf("excluded developer's p_index = %v, want 91.50 unchanged — an unpublished "+
			"developer still plays, still earns, and still has a P-Index; it is only unranked",
			pIndex)
	}

	// --- 3 & 4. Rank covers the board's population, and clears a rank it no longer backs -------

	if err := pxRepo.Rank(ctx, season); err != nil {
		t.Fatalf("Rank: %v", err)
	}
	readRank := func(uid int64) (int, float64) {
		t.Helper()
		var rank int
		var pct float64
		if err := pool.QueryRow(ctx,
			`SELECT global_rank, percentile FROM developer_pindex WHERE user_id = $1 AND season = $2`,
			uid, season).Scan(&rank, &pct); err != nil {
			t.Fatalf("read rank: %v", err)
		}
		return rank, pct
	}
	if rank, _ := readRank(pubID); rank != 1 {
		t.Errorf("published developer's global_rank = %d, want 1 — the rank must be a position "+
			"in the population the board draws from, or it names a row nobody can find", rank)
	}
	if rank, pct := readRank(unpubID); rank != 0 || pct != 0 {
		t.Errorf("unpublished developer's global_rank/percentile = %d/%v, want 0/0 — Rank must "+
			"CLEAR a stale rank, not merely skip the row; skipping leaves the developer showing "+
			"a number the board no longer backs", rank, pct)
	}

	// --- 5. The directory agrees with the board about who is ranked ----------------------------
	//
	// Both developers remain DISCOVERABLE — the directory lists people who have never played at
	// all. What must match is the `ranked` claim, not the membership.

	rankedOf := func(publicID string) bool {
		t.Helper()
		row, found, err := devRepo.DirectoryRowFor(ctx, season, publicID)
		if err != nil || !found {
			t.Fatalf("DirectoryRowFor(%s): found=%v err=%v — an excluded developer must still "+
				"be discoverable, only unranked", publicID, found, err)
		}
		return row.Ranked
	}
	if !rankedOf(pubUser) {
		t.Error("published developer reports ranked=false in the directory")
	}
	if rankedOf(unpubUser) {
		t.Error("unpublished developer reports ranked=true in the directory — the profile would " +
			"claim a ranking the leaderboard refuses to show, which is the disagreement the " +
			"shared predicate exists to prevent")
	}
}
