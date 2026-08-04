//go:build dbtest

// Round-trip tests against a REAL, migrated Postgres.
//
// WHY THIS FILE EXISTS. MatchUsageRepo.OwnedBy joined `agents a JOIN users u ON
// u.id = a.user_id`. There is no `user_id` on agents and never has been — migration 0002
// creates `owner_user_id` — so that query failed with "column a.user_id does not exist" on
// every call, and GET /v1/matches/{id}/usage answered 500 for its entire life. Nothing in
// the product read it, so nothing reported it.
//
// No amount of unit testing with a fake repo can catch that: the SQL is only checked when
// Postgres parses it. The project's own handover notes the lesson — "anything touching the
// DB needs a real round trip: write as production writes, read as production reads" — and
// it had not been applied. This is that round trip.
//
// BUILD-TAGGED on purpose. `go test ./...` in the deploy gate does NOT include these, so a
// missing database cannot block a deploy, and a developer without Docker still gets a
// green local run. CI runs them as their own job against a service container:
//
//	DATABASE_URL=postgres://... go test -tags=dbtest ./internal/store/...
//
// Every test builds its own fixtures through the real schema and asserts what production
// reads. They are written to be independent and re-runnable: unique ids per run, no shared
// mutable state, no ordering requirements between tests.
package store

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/agent-arena/arena/internal/devprofile"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestMain applies every migration before any test runs, using the SAME in-process
// runner the server uses on boot (store.Migrate). So the schema under test is the schema
// production gets — not a hand-maintained fixture that drifts from it, which would defeat
// the entire point of testing against a real database.
func TestMain(m *testing.M) {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		if err := Migrate(url); err != nil {
			fmt.Fprintf(os.Stderr, "dbtest: migrate failed: %v\n", err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set — skipping DB round-trip tests")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return pool
}

// seedOwnerWithAgent creates a real user + agent through the actual schema and returns
// their public ids. Unique per call so tests never collide.
func seedOwnerWithAgent(t *testing.T, pool *pgxpool.Pool, tag string) (userPID, agentPID string) {
	t.Helper()
	ctx := context.Background()
	userPID = fmt.Sprintf("usr_dbt_%s_%d", tag, os.Getpid())
	agentPID = fmt.Sprintf("ag_dbt_%s_%d", tag, os.Getpid())

	var userID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (public_id, x_user_id, x_handle, status)
		 VALUES ($1, $1, $1, 'active') RETURNING id`, userPID).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO agents (public_id, owner_user_id, name, slug, status, verification_level, engine_version)
		 SELECT $1, $2, $1, $1, 'active', 'new', ''
		 WHERE NOT EXISTS (SELECT 1 FROM agents WHERE public_id = $1)`,
		agentPID, userID); err != nil {
		// engine_version may not exist on agents; retry without it rather than failing the
		// whole suite on a column this test does not care about.
		if _, err2 := pool.Exec(ctx,
			`INSERT INTO agents (public_id, owner_user_id, name, slug, status, verification_level)
			 VALUES ($1, $2, $1, $1, 'active', 'new')`, agentPID, userID); err2 != nil {
			t.Fatalf("seed agent: %v / %v", err, err2)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM agent_keys WHERE agent_id IN (SELECT id FROM agents WHERE public_id = $1)`, agentPID)
		_, _ = pool.Exec(ctx, `DELETE FROM agents WHERE public_id = $1`, agentPID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE public_id = $1`, userPID)
	})
	return userPID, agentPID
}

// The bug this file was written for. It asserts the query RUNS and answers correctly —
// the previous version could do neither.
func TestMatchUsageOwnedByRunsAndAnswers(t *testing.T) {
	pool := testPool(t)
	repo := NewMatchUsageRepo(pool)
	owner, agent := seedOwnerWithAgent(t, pool, "usage")

	owns, err := repo.OwnedBy(context.Background(), owner, agent)
	if err != nil {
		t.Fatalf("OwnedBy returned an error — the SQL does not run: %v", err)
	}
	if !owns {
		t.Error("OwnedBy said the owner does not own their own agent")
	}

	// A stranger must not own it, and asking must still not error.
	other, _ := seedOwnerWithAgent(t, pool, "usage2")
	owns, err = repo.OwnedBy(context.Background(), other, agent)
	if err != nil {
		t.Fatalf("OwnedBy(stranger): %v", err)
	}
	if owns {
		t.Error("OwnedBy let a different developer read another's metering")
	}
}

// ForAgent is the read behind GET /v1/matches/{id}/usage. A match with no telemetry row
// must read as zeroes rather than an error: "nothing landed" is the honest answer and the
// one a developer needs to see.
func TestMatchUsageForAgentZeroesRatherThanErrors(t *testing.T) {
	pool := testPool(t)
	repo := NewMatchUsageRepo(pool)
	_, agent := seedOwnerWithAgent(t, pool, "usagezero")

	u, err := repo.ForAgent(context.Background(), "mt_does_not_exist", agent)
	if err != nil {
		t.Fatalf("ForAgent on a match with no metering: %v", err)
	}
	if u.Decisions != 0 || u.Tokens != 0 || u.VerifiedCost != 0 {
		t.Errorf("expected zeroes for an unmetered match, got %+v", u)
	}
}

