package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/agent-arena/arena/internal/benchmark"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The pull path, end to end, against a REAL Postgres.
//
// Deliberately BEHAVIOURAL, as specified in OBSERVABILITY_COVERAGE_GAP.md: it records decisions
// the way Act will and then asserts the rows every board actually reads. A structural test — "a
// recorder exists on this path" — would have passed throughout the period when Monopoly emitted
// nothing at all, which is precisely how the gap survived.
func TestActDecisionsProduceTheRowsEveryBoardReads(t *testing.T) {
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
	repo := NewPIndexRepo(pool)

	const matchID = "mp_actpath_itest"
	agents := []string{"ag_actpath_a", "ag_actpath_b"}
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM agent_match_benchmark WHERE match_id = $1`, matchID)
		_, _ = pool.Exec(ctx, `DELETE FROM agent_match_decisions WHERE match_id = $1`, matchID)
		_, _ = pool.Exec(ctx, `DELETE FROM agents WHERE public_id = ANY($1)`, agents)
	}
	// BEFORE seeding, so a previous run's rows cannot collide — and never after, which is what
	// the first version of this test did: it seeded the agents, then immediately deleted them,
	// so every insert silently matched no agent and wrote nothing. The INSERT..SELECT pattern
	// makes that failure quiet, which is exactly why the assertion is on ROWS rather than on the
	// absence of an error.
	cleanup()
	t.Cleanup(cleanup)

	var ids []int64
	for i, pub := range agents {
		var id int64
		if err := pool.QueryRow(ctx,
			`INSERT INTO agents (public_id, name, slug, owner_user_id, status)
			 SELECT $1, $2, $2, (SELECT id FROM users ORDER BY id LIMIT 1), 'active'
			 ON CONFLICT (public_id) DO UPDATE SET name = EXCLUDED.name
			 RETURNING id`, pub, "actpath-itest-"+string(rune('a'+i))).Scan(&id); err != nil {
			t.Fatalf("seed agent %s: %v", pub, err)
		}
		ids = append(ids, id)
	}
	// Two seats, three decisions each — the shape a Monopoly table played through Act produces.
	// One seat reports model usage including a cache WRITE; the other reports none, which is the
	// realistic mix and exercises the nil-usage branch.
	usage := &benchmark.TokenUsage{
		Provider: "anthropic", Model: "claude-opus-4",
		PromptTokens: 2520, CompletionTokens: 90,
		CachedTokens: 1500, CachedWriteTokens: 600,
		Scaffold: "sc_actpath",
	}
	view, _ := json.Marshal(map[string]any{"turn": 3, "cash": 1500})
	for seq := 0; seq < 3; seq++ {
		if err := repo.RecordActDecision(ctx, ActDecision{
			MatchID: matchID, AgentPublicID: agents[0], Game: "monopoly",
			Seq: seq, Round: seq + 1, Action: "buy", Outcome: "ok",
			LatencyMS: int64(100 + seq), Rationale: "orange group is the play",
			Usage: usage, InputJSON: view,
		}); err != nil {
			t.Fatalf("record seat A seq %d: %v", seq, err)
		}
		if err := repo.RecordActDecision(ctx, ActDecision{
			MatchID: matchID, AgentPublicID: agents[1], Game: "monopoly",
			Seq: seq, Round: seq + 1, Action: "pass", Outcome: "fallback",
			LatencyMS: 50,
		}); err != nil {
			t.Fatalf("record seat B seq %d: %v", seq, err)
		}
	}

	// The decision log: what a developer's trace reads.
	var logged int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_match_decisions WHERE match_id = $1`, matchID).Scan(&logged); err != nil {
		t.Fatalf("count decisions: %v", err)
	}
	if logged != 6 {
		t.Fatalf("decision log has %d rows, want 6 — the pull path is not recording", logged)
	}

	// Cost, cache split and scaffold must all survive, since those are exactly what the boards
	// were missing for two of three games.
	var cost float64
	var cachedWrite int
	var scaffold string
	if err := pool.QueryRow(ctx,
		`SELECT estimated_cost, cached_write_tokens, scaffold FROM agent_match_decisions
		  WHERE match_id = $1 AND agent_id = $2 AND seq = 0`, matchID, ids[0]).
		Scan(&cost, &cachedWrite, &scaffold); err != nil {
		t.Fatalf("read seat A decision: %v", err)
	}
	if cachedWrite != 600 {
		t.Errorf("cached_write_tokens = %d, want 600", cachedWrite)
	}
	if scaffold != "sc_actpath" {
		t.Errorf("scaffold = %q, want sc_actpath", scaffold)
	}
	// 420 uncached @15 + 1500 read @1.50 + 600 written @18.75 + 90 out @75 = 0.02655
	if cost < 0.0265 || cost > 0.0266 {
		t.Errorf("estimated_cost = %v, want ~0.02655 — the shared pricing path is not being used", cost)
	}

	// Now the match-end aggregation: the seat facts every board reads.
	if err := repo.AggregateSeatBenchmark(ctx, matchID, "monopoly", map[string]string{
		agents[0]: "win", agents[1]: "loss",
	}); err != nil {
		t.Fatalf("AggregateSeatBenchmark: %v", err)
	}

	type row struct {
		result                      string
		decisions, legal, fallbacks int
		latencySum, latencyMin      int64
		tokens                      int64
		obsProvider, obsModel       string
		estCost                     float64
	}
	got := map[string]row{}
	rows, err := pool.Query(ctx,
		`SELECT a.public_id, b.result, b.decisions, b.legal, b.fallbacks,
		        b.latency_sum_ms, b.latency_min_ms, b.tokens,
		        b.observed_provider, b.observed_model, b.estimated_cost
		   FROM agent_match_benchmark b JOIN agents a ON a.id = b.agent_id
		  WHERE b.match_id = $1`, matchID)
	if err != nil {
		t.Fatalf("read benchmark: %v", err)
	}
	for rows.Next() {
		var pub string
		var r row
		if err := rows.Scan(&pub, &r.result, &r.decisions, &r.legal, &r.fallbacks,
			&r.latencySum, &r.latencyMin, &r.tokens,
			&r.obsProvider, &r.obsModel, &r.estCost); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[pub] = r
	}
	rows.Close()

	if len(got) != 2 {
		t.Fatalf("benchmark has %d seat rows, want 2 — this is the table the model board, the "+
			"model benchmark, the developer edge and P-Index Intelligence all read", len(got))
	}
	a := got[agents[0]]
	if a.result != "win" || a.decisions != 3 || a.legal != 3 || a.fallbacks != 0 {
		t.Errorf("seat A = %+v, want win/3/3/0", a)
	}
	if a.latencySum != 100+101+102 {
		t.Errorf("seat A latency sum = %d, want 303", a.latencySum)
	}
	if a.obsProvider != "anthropic" || a.obsModel != "claude-opus-4" {
		t.Errorf("seat A attribution = %q/%q, want the model it actually ran", a.obsProvider, a.obsModel)
	}
	if a.estCost < 3*0.0265 || a.estCost > 3*0.0266 {
		t.Errorf("seat A cost = %v, want ~%v (3 decisions)", a.estCost, 3*0.02655)
	}
	b := got[agents[1]]
	if b.result != "loss" || b.decisions != 3 || b.legal != 0 || b.fallbacks != 3 {
		t.Errorf("seat B = %+v, want loss/3/0/3", b)
	}
	// A seat that never reported usage must show no attribution rather than inheriting its
	// opponent's — the aggregation groups per seat, and a bug there would silently credit one
	// developer's model with another's play.
	if b.obsModel != "" {
		t.Errorf("seat B inherited a model it never ran: %q", b.obsModel)
	}
	if b.tokens != 0 || b.estCost != 0 {
		t.Errorf("seat B reported tokens/cost with no usage: %d/%v", b.tokens, b.estCost)
	}
}

