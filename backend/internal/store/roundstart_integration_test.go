package store

// The round-start column, asserted against real SQL.
//
// WHY THIS FILE EXISTS. round_started_at is the origin think-time is measured from, and
// that measurement feeds verification.Record — the timing profile used to decide whether a
// HUMAN is playing a match by hand. It shipped broken twice in one session:
//
//  1. It was written with an unconditional now() inside Advance's UPDATE. commit() calls
//     Advance three times per round (one seat sealed, round finished, next round opened) and
//     only the last opens a round, so the first seat's seal — and every chat message, since
//     trySay commits too — reset the origin. Think-times collapsed from a real 6-10s to
//     19-915ms, and near-zero response times are the strongest possible "not a human" signal.
//
//  2. The unit guard written for the fix asserts on the Advance CALL LOG using an in-memory
//     fake. That fake executes NO SQL, so restoring now() in the UPDATE keeps it green. The
//     bug that actually shipped lives in the statement, and nothing in the suite could see it.
//
// So this test drives the real repository against real Postgres and asserts what the column
// HOLDS after each kind of write. A behavioural assertion, not a structural one: "Advance
// takes a roundStarted parameter" was already true while the SQL ignored it.
//
//	PYYOL_TEST_DATABASE_URL=postgres://… go test ./internal/store/ -run RoundStart

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/match"
)

func TestRoundStartOnlyMovesWhenAdvanceIsGivenOne(t *testing.T) {
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL to a migrated Postgres to run this")
	}
	// Migrate rather than assume: these in-package tests share one database and Go runs
	// files in name order, so this may run before whichever file migrates. Idempotent.
	if err := Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close) // registered first so LIFO runs it after the data cleanups

	repo := NewMatchRepo(pool)

	const matchID = "m_roundstart_itest"
	agents := []string{"ag_rs_itest_a", "ag_rs_itest_b"}
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM match_events WHERE match_id IN (SELECT id FROM matches WHERE public_id = $1)`, matchID)
		_, _ = pool.Exec(ctx, `DELETE FROM match_players WHERE match_id IN (SELECT id FROM matches WHERE public_id = $1)`, matchID)
		_, _ = pool.Exec(ctx, `DELETE FROM matches WHERE public_id = $1`, matchID)
		_, _ = pool.Exec(ctx, `DELETE FROM agents WHERE public_id = ANY($1)`, agents)
	}
	// BEFORE seeding, never after: the seeds use INSERT..SELECT, which writes nothing at all
	// when the referenced row is missing, and fails quietly.
	cleanup()
	t.Cleanup(cleanup)

	for i, pub := range agents {
		if _, err := pool.Exec(ctx,
			`INSERT INTO agents (public_id, name, slug, owner_user_id, status)
			 SELECT $1, $2, $2, (SELECT id FROM users ORDER BY id LIMIT 1), 'active'
			 ON CONFLICT (public_id) DO UPDATE SET name = EXCLUDED.name`,
			pub, "rs-itest-"+string(rune('a'+i))); err != nil {
			t.Fatalf("seed agent %s: %v", pub, err)
		}
	}

	// matches.creator_owner_user_id is NOT NULL and CreatePairedActive resolves it from
	// SeatA.OwnerPublicID, so a Player with no owner fails the insert rather than the
	// assertion — which is how the first run of this test "failed".
	var owner string
	if err := pool.QueryRow(ctx, `SELECT public_id FROM users ORDER BY id LIMIT 1`).Scan(&owner); err != nil {
		t.Fatalf("no user to own the seeded match: %v", err)
	}

	// A live match with a known round start.
	opened := time.Now().UTC().Truncate(time.Millisecond)
	deadline := opened.Add(45 * time.Second)
	if err := repo.CreatePairedActive(ctx, match.CreatePairedInput{
		PublicID: matchID, Game: "goofspiel", Mode: "competitive",
		Bid: 0, RakePct: 5, TotalRounds: 13, EngineVersion: "test", FairnessMode: "commit_reveal",
		Commit: "deadbeef", Seed: []byte("seed"),
		SeatA:    match.Player{AgentPublicID: agents[0], OwnerPublicID: owner, Seat: 0},
		SeatB:    match.Player{AgentPublicID: agents[1], OwnerPublicID: owner, Seat: 1},
		State:    gs.State{Round: 1},
		Deadline: deadline,
	}); err != nil {
		t.Fatalf("CreatePairedActive: %v", err)
	}

	readStart := func(label string) *time.Time {
		var got *time.Time
		if err := pool.QueryRow(ctx,
			`SELECT round_started_at FROM matches WHERE public_id = $1`, matchID).Scan(&got); err != nil {
			t.Fatalf("read round_started_at after %s: %v", label, err)
		}
		return got
	}

	afterCreate := readStart("create")
	if afterCreate == nil {
		t.Fatal("a newly dealt match has no round start — nothing to measure think-time from")
	}

	// ── the defect: a write that opens NO round must not move the origin ──────────
	//
	// This is the seal-one-seat / agent-spoke path. It passes the EXISTING deadline and a
	// nil round start, and the column must be untouched.
	time.Sleep(20 * time.Millisecond) // so a wrongly-stamped now() is distinguishable
	if err := repo.Advance(ctx, matchID, gs.State{Round: 1}, &deadline, nil, nil); err != nil {
		t.Fatalf("Advance(same round): %v", err)
	}
	afterSameRound := readStart("a same-round Advance")
	if afterSameRound == nil {
		t.Fatal("a nil roundStarted erased the round start; it must leave it alone")
	}
	if !afterSameRound.Equal(*afterCreate) {
		t.Fatalf("a same-round Advance moved the round start: %v → %v.\n"+
			"This is the bug that shipped: now() hardcoded in the UPDATE means one seat "+
			"sealing (or any chat message) resets the origin think-time is measured from, "+
			"and the recorded value becomes 'time since the last write'.",
			afterCreate, afterSameRound)
	}

	// ── the new round: an explicit start must be stored exactly as given ──────────
	reopened := time.Now().UTC().Truncate(time.Millisecond)
	nextDeadline := reopened.Add(45 * time.Second)
	if err := repo.Advance(ctx, matchID, gs.State{Round: 2}, &nextDeadline, &reopened, nil); err != nil {
		t.Fatalf("Advance(new round): %v", err)
	}
	afterNewRound := readStart("a new-round Advance")
	if afterNewRound == nil {
		t.Fatal("a new round stored no round start")
	}
	if !afterNewRound.Equal(reopened) {
		t.Fatalf("round start stored as %v, want the value passed in (%v) — the SQL is "+
			"substituting its own clock for the caller's", afterNewRound, reopened)
	}
	if !afterNewRound.Before(nextDeadline) {
		t.Fatalf("round start %v is not before the deadline %v — the deadline was stored as "+
			"the start; a round does not begin a window from now", afterNewRound, nextDeadline)
	}

	// ── and the value survives a read back through the aggregate ──────────────────
	//
	// Writing the column correctly is useless if Get does not select it: the service would
	// see nil and silently fall back to the old reconstruction it was built to replace.
	m, err := repo.Get(ctx, matchID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.RoundStartedAt == nil {
		t.Fatal("Get returned no RoundStartedAt — the column is written but never read, so " +
			"the service falls back to the reconstruction this column exists to replace")
	}
	if !m.RoundStartedAt.Equal(reopened) {
		t.Fatalf("Get returned round start %v, want %v", m.RoundStartedAt, reopened)
	}
}
