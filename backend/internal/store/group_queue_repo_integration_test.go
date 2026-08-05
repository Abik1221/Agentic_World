package store

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/groupmatch"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These exercise the N-player group queue + the waiting-lobby TTL sweep SQL against a
// REAL Postgres — the queries the group matchmaker and the mafia/monopoly sweepers run
// in production. Skipped unless PYYOL_TEST_DATABASE_URL points at a Postgres (the repo
// has no standing PG harness); the test migrates the schema itself so an empty DB works.

func openGroupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	// DATABASE_URL is accepted as a fallback because that is what CI's database job
	// sets (`DATABASE_URL=... go test -tags=dbtest ./internal/store/...`). Reading only
	// PYYOL_TEST_DATABASE_URL meant every test built on this harness — the model
	// benchmark aggregation, the benchmark API, the docs round trip — SKIPPED in CI
	// while reporting green, so the one environment that has a real Postgres was the
	// one place they never ran.
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL (or DATABASE_URL) to a Postgres DSN to run the integration tests")
	}
	if err := Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// mkUserAgent inserts a user + an agent it owns, returning the agent's public id. A
// unique suffix keeps fixtures from colliding across runs on a reused DB.
func mkUserAgent(t *testing.T, pool *pgxpool.Pool, suffix string) (agentPub, ownerPub string) {
	t.Helper()
	ctx := context.Background()
	ownerPub = "u_" + suffix
	agentPub = "ag_" + suffix
	var uid int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (public_id) VALUES ($1)
		 ON CONFLICT (public_id) DO UPDATE SET updated_at = now() RETURNING id`, ownerPub).Scan(&uid); err != nil {
		t.Fatalf("insert user %s: %v", ownerPub, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO agents (public_id, owner_user_id, name, slug) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (public_id) DO NOTHING`, agentPub, uid, "Agent "+suffix, "slug-"+suffix); err != nil {
		t.Fatalf("insert agent %s: %v", agentPub, err)
	}
	return agentPub, ownerPub
}