// The label semantics migration 0071 exists for: issuing replaces the SAME machine's key
// and leaves every other machine alone. This is the property the whole per-machine design
// rests on, and it is enforced by SQL (a partial unique index plus a scoped revoke), so it
// can only be verified here.
func TestIssueKeyReplacesOnlyTheSameLabel(t *testing.T) {
	pool := testPool(t)
	repo := NewIdentityRepo(pool)
	ctx := context.Background()
	owner, agent := seedOwnerWithAgent(t, pool, "keys")

	mustIssue := func(prefix, label string) {
		t.Helper()
		if err := repo.IssueKey(ctx, agent, owner, prefix, "hash-"+prefix, label, 20); err != nil {
			t.Fatalf("IssueKey(%s,%s): %v", prefix, label, err)
		}
	}
	live := func() map[string]string { // label -> prefix
		t.Helper()
		keys, err := repo.ListKeys(ctx, owner)
		if err != nil {
			t.Fatalf("ListKeys: %v", err)
		}
		out := map[string]string{}
		for _, k := range keys {
			if k.RevokedAt == nil {
				out[k.Label] = k.Prefix
			}
		}
		return out
	}

	mustIssue("sk_arena_laptop1", "laptop")
	mustIssue("sk_arena_ci1", "ci-runner")
	got := live()
	if len(got) != 2 || got["laptop"] != "sk_arena_laptop1" || got["ci-runner"] != "sk_arena_ci1" {
		t.Fatalf("two machines should coexist, got %v", got)
	}

	// Re-issue for the laptop only.
	mustIssue("sk_arena_laptop2", "laptop")
	got = live()
	if got["laptop"] != "sk_arena_laptop2" {
		t.Errorf("laptop key was not replaced: %v", got)
	}
	if got["ci-runner"] != "sk_arena_ci1" {
		t.Errorf("re-issuing for the laptop revoked the CI runner's key — the exact bug 0071 fixes: %v", got)
	}
	if len(got) != 2 {
		t.Errorf("expected exactly 2 live keys, got %v", got)
	}
}

// The live-key cap is enforced INSIDE the issuing transaction, so two concurrent issues
// cannot both squeeze past it. Verified here because the counting is done in SQL.
func TestIssueKeyEnforcesTheLiveCap(t *testing.T) {
	pool := testPool(t)
	repo := NewIdentityRepo(pool)
	ctx := context.Background()
	owner, agent := seedOwnerWithAgent(t, pool, "keycap")

	const maxLive = 3
	for i := 0; i < maxLive; i++ {
		label := fmt.Sprintf("machine-%d", i)
		if err := repo.IssueKey(ctx, agent, owner, fmt.Sprintf("sk_arena_cap%d", i), "h", label, maxLive); err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
	}
	// A NEW label at the cap is refused…
	if err := repo.IssueKey(ctx, agent, owner, "sk_arena_capX", "h", "one-too-many", maxLive); err == nil {
		t.Error("issuing past the cap succeeded")
	}
	// …but REPLACING an existing label at the cap must still work, or a developer at the
	// limit could never rotate a leaked key.
	if err := repo.IssueKey(ctx, agent, owner, "sk_arena_cap0b", "h", "machine-0", maxLive); err != nil {
		t.Errorf("replacing an existing label at the cap was refused: %v", err)
	}
}

// The directory's ?self= contract: the caller's row comes back on its own AND is removed
// from both the rows and the count, so the page arithmetic stays exact. Both halves are
// SQL, and getting the count and the rows to disagree is the failure mode.
func TestDirectorySelfIsExcludedFromRowsAndCount(t *testing.T) {
	pool := testPool(t)
	repo := NewDevProfileRepo(pool)
	ctx := context.Background()
	me, _ := seedOwnerWithAgent(t, pool, "dirself")

	const season = 1
	all, err := repo.DirectoryCount(ctx, season, "", "")
	if err != nil {
		t.Fatalf("DirectoryCount: %v", err)
	}
	without, err := repo.DirectoryCount(ctx, season, "", me)
	if err != nil {
		t.Fatalf("DirectoryCount(exclude): %v", err)
	}
	if without != all-1 {
		t.Errorf("excluding self changed the count by %d, want exactly 1", all-without)
	}

	rows, err := repo.Directory(ctx, season, "", "top", 100, 0, me)
	if err != nil {
		t.Fatalf("Directory(exclude): %v", err)
	}
	for _, r := range rows {
		if r.Developer == me {
			t.Error("the excluded developer is still in the rows")
		}
	}

	// And the same developer must be retrievable on their own, via the same base query so
	// the card at the top of the page and the rows below cannot describe a record
	// differently.
	self, found, err := repo.DirectoryRowFor(ctx, season, me)
	if err != nil {
		t.Fatalf("DirectoryRowFor: %v", err)
	}
	if !found || self.Developer != me {
		t.Errorf("DirectoryRowFor(%s) = (%+v, %v)", me, self, found)
	}
	if _, found, err = repo.DirectoryRowFor(ctx, season, "usr_nobody"); err != nil || found {
		t.Errorf("DirectoryRowFor(unknown) = (found %v, err %v), want (false, nil)", found, err)
	}
}

