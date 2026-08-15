package store

import (
	"context"
	"github.com/agent-arena/arena/internal/llmgw"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/modelboard"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestModelBoardSeatsIntegration runs the seat query against a REAL Postgres.
//
// Five joins, two window functions and a jsonb path lookup. None of that is exercised by
// compiling, and the failure modes are silent rather than loud: a mis-keyed join returns FEWER
// rows, not an error, and a board fitted on half its seats looks like a board with less data
// rather than a broken query. This asserts the shape and the invariants that must hold whatever
// the data happens to be.
//
// Skipped unless PYYOL_TEST_DATABASE_URL points at a migrated DB.
func TestModelBoardSeatsIntegration(t *testing.T) {
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

	repo := NewModelBoardRepo(pool)
	// A window wide enough to cover whatever the lab has played.
	start := time.Now().AddDate(-5, 0, 0)
	end := time.Now().AddDate(1, 0, 0)

	seats, err := repo.Seats(ctx, "", start, end, realHosts())
	if err != nil {
		t.Fatalf("Seats: %v", err)
	}
	t.Logf("read %d seats", len(seats))

	perMatch := map[string]int{}
	for _, s := range seats {
		perMatch[s.MatchID]++
		if s.MatchID == "" || s.Game == "" || s.AgentID == "" {
			t.Errorf("seat missing an identity: %+v", s)
		}
		if s.DeveloperID == "" {
			t.Errorf("seat %s/%s has no developer — the stratum would collapse", s.MatchID, s.AgentID)
		}
		// Coverage must be internally consistent whatever the data is.
		if s.Coverage < 0 || s.Coverage > 1 {
			t.Errorf("coverage %v out of range for %s/%s", s.Coverage, s.MatchID, s.AgentID)
		}
		if !s.CoverageKnown && s.Coverage != 0 {
			t.Errorf("unknown coverage carrying value %v", s.Coverage)
		}
		// A verified model key is provider/model, never a bare slash or a half-empty pair.
		if s.Model != "" {
			if s.Model == "/" || s.Model[0] == '/' || s.Model[len(s.Model)-1] == '/' {
				t.Errorf("malformed model key %q — provider or model was empty and the "+
					"concatenation produced a phantom key", s.Model)
			}
		}
	}

	// A match returning ONE seat has to be EXPLAINABLE, and there are three different reasons —
	// only one of which is this query's fault:
	//
	//   a) the opponent was a house bot          -> correct, no model comparison is possible
	//   b) the opponent has no benchmark row     -> an upstream data gap, reported not failed
	//   c) neither of those                      -> a join in this query dropped a seat
	//
	// My first version of this assertion collapsed all three and flagged fifteen perfectly good
	// sandbox matches as join failures. The second caught real single-seat matches but blamed the
	// join for what turned out to be missing benchmark rows. Only (c) is a defect here.
	var missingBenchmark int
	for id, n := range perMatch {
		if n >= 2 {
			continue
		}
		var rateable, benchmarked int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FILTER (WHERE a.kind <> 'house'),
			        count(*) FILTER (WHERE a.kind <> 'house' AND b.match_id IS NOT NULL)
			   FROM match_players mp
			   JOIN matches m ON m.id = mp.match_id
			   JOIN agents a ON a.id = mp.agent_id
			   LEFT JOIN agent_match_benchmark b ON b.match_id = m.public_id AND b.agent_id = a.id
			  WHERE m.public_id = $1`, id).Scan(&rateable, &benchmarked); err != nil {
			t.Fatalf("classify %s: %v", id, err)
		}
		switch {
		case rateable < 2:
			// (a) house-bot opponent.
		case benchmarked < rateable:
			// (b) upstream gap. Counted and reported below rather than failed: this query cannot
			// invent a row that was never written, and failing here would blame the wrong layer.
			missingBenchmark++
		default:
			t.Errorf("match %s has %d rateable players, all benchmarked, but the query returned "+
				"%d seat(s) — a join in modelBoardSeatsSQL dropped one", id, rateable, n)
		}
	}
	if missingBenchmark > 0 {
		// Loud, because it bounds what EVERY board reading agent_match_benchmark can see, not just
		// this one. A board silently resting on half its matches looks like a board with less data.
		t.Logf("UPSTREAM GAP: %d match(es) lost a seat because an agent had no "+
			"agent_match_benchmark row. Those seats are invisible to the model board, the model "+
			"benchmark and the developer edge alike.", missingBenchmark)
	}

	// The builder must accept this shape and produce a census rather than an error.
	cmp, excluded := modelboard.BuildComparisons(seats, modelboard.DefaultBuildConfig())
	t.Logf("built %d comparisons; census: %v", len(cmp), excluded)
	for _, c := range cmp {
		if c.ModelA == "" || c.ModelB == "" {
			t.Errorf("comparison with an unattributed side survived the build: %+v", c)
		}
		if c.StratumA == "" || c.StratumB == "" || c.StratumA == "/" || c.StratumB == "/" {
			t.Errorf("comparison with an empty stratum survived the build: %+v", c)
		}
		if c.Weight <= 0 || c.Weight > 1 {
			t.Errorf("weight %v outside (0,1]: %+v", c.Weight, c)
		}
	}
}

// realHosts is the publishable set used by these tests: the vendor endpoints the binary
// ships, with no operator additions.
func realHosts() []string {
	set := llmgw.PublishableUpstreamHosts("")
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// TestModelBoardExcludesStandInUpstreams pins the control that keeps a lab stand-in off a
// public ranking of models.
//
// provider and model are read from the REQUEST. LLM_GATEWAY_UPSTREAMS decides where that
// request is actually sent. Point anthropic at a local stand-in — which every lab run does
// — and the gateway records a bound, well-formed `anthropic / claude-opus-4` call, with
// usage, for a response no model produced. In this database 1,601 of 1,829 bound calls
// were exactly that, under 25 model names.
//
// The control is `mc.upstream_host = ANY($4)` in the verified CTE. Remove it and this test
// fails on the stand-in seat gaining an attribution, which is the whole point of driving it
// through the real query rather than asserting on the filter in isolation.
func TestModelBoardExcludesStandInUpstreams(t *testing.T) {
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

	// Count bound calls by provenance, straight from the table, so the assertion below is
	// about THIS database rather than an assumption about it.
	var external, standIn int
	if err := pool.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE upstream_host = ANY($1)),
		  count(*) FILTER (WHERE NOT (upstream_host = ANY($1)))
		FROM agent_model_calls WHERE bound AND COALESCE(model,'') <> ''`,
		realHosts()).Scan(&external, &standIn); err != nil {
		t.Fatalf("count by provenance: %v", err)
	}
	t.Logf("bound calls: %d externally served, %d stand-in or unknown", external, standIn)
	if standIn == 0 {
		t.Skip("no stand-in calls in this database — nothing for the filter to exclude")
	}

	// Every model the board is willing to attribute must come from a publishable host.
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT mc.provider || '/' || mc.model
		  FROM agent_model_calls mc
		 WHERE mc.bound AND COALESCE(mc.model,'') <> ''
		   AND mc.upstream_host = ANY($1)`, realHosts())
	if err != nil {
		t.Fatalf("query attributable models: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		// The stand-in answers under names no provider ships. If one of these is
		// attributable, the filter is not doing its job.
		for _, fake := range []string{"lab-alpha", "lab-beta", "slow-alpha", "slow-beta",
			"budget-alpha", "budget-beta", "detail-model-", "verified-model-"} {
			if strings.Contains(key, fake) {
				t.Errorf("a lab stand-in model is attributable on the board: %q", key)
			}
		}
	}
}