func TestGroupQueueRepoIntegration(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	repo := NewGroupQueueRepo(pool)

	// Three distinct-owner agents, unique per run.
	run := time.Now().Format("150405.000")
	aAg, aOw := mkUserAgent(t, pool, "a-"+run)
	bAg, bOw := mkUserAgent(t, pool, "b-"+run)
	cAg, cOw := mkUserAgent(t, pool, "c-"+run)
	for _, ag := range []string{aAg, bAg, cAg} {
		t.Cleanup(func(ag string) func() {
			return func() { _ = repo.Delete(context.Background(), ag) }
		}(ag))
	}

	// Enqueue all three for monopoly @100.
	for _, p := range []struct{ ag, ow string }{{aAg, aOw}, {bAg, bOw}, {cAg, cOw}} {
		if err := repo.Upsert(ctx, groupmatch.Entry{AgentPublicID: p.ag, OwnerPublicID: p.ow, Game: "monopoly", Bid: 100, Elo: 1500}); err != nil {
			t.Fatalf("upsert %s: %v", p.ag, err)
		}
	}

	// WaitingByGame filters by game and orders by (bid, enqueued_at).
	waiting, err := repo.WaitingByGame(ctx, "monopoly", 100)
	if err != nil {
		t.Fatalf("WaitingByGame: %v", err)
	}
	if countAgents(waiting, aAg, bAg, cAg) != 3 {
		t.Fatalf("expected the 3 monopoly agents waiting, got %d of them (%d total rows)", countAgents(waiting, aAg, bAg, cAg), len(waiting))
	}
	if maf, _ := repo.WaitingByGame(ctx, "mafia", 100); countAgents(maf, aAg, bAg, cAg) != 0 {
		t.Fatalf("mafia pool must not see monopoly entries")
	}

	// Get reflects waiting.
	if e, err := repo.Get(ctx, aAg); err != nil || e.Status != groupmatch.StatusWaiting || e.Game != "monopoly" {
		t.Fatalf("Get(a): %+v err=%v", e, err)
	}

	// ClaimGroup is all-or-nothing. Claim {a,b}.
	if ok, err := repo.ClaimGroup(ctx, []string{aAg, bAg}); err != nil || !ok {
		t.Fatalf("ClaimGroup(a,b): ok=%v err=%v", ok, err)
	}
	// A second claim overlapping an already-claimed agent must fail (a is claimed).
	if ok, err := repo.ClaimGroup(ctx, []string{aAg, cAg}); err != nil || ok {
		t.Fatalf("ClaimGroup(a,c) should fail (a already claimed): ok=%v err=%v", ok, err)
	}
	// c must still be waiting (not partially claimed by the failed attempt).
	if e, _ := repo.Get(ctx, cAg); e.Status != groupmatch.StatusWaiting {
		t.Fatalf("c should remain waiting after the failed claim, got %q", e.Status)
	}

	// MarkMatchedGroup flips the claimed pair to matched with the id.
	if err := repo.MarkMatchedGroup(ctx, []string{aAg, bAg}, "gm_1"); err != nil {
		t.Fatalf("MarkMatchedGroup: %v", err)
	}
	if e, _ := repo.Get(ctx, aAg); e.Status != groupmatch.StatusMatched || e.MatchID != "gm_1" {
		t.Fatalf("a should be matched into gm_1, got %+v", e)
	}

	// ReleaseGroup returns a claimed entry to waiting (claim c alone, then release).
	if ok, _ := repo.ClaimGroup(ctx, []string{cAg}); !ok {
		t.Fatal("ClaimGroup(c): expected ok")
	}
	if err := repo.ReleaseGroup(ctx, []string{cAg}); err != nil {
		t.Fatalf("ReleaseGroup(c): %v", err)
	}
	if e, _ := repo.Get(ctx, cAg); e.Status != groupmatch.StatusWaiting {
		t.Fatalf("c should be waiting again after release, got %q", e.Status)
	}

	// Re-enqueue (the autoplay loop) resets a matched agent to a fresh waiting entry.
	if err := repo.Upsert(ctx, groupmatch.Entry{AgentPublicID: aAg, OwnerPublicID: aOw, Game: "mafia", Bid: 250, Elo: 1500}); err != nil {
		t.Fatalf("re-upsert a: %v", err)
	}
	if e, _ := repo.Get(ctx, aAg); e.Status != groupmatch.StatusWaiting || e.MatchID != "" || e.Game != "mafia" || e.Bid != 250 {
		t.Fatalf("re-enqueued a should be freshly waiting for mafia@250, got %+v", e)
	}

	// Delete removes the entry.
	if err := repo.Delete(ctx, aAg); err != nil {
		t.Fatalf("Delete(a): %v", err)
	}
	if _, err := repo.Get(ctx, aAg); err != groupmatch.ErrNotQueued {
		t.Fatalf("deleted agent should be ErrNotQueued, got %v", err)
	}
}

func countAgents(entries []groupmatch.Entry, ids ...string) int {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	n := 0
	for _, e := range entries {
		if want[e.AgentPublicID] {
			n++
		}
	}
	return n
}

// ── end-to-end logical test: enqueue → real matcher loop → table formed → DB matched ──

type recordingCreator struct {
	seats   int
	min     int // 0 ⇒ full-roster-only (MinSeats == SeatTarget)
	groups  [][]groupmatch.Seat
	matchID string
}

func (c *recordingCreator) SeatTarget() int { return c.seats }
func (c *recordingCreator) MinSeats() int {
	if c.min == 0 {
		return c.seats
	}
	return c.min
}
func (c *recordingCreator) CreateStartedTable(_ context.Context, seats []groupmatch.Seat, _ int64) (string, error) {
	c.groups = append(c.groups, seats)
	return c.matchID, nil
}

type fixedElo struct{}

func (fixedElo) Elo(context.Context, string, string) (int, error) { return 1500, nil }

type sysClock struct{}

func (sysClock) Now() time.Time { return time.Now() }