// An empty query must still match EVERYONE. The directory's WHERE was an unparenthesised
// OR chain; adding the self/exclude filters to it would have bound them to the last branch
// only, silently turning "list everybody" into "list nobody".
func TestDirectoryEmptyQueryStillMatchesEveryone(t *testing.T) {
	pool := testPool(t)
	repo := NewDevProfileRepo(pool)
	ctx := context.Background()
	seedOwnerWithAgent(t, pool, "dirall")

	n, err := repo.DirectoryCount(ctx, 1, "", "")
	if err != nil {
		t.Fatalf("DirectoryCount: %v", err)
	}
	if n == 0 {
		t.Fatal("an empty query matched nobody — the WHERE clause is mis-grouped")
	}
	rows, err := repo.Directory(ctx, 1, "", "recent", 5, 0, "")
	if err != nil {
		t.Fatalf("Directory: %v", err)
	}
	if len(rows) == 0 {
		t.Error("an empty query returned no rows")
	}
	var _ []devprofile.DirectoryRow = rows // shape assertion
}

// FOLLOWERS vs FOLLOWING must not be swapped. The two directions are one map lookup apart
// (followEdges), and getting them backwards would show a developer their own followers under
// "following" — a wrong answer that looks entirely plausible on screen, which is exactly the
// kind of bug that survives review and only a real edge can catch.
func TestFollowListDirectionsAreNotSwapped(t *testing.T) {
	pool := testPool(t)
	repo := NewDevProfileRepo(pool)
	ctx := context.Background()

	// alice follows bob. So bob has one FOLLOWER (alice), and alice is FOLLOWING one (bob).
	alice, _ := seedOwnerWithAgent(t, pool, "falice")
	bob, _ := seedOwnerWithAgent(t, pool, "fbob")
	if _, err := pool.Exec(ctx,
		`INSERT INTO developer_follows (follower_user_id, followee_user_id)
		 SELECT (SELECT id FROM users WHERE public_id = $1), (SELECT id FROM users WHERE public_id = $2)`,
		alice, bob); err != nil {
		t.Fatalf("seed follow edge: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM developer_follows
			WHERE follower_user_id = (SELECT id FROM users WHERE public_id = $1)`, alice)
	})

	only := func(dir, subject string) []string {
		t.Helper()
		rows, err := repo.FollowList(ctx, 1, subject, dir, 50, 0)
		if err != nil {
			t.Fatalf("FollowList(%s, %s): %v", dir, subject, err)
		}
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.Developer)
		}
		return out
	}

	if got := only("followers", bob); len(got) != 1 || got[0] != alice {
		t.Errorf("bob's followers = %v, want [alice=%s]", got, alice)
	}
	if got := only("following", alice); len(got) != 1 || got[0] != bob {
		t.Errorf("alice's following = %v, want [bob=%s]", got, bob)
	}
	// And the reverse must be EMPTY, or the two directions are the same query.
	if got := only("following", bob); len(got) != 0 {
		t.Errorf("bob follows nobody but 'following' returned %v — the directions are swapped", got)
	}
	if got := only("followers", alice); len(got) != 0 {
		t.Errorf("alice has no followers but 'followers' returned %v — the directions are swapped", got)
	}

	// The counts must agree with the lists, since the pager is driven by them.
	for _, tc := range []struct {
		subject, dir string
		want         int
	}{{bob, "followers", 1}, {alice, "following", 1}, {bob, "following", 0}, {alice, "followers", 0}} {
		n, err := repo.FollowListCount(ctx, tc.subject, tc.dir)
		if err != nil {
			t.Fatalf("FollowListCount(%s): %v", tc.dir, err)
		}
		if n != tc.want {
			t.Errorf("FollowListCount(%s, %s) = %d, want %d", tc.subject, tc.dir, n, tc.want)
		}
	}

	// FollowCounts backs the profile header and MUST match the lists, or the header says 1
	// and the list shows 0 (or the pager offers a page that comes back empty).
	followers, following, err := repo.FollowCounts(ctx, bob)
	if err != nil {
		t.Fatalf("FollowCounts: %v", err)
	}
	if followers != 1 || following != 0 {
		t.Errorf("FollowCounts(bob) = (%d, %d), want (1, 0) — header and list disagree", followers, following)
	}

	// An unknown direction must yield nothing rather than the wrong list.
	if rows, err := repo.FollowList(ctx, 1, bob, "sideways", 10, 0); err != nil || len(rows) != 0 {
		t.Errorf("FollowList with a bad direction = (%d rows, %v), want (0, nil)", len(rows), err)
	}
}
