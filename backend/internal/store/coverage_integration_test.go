package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CoverageFor's denominator, against a REAL Postgres.
//
// # What this pins, and why a unit test could not
//
// The numerator counts DISTINCT bound_decisions.round — the number the TURN PROOF was minted for.
// The denominator must count the same thing, and which decision-log column holds it DIFFERS BY
// GAME: Goofspiel's proof slot is the round (its `seq` is a submission counter that repeats on a
// retry), while Mafia's and Monopoly's proof slot IS their `seq`.
//
// It counted `seq` for every game. So a Goofspiel agent that retried a round had that round
// counted twice in the denominator and once in the numerator. Measured on live matches: six seats
// that bound EVERY round they played reported 76–93% instead of 100%.
//
// The bug lives entirely in one SQL statement's GROUP-BY-ish semantics, so nothing short of a real
// database exercises it — a fake repo would just return whatever the fake was told. And the
// assertion has to be on the NUMBERS rather than on "a query ran", because the broken version ran
// perfectly happily and returned a plausible-looking fraction.
//
// This figure backs the Verified badge, `pyyol doctor` and the developer's own "verified share".
// It does NOT gate settlement — the ranked share rule derives its own denominator from the
// engine's round count (see match.rankedIntegrityFailed) and was never affected.
func TestCoverageDenominatorCountsTheProofSlotPerGame(t *testing.T) {
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
	repo := NewLLMGatewayRepo(pool)

	const (
		goofMatch  = "m_covitest_goof"
		mafiaMatch = "mf_covitest_mafia"
		agentID    = "ag_covitest"
	)
	cleanup := func() {
		for _, m := range []string{goofMatch, mafiaMatch} {
			_, _ = pool.Exec(ctx, `DELETE FROM agent_match_bound_decisions WHERE match_id = $1`, m)
			_, _ = pool.Exec(ctx, `DELETE FROM agent_match_decisions WHERE match_id = $1`, m)
			_, _ = pool.Exec(ctx, `DELETE FROM matches WHERE public_id = $1`, m)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM agents WHERE public_id = $1`, agentID)
	}
	// BEFORE seeding, never after: the inserts below are INSERT..SELECT against agents, so a
	// missing agent writes nothing and reports no error. Cleaning up afterwards only would make
	// a failing run look like a passing one on the next attempt.
	cleanup()
	t.Cleanup(cleanup)

	// A user to own the agent and the matches. Reused if one already exists, so repeated runs do
	// not pile up accounts.
	var ownerID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&ownerID); err != nil {
		t.Skipf("no users in this database to own an agent: %v", err)
	}

	var dbAgentID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO agents (public_id, slug, name, owner_user_id)
		 VALUES ($1, $1, 'coverage itest', $2)
		 RETURNING id`, agentID, ownerID).Scan(&dbAgentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	seedMatch := func(publicID, game string) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`INSERT INTO matches (public_id, game, status, bid, rake_pct, total_rounds,
			     engine_version, prize_seed_commit, fairness_mode, creator_owner_user_id)
			 VALUES ($1, $2, 'finished', 0, 5, 13, 'itest', '', 'shuffled', $3)`,
			publicID, game, ownerID); err != nil {
			t.Fatalf("seed %s match: %v", game, err)
		}
	}
	seedDecision := func(matchID string, seq, round int) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`INSERT INTO agent_match_decisions (match_id, agent_id, seq, round)
			 VALUES ($1, $2, $3, $4)`, matchID, dbAgentID, seq, round); err != nil {
			t.Fatalf("seed decision: %v", err)
		}
	}
	seedBound := func(matchID string, round int) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`INSERT INTO agent_match_bound_decisions (match_id, agent_id, round, extracted_move)
			 VALUES ($1, $2, $3, 'card:7')`, matchID, dbAgentID, round); err != nil {
			t.Fatalf("seed bound: %v", err)
		}
	}

	// --- Goofspiel: three rounds, but round 2 was SUBMITTED TWICE (a retry) --------------------
	//
	// This is the exact shape observed live: `seq` advances per submission while `round` repeats.
	seedMatch(goofMatch, "goofspiel")
	seedDecision(goofMatch, 0, 1)
	seedDecision(goofMatch, 1, 2)
	seedDecision(goofMatch, 2, 2) // the retry — must NOT inflate the denominator
	seedDecision(goofMatch, 3, 3)
	for _, round := range []int{1, 2, 3} {
		seedBound(goofMatch, round)
	}

	got, err := repo.CoverageFor(ctx, agentID, goofMatch)
	if err != nil {
		t.Fatalf("CoverageFor(goofspiel): %v", err)
	}
	if got.Decisions != 3 {
		t.Errorf("goofspiel denominator = %d, want 3 distinct ROUNDS (4 submissions were made, "+
			"one of them a retry of round 2; counting submissions is what understated live "+
			"agents to 76-93%%)", got.Decisions)
	}
	if got.BoundDecisions != 3 {
		t.Errorf("goofspiel numerator = %d, want 3", got.BoundDecisions)
	}
	if got.Coverage != 1.0 {
		t.Errorf("goofspiel coverage = %v, want 1.0 — every round this agent played was bound, so "+
			"anything less tells an honest developer their agent is partly unverified",
			got.Coverage)
	}

	// --- Mafia: seq IS the proof slot, so the denominator must follow seq ----------------------
	//
	// turnproof.MafiaTurn(day, phase) numbers each decision within a day, because a seat acts in
	// both the night and the voting phase. `round` holds only the DAY, so counting rounds here
	// would collapse two real decisions into one and OVERstate coverage.
	seedMatch(mafiaMatch, "mafia")
	seedDecision(mafiaMatch, 16, 1) // day 1, night
	seedDecision(mafiaMatch, 19, 1) // day 1, voting — same day, different decision
	seedBound(mafiaMatch, 16)

	got, err = repo.CoverageFor(ctx, agentID, mafiaMatch)
	if err != nil {
		t.Fatalf("CoverageFor(mafia): %v", err)
	}
	if got.Decisions != 2 {
		t.Errorf("mafia denominator = %d, want 2 — both night and voting are separate proof "+
			"slots on day 1, and counting the DAY would hide one of them", got.Decisions)
	}
	if got.BoundDecisions != 1 {
		t.Errorf("mafia numerator = %d, want 1", got.BoundDecisions)
	}
	if got.Coverage != 0.5 {
		t.Errorf("mafia coverage = %v, want 0.5 (one of two decisions bound)", got.Coverage)
	}
}