// TestGroupMatchEndToEndLive drives the WHOLE group-matchmaking flow against real
// Postgres: two distinct-owner agents enqueue via the real Service (→ real repo rows),
// the real background Matcher loop pools them, forms a table via the creator, and marks
// both agents 'matched' in the DB. This is the live "does N-player ranked actually
// work end to end" check, not a fake-repo unit test.
func TestGroupMatchEndToEndLive(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	repo := NewGroupQueueRepo(pool)

	run := time.Now().Format("150405.000")
	aAg, aOw := mkUserAgent(t, pool, "e2e-a-"+run)
	bAg, bOw := mkUserAgent(t, pool, "e2e-b-"+run)
	t.Cleanup(func() { _ = repo.Delete(ctx, aAg); _ = repo.Delete(ctx, bAg) })

	creator := &recordingCreator{seats: 2, matchID: "gm_live_" + run}
	svc := groupmatch.New(repo,
		map[string]groupmatch.TableCreator{"monopoly": creator},
		fixedElo{}, sysClock{},
		groupmatch.Config{Interval: 10 * time.Millisecond},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	if _, err := svc.Enqueue(ctx, aAg, aOw, "monopoly", 100); err != nil {
		t.Fatalf("enqueue a: %v", err)
	}
	if _, err := svc.Enqueue(ctx, bAg, bOw, "monopoly", 100); err != nil {
		t.Fatalf("enqueue b: %v", err)
	}

	// Run the real matcher loop briefly (fast interval), then stop.
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { svc.NewMatcher().Run(loopCtx); close(done) }()

	deadline := time.Now().Add(3 * time.Second)
	matched := func(ag string) bool {
		e, err := repo.Get(ctx, ag)
		return err == nil && e.Status == groupmatch.StatusMatched
	}
	for time.Now().Before(deadline) {
		if matched(aAg) && matched(bAg) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	if len(creator.groups) != 1 || len(creator.groups[0]) != 2 {
		t.Fatalf("expected one 2-seat table created, got %v", creator.groups)
	}
	ea, _ := repo.Get(ctx, aAg)
	eb, _ := repo.Get(ctx, bAg)
	if ea.Status != groupmatch.StatusMatched || eb.Status != groupmatch.StatusMatched {
		t.Fatalf("both agents should be matched in the DB, got a=%q b=%q", ea.Status, eb.Status)
	}
	if ea.MatchID != creator.matchID || eb.MatchID != creator.matchID {
		t.Fatalf("both should carry match id %q, got a=%q b=%q", creator.matchID, ea.MatchID, eb.MatchID)
	}
}

// TestNewReadQueriesValidLive exercises every NEW read query added this pass against
// real Postgres (empty result is fine — this proves the SQL is valid, columns exist,
// and the joins/aggregations parse): the per-model latency/cost benchmark, the
// developer token-efficiency + top-models aggregations, the offset-paginated ledger
// history, and the offset-paginated developer matches.
func TestNewReadQueriesValidLive(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()

	rr := NewRatingRepo(pool)
	// Both shapes of the model board: one arena, and the all-arena aggregate whose
	// GROUPING SETS produce the per-arena breakdown rows.
	start, end := time.Now().Add(-30*24*time.Hour), time.Now().Add(24*time.Hour)
	if _, err := rr.ModelBenchmark(ctx, 1, "goofspiel", start, end); err != nil {
		t.Fatalf("ModelBenchmark (single arena): %v", err)
	}
	if _, err := rr.ModelBenchmark(ctx, 1, "", start, end); err != nil {
		t.Fatalf("ModelBenchmark (all arenas): %v", err)
	}
	// AgentStanding both ways: a named arena, and "" for primary-arena resolution.
	// This query referenced a non-existent agent_manifests.agent_id column and so
	// answered 500 for every agent in production; nothing here executed it.
	if _, _, err := rr.AgentStanding(ctx, 1, "goofspiel", "ag_none"); err != nil {
		t.Fatalf("AgentStanding (named arena): %v", err)
	}
	if _, _, err := rr.AgentStanding(ctx, 1, "", "ag_none"); err != nil {
		t.Fatalf("AgentStanding (primary arena): %v", err)
	}
	// The two queries behind the model detail page and the agentic (developer) board.
	if _, err := rr.ModelRunners(ctx, 1, "", "openai", "gpt-4o", start, end); err != nil {
		t.Fatalf("ModelRunners: %v", err)
	}
	if _, err := rr.DeveloperModelSplit(ctx, 1, "", start, end); err != nil {
		t.Fatalf("DeveloperModelSplit: %v", err)
	}
	dp := NewDevProfileRepo(pool)
	if _, _, err := dp.TokenEfficiency(ctx, "u_none"); err != nil {
		t.Fatalf("TokenEfficiency: %v", err)
	}
	if _, err := dp.TopModels(ctx, "u_none", 5); err != nil {
		t.Fatalf("TopModels: %v", err)
	}
	if _, err := dp.RecentMatches(ctx, "u_none", 10, 5); err != nil {
		t.Fatalf("RecentMatches (offset): %v", err)
	}
	lr := NewLedgerRepo(pool)
	if _, err := lr.History(ctx, "ag_none", 10, 5); err != nil {
		t.Fatalf("ledger History (offset): %v", err)
	}
	if _, err := lr.UserHistory(ctx, "u_none", 10, 5); err != nil {
		t.Fatalf("ledger UserHistory (offset): %v", err)
	}
}

// mkWaitingMatch inserts a waiting match row with a controllable created_at, for the
// TTL-sweep SQL. Returns the public id.
func mkWaitingMatch(t *testing.T, pool *pgxpool.Pool, pub, game string, ownerID int64, createdAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO matches (public_id, game, status, bid, engine_version, prize_seed_commit, creator_owner_user_id, created_at)
		 VALUES ($1, $2, 'waiting', 0, 'test', 'test', $3, $4)`, pub, game, ownerID, createdAt); err != nil {
		t.Fatalf("insert waiting match %s: %v", pub, err)
	}
}

func matchStatus(t *testing.T, pool *pgxpool.Pool, pub string) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM matches WHERE public_id = $1`, pub).Scan(&s); err != nil {
		t.Fatalf("status %s: %v", pub, err)
	}
	return s
}

// ExpireStaleWaiting must abort only waiting tables older than the cutoff, leaving
// fresh ones — the waiting-lobby TTL sweep, run for both N-player games.
func TestExpireStaleWaitingIntegration(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()

	var ownerID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (public_id) VALUES ($1)
		 ON CONFLICT (public_id) DO UPDATE SET updated_at = now() RETURNING id`,
		"u_ttl_"+time.Now().Format("150405.000")).Scan(&ownerID); err != nil {
		t.Fatalf("owner: %v", err)
	}
	run := time.Now().Format("150405.000")
	now := time.Now()
	stale := now.Add(-30 * time.Minute)
	fresh := now.Add(-1 * time.Minute)

	cases := []struct {
		game string
		repo interface {
			ExpireStaleWaiting(context.Context, time.Time, int) (int, error)
		}
	}{
		{"mafia", NewMafiaRepo(pool)},
		{"monopoly", NewMonopolyRepo(pool)},
	}
	for _, tc := range cases {
		staleID := "m_stale_" + tc.game + "_" + run
		freshID := "m_fresh_" + tc.game + "_" + run
		mkWaitingMatch(t, pool, staleID, tc.game, ownerID, stale)
		mkWaitingMatch(t, pool, freshID, tc.game, ownerID, fresh)

		cutoff := now.Add(-10 * time.Minute) // stale (-30m) is before it; fresh (-1m) is after
		n, err := tc.repo.ExpireStaleWaiting(ctx, cutoff, 100)
		if err != nil {
			t.Fatalf("%s ExpireStaleWaiting: %v", tc.game, err)
		}
		if n < 1 {
			t.Fatalf("%s: expected at least the stale table aborted, got %d", tc.game, n)
		}
		if got := matchStatus(t, pool, staleID); got != "aborted" {
			t.Fatalf("%s stale table should be aborted, got %q", tc.game, got)
		}
		if got := matchStatus(t, pool, freshID); got != "waiting" {
			t.Fatalf("%s fresh table must stay waiting, got %q", tc.game, got)
		}
		// Cleanup.
		_, _ = pool.Exec(ctx, `DELETE FROM matches WHERE public_id = ANY($1)`, []string{staleID, freshID})
	}
}

// PoolStats is what GET /v1/group-queue reports back to a waiting agent, so its SQL is
// worth exercising against real Postgres: it uses a row comparison for the position and
// a correlated subquery that must yield 0 (not NULL) for an agent who is no longer
// waiting. Both are easy to get wrong in a way unit tests with a fake repo cannot see.
func TestGroupQueuePoolStatsIntegration(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	repo := NewGroupQueueRepo(pool)

	// A game name unique to this run isolates the pool completely, so a reused DB (or a
	// concurrent run) cannot perturb the counts asserted below.
	run := time.Now().Format("150405.000")
	game := "pooltest-" + run

	aAg, aOw := mkUserAgent(t, pool, "ps-a-"+run)
	bAg, bOw := mkUserAgent(t, pool, "ps-b-"+run)
	cAg, _ := mkUserAgent(t, pool, "ps-c-"+run)
	for _, ag := range []string{aAg, bAg, cAg} {
		t.Cleanup(func(ag string) func() {
			return func() { _ = repo.Delete(context.Background(), ag) }
		}(ag))
	}

	// a, then b, then c — where c belongs to b's OWNER. Three waiting agents, two owners:
	// the pool can never seat more than two of them at one table.
	for _, p := range []struct{ ag, ow string }{{aAg, aOw}, {bAg, bOw}, {cAg, bOw}} {
		if err := repo.Upsert(ctx, groupmatch.Entry{
			AgentPublicID: p.ag, OwnerPublicID: p.ow, Game: game, Bid: 500, Elo: 1500,
		}); err != nil {
			t.Fatalf("upsert %s: %v", p.ag, err)
		}
	}

	for i, ag := range []string{aAg, bAg, cAg} {
		ps, err := repo.PoolStats(ctx, game, 500, ag)
		if err != nil {
			t.Fatalf("PoolStats(%s): %v", ag, err)
		}
		if ps.Position != i+1 {
			t.Errorf("%s position = %d, want %d (enqueue order)", ag, ps.Position, i+1)
		}
		if ps.Waiting != 3 {
			t.Errorf("%s pool size = %d, want 3", ag, ps.Waiting)
		}
		if ps.DistinctOwners != 2 {
			t.Errorf("%s distinct owners = %d, want 2 (c shares b's owner)", ag, ps.DistinctOwners)
		}
	}

	// A different bid is a different pool, even in the same game.
	if ps, err := repo.PoolStats(ctx, game, 999, aAg); err != nil {
		t.Fatalf("PoolStats(other bid): %v", err)
	} else if ps.Waiting != 0 || ps.Position != 0 {
		t.Errorf("a different bid must be an empty pool, got %+v", ps)
	}

	// Once claimed, an agent has no position — and the query must return 0 rather than
	// failing to scan a NULL.
	if ok, err := repo.ClaimGroup(ctx, []string{aAg, bAg}); err != nil || !ok {
		t.Fatalf("ClaimGroup: ok=%v err=%v", ok, err)
	}
	ps, err := repo.PoolStats(ctx, game, 500, aAg)
	if err != nil {
		t.Fatalf("PoolStats after claim: %v", err)
	}
	if ps.Position != 0 {
		t.Errorf("a claimed agent has no queue position, got %d", ps.Position)
	}
	if ps.Waiting != 1 || ps.DistinctOwners != 1 {
		t.Errorf("only c should still be waiting, got waiting=%d owners=%d", ps.Waiting, ps.DistinctOwners)
	}
}
