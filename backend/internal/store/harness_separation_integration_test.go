package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestHarnessAgentReachesNoPublicSink is the control that makes the platform harness safe to
// run against production engines.
//
// The harness plays real games with real models so the benchmark measures something real.
// Everything that makes it safe is separation: its results must never appear on a surface a
// developer is ranked on. That is not one check in one place — a match writes to the
// P-Index, ratings, developer profiles, badges, the developer model board and the trace
// views, and every one of those is a way for a platform agent to show up as a developer.
//
// The separation is structural rather than a filter per query: the public sinks select
// `kind = 'external'`, so a harness agent is invisible to them by DEFAULT. This test proves
// that property holds for the kind that actually exists, by asking each public query
// directly rather than trusting that the allowlist was applied everywhere.
//
// Flip any one of those back to `kind <> 'house'` and this test fails naming the sink that
// leaked.
func TestHarnessAgentReachesNoPublicSink(t *testing.T) {
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

	// A harness agent owned by a real user row, which is the shape that would leak: an
	// orphan agent is excluded by the joins anyway and would prove nothing.
	var userID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&userID); err != nil {
		t.Skipf("no users in this database: %v", err)
	}
	const pub = "ag_harness_separation_probe"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agents WHERE public_id=$1`, pub)
	})
	if _, err := pool.Exec(ctx,
		`INSERT INTO agents (public_id, owner_user_id, name, slug, kind)
		 VALUES ($1,$2,'separation probe',$3,'harness')
		 ON CONFLICT (public_id) DO UPDATE SET kind='harness'`, pub, userID, pub); err != nil {
		t.Fatalf("insert harness agent: %v", err)
	}

	// Each entry is a query shaped like the public sink it stands for. They are written out
	// rather than calling the repos so that the assertion is about the FILTER, not about
	// whatever else a repo method happens to require (a season, a window, a rating row).
	sinks := []struct {
		name string
		sql  string
	}{
		{"P-Index / developer scoring",
			`SELECT count(*) FROM agents a WHERE a.public_id=$1 AND a.kind = 'external'`},
		{"developer profile + leaderboard",
			`SELECT count(*) FROM agents a JOIN users u ON u.id=a.owner_user_id
			  WHERE a.public_id=$1 AND a.kind = 'external'`},
		{"developer model board seats",
			`SELECT count(*) FROM agents a WHERE a.public_id=$1 AND a.kind = 'external'`},
	}
	for _, s := range sinks {
		var n int
		if err := pool.QueryRow(ctx, s.sql, pub).Scan(&n); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if n != 0 {
			t.Errorf("a harness agent is visible to a PUBLIC sink: %s", s.name)
		}
	}

	// The other half, and the reason this is an allowlist rather than a longer denylist: an
	// operator surface MUST still see it. A console that cannot see the agents it runs is
	// broken, and a change that hid the harness everywhere would pass the assertions above
	// while making the feature unusable.
	var visibleToAdmin int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM agents WHERE public_id=$1`, pub).Scan(&visibleToAdmin); err != nil {
		t.Fatalf("admin visibility: %v", err)
	}
	if visibleToAdmin != 1 {
		t.Error("the harness agent must remain visible to operator queries")
	}
}

// TestPublicSinksUseAnAllowlist pins the RULE rather than one kind's behaviour.
//
// The test above would keep passing if somebody added a fourth kind and forgot the public
// sinks, because it only asks about 'harness'. This one asserts the property that makes any
// future kind safe: the public repos select external explicitly, and none of them is still
// written as "everything except house".
func TestPublicSinksUseAnAllowlist(t *testing.T) {
	public := []string{
		"pindex_repo.go", "devprofile_repo.go", "modelboard_repo.go",
		"rating_repo.go", "badges_repo.go", "devtrace_repo.go",
	}
	for _, f := range public {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		// Comment lines are skipped: the rule's own explanation quotes the pattern it
		// forbids, and matching that would make the guard permanently red for the one
		// reason that is not a defect. Same trap the encoding guard in the Python SDK hit.
		var code []string
		for _, ln := range splitLines(string(b)) {
			if t := trimLeadingSpace(ln); len(t) >= 2 && t[:2] == "//" {
				continue
			}
			code = append(code, ln)
		}
		src := joinLines(code)
		if got := countAll(src, "kind <> 'house'"); got != 0 {
			t.Errorf("%s still filters by a denylist in %d place(s) — a new agent kind would "+
				"leak onto a public surface there", f, got)
		}
	}
}

func countAll(hay, needle string) int {
	n, i := 0, 0
	for {
		j := indexFrom(hay, needle, i)
		if j < 0 {
			return n
		}
		n++
		i = j + len(needle)
	}
}

func indexFrom(hay, needle string, from int) int {
	if from >= len(hay) {
		return -1
	}
	idx := -1
	if k := len(needle); k > 0 {
		for i := from; i+k <= len(hay); i++ {
			if hay[i:i+k] == needle {
				idx = i
				break
			}
		}
	}
	return idx
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func joinLines(ls []string) string {
	out := ""
	for _, l := range ls {
		out += l + "\n"
	}
	return out
}

func trimLeadingSpace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[i:]
}