// Act can be retried by a client or replayed by a proxy. A retry must refresh its decision, not
// append a second one that never happened — otherwise a flaky network inflates an agent's
// decision count and dilutes every per-decision rate computed from it.
func TestRetriedActDecisionsAreIdempotent(t *testing.T) {
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
	repo := NewPIndexRepo(pool)

	const matchID = "mp_actretry_itest"
	const agent = "ag_actretry_itest"
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM agent_match_benchmark WHERE match_id = $1`, matchID)
		_, _ = pool.Exec(ctx, `DELETE FROM agent_match_decisions WHERE match_id = $1`, matchID)
		_, _ = pool.Exec(ctx, `DELETE FROM agents WHERE public_id = $1`, agent)
	}
	cleanup() // before seeding, never after — see the note in the test above
	t.Cleanup(cleanup)
	// RETURNING so a seed that silently matched nothing fails loudly here rather than as a
	// mysterious zero-row insert later.
	var seeded int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO agents (public_id, name, slug, owner_user_id, status)
		 SELECT $1, 'actretry', 'actretry', (SELECT id FROM users ORDER BY id LIMIT 1), 'active'
		 ON CONFLICT (public_id) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id`, agent).Scan(&seeded); err != nil {
		t.Fatalf("seed: %v", err)
	}

	d := ActDecision{
		MatchID: matchID, AgentPublicID: agent, Game: "monopoly",
		Seq: 0, Round: 1, Action: "buy", Outcome: "ok", LatencyMS: 120,
		Rationale: "first answer",
		Usage:     &benchmark.TokenUsage{Provider: "anthropic", Model: "claude-opus-4", PromptTokens: 10, CompletionTokens: 5},
	}
	for i := 0; i < 4; i++ {
		if err := repo.RecordActDecision(ctx, d); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_match_decisions WHERE match_id = $1`, matchID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("four retries produced %d decision rows, want 1", n)
	}

	// A retry that lost its usage block must not erase attribution an earlier one recorded.
	stripped := d
	stripped.Usage = nil
	stripped.Rationale = ""
	if err := repo.RecordActDecision(ctx, stripped); err != nil {
		t.Fatalf("stripped retry: %v", err)
	}
	var model, rationale string
	if err := pool.QueryRow(ctx,
		`SELECT model, rationale FROM agent_match_decisions WHERE match_id = $1 AND seq = 0`,
		matchID).Scan(&model, &rationale); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if model != "claude-opus-4" {
		t.Errorf("a retry without usage erased the model: %q", model)
	}
	if rationale != "first answer" {
		t.Errorf("a retry without a rationale erased it: %q", rationale)
	}
}
