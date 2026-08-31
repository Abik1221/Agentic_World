package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/rating"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The model-benchmark aggregate against REAL Postgres, with real fixtures.
//
// "The SQL parses" is not enough for this query: it resolves a three-tier attribution
// ladder, splits two different denominators, and produces both the aggregate and the
// per-arena breakdown from one GROUPING SETS pass. Every one of those can be wrong
// while the query runs perfectly, so these assert the numbers.
//
// Skipped unless PYYOL_TEST_DATABASE_URL points at a Postgres; the harness migrates.

// benchFixture inserts a finished match plus one agent's benchmark fact for it.
type benchFixture struct {
	matchID  string
	game     string
	agentPub string
	ownerID  int64

	result           string
	decisions, legal int
	tokens           int64
	estCost          float64
	durationSec      int

	observedProvider, observedModel string
	declaredProvider, declaredModel string

	// When set, a gateway-verified row is written too — the top attribution tier.
	verifiedProvider, verifiedModel string
	verifiedCost                    float64

	// finishedAt defaults to now; set it to push a match out of the season window.
	finishedAt time.Time
}

func writeBenchFixture(t *testing.T, pool *pgxpool.Pool, f benchFixture) {
	t.Helper()
	ctx := context.Background()
	fin := f.finishedAt
	if fin.IsZero() {
		fin = time.Now().Add(-time.Minute)
	}
	started := fin.Add(-time.Duration(f.durationSec) * time.Second)

	if _, err := pool.Exec(ctx,
		`INSERT INTO matches (public_id, game, status, bid, engine_version, prize_seed_commit,
		                      creator_owner_user_id, started_at, finished_at)
		 VALUES ($1,$2,'finished',0,'test','test',$3,$4,$5)
		 ON CONFLICT (public_id) DO UPDATE SET started_at = $4, finished_at = $5`,
		f.matchID, f.game, f.ownerID, started, fin); err != nil {
		t.Fatalf("insert match %s: %v", f.matchID, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_match_benchmark (
		     match_id, agent_id, game, decisions, legal, fallbacks, latency_sum_ms,
		     latency_min_ms, latency_max_ms, tokens, prompt_tokens, completion_tokens,
		     estimated_cost, result, observed_provider, observed_model,
		     declared_provider, declared_model)
		 SELECT $1, a.id, $2, $3, $4, 0, $5, 100, 900,
		        $6::bigint, $6::bigint / 2, $6::bigint / 2, $7, $8, $9, $10, $11, $12
		 FROM agents a WHERE a.public_id = $13
		 ON CONFLICT (match_id, agent_id) DO NOTHING`,
		f.matchID, f.game, f.decisions, f.legal, int64(f.decisions)*400, f.tokens,
		f.estCost, f.result, f.observedProvider, f.observedModel,
		f.declaredProvider, f.declaredModel, f.agentPub); err != nil {
		t.Fatalf("insert benchmark fact %s: %v", f.matchID, err)
	}
	if f.verifiedModel != "" {
		if _, err := pool.Exec(ctx,
			`INSERT INTO agent_match_verified_cost (match_id, agent_id, verified_cost, calls, provider, model, total_tokens)
			 SELECT $1, a.id, $2, 1, $3, $4, $5 FROM agents a WHERE a.public_id = $6
			 ON CONFLICT (match_id, agent_id) DO NOTHING`,
			f.matchID, f.verifiedCost, f.verifiedProvider, f.verifiedModel, f.tokens, f.agentPub); err != nil {
			t.Fatalf("insert verified cost %s: %v", f.matchID, err)
		}
		// The DECISION LOG and its bindings, because coverage is bound ÷ logged and the board
		// DOWNGRADES attribution by coverage — it can never promote it. A fixture that declared
		// 250 gateway-verified decisions while logging none produced coverage 0, so the row came
		// back "observed" and "self-reported" no matter what else it carried, and the test read
		// as a product defect on a clean database.
		//
		// Written to MATCH f.decisions exactly: this seat played that many decisions and every
		// one of them was proven, which is what "gateway-verified" claims.
		if _, err := pool.Exec(ctx,
			`INSERT INTO agent_match_decisions (match_id, agent_id, seq, round)
			 SELECT $1, a.id, g.i, g.i FROM agents a, generate_series(0, $2 - 1) AS g(i)
			  WHERE a.public_id = $3
			 ON CONFLICT DO NOTHING`, f.matchID, f.decisions, f.agentPub); err != nil {
			t.Fatalf("seed decision log %s: %v", f.matchID, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO agent_match_bound_decisions (match_id, agent_id, round)
			 SELECT $1, a.id, g.i FROM agents a, generate_series(0, $2 - 1) AS g(i)
			  WHERE a.public_id = $3
			 ON CONFLICT DO NOTHING`, f.matchID, f.decisions, f.agentPub); err != nil {
			t.Fatalf("seed bound decisions %s: %v", f.matchID, err)
		}
		// The BOUND CALL that makes this fixture what it says it is.
		//
		// "verified" on this platform means the gateway PROVED a call belonged to a decision
		// and saw the provider name a model — that one row in agent_model_calls is what the
		// model board's `no_verified_model` exclusion and the published-ladder filter both
		// read. Writing only the verified-cost row described a gateway-verified seat while
		// leaving no evidence any of the code that decides "verified" can see, so the fixture
		// asserted an attribution its own data did not support.
		if _, err := pool.Exec(ctx,
			`INSERT INTO agent_model_calls (agent_id, match_id, round, bound, provider, model, status)
			 SELECT a.id, $1, 1, true, $2, $3, 200 FROM agents a WHERE a.public_id = $4`,
			f.matchID, f.verifiedProvider, f.verifiedModel, f.agentPub); err != nil {
			t.Fatalf("insert bound model call %s: %v", f.matchID, err)
		}
		// The COVERAGE ROLLUP, which is what the board actually reads.
		//
		// The two writes above are the raw decision log. The benchmark query stopped grouping
		// that log per request when it reached 10.2M rows and started hanging the public routes;
		// it now LEFT JOINs agent_match_coverage, refreshed off match finish time by
		// CoverageWorker (cmd/server/main.go, every minute). No worker runs in a test, so the
		// rollup stayed empty, coverage read as unknown, and every fixture seat came back
		// "observed"/"self-reported" however many decisions it had proved.
		//
		// Written by the same aggregation CoverageRepo.Refresh uses — COUNT(*) logged against
		// COUNT(DISTINCT round) bound — but scoped to this seat rather than to a time window.
		// Calling the worker's Refresh here would make the result depend on a shared watermark
		// that earlier tests in the same database have already advanced past these matches.
		if _, err := pool.Exec(ctx,
			`INSERT INTO agent_match_coverage (match_id, agent_id, logged_decisions, bound_decisions, computed_at)
			 SELECT $1, a.id,
			        (SELECT COUNT(*) FROM agent_match_decisions d
			          WHERE d.match_id = $1 AND d.agent_id = a.id),
			        (SELECT COUNT(DISTINCT bd.round) FROM agent_match_bound_decisions bd
			          WHERE bd.match_id = $1 AND bd.agent_id = a.id),
			        now()
			   FROM agents a WHERE a.public_id = $2
			 ON CONFLICT (match_id, agent_id) DO UPDATE SET
			      logged_decisions = EXCLUDED.logged_decisions,
			      bound_decisions  = EXCLUDED.bound_decisions,
			      computed_at      = now()`,
			f.matchID, f.agentPub); err != nil {
			t.Fatalf("roll up coverage %s: %v", f.matchID, err)
		}
	}
}

func writeRating(t *testing.T, pool *pgxpool.Pool, agentPub, game string, season, elo, w, l int, coins int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO ratings (agent_id, game, season, elo, wins, losses, ties, coins_earned)
		 SELECT a.id, $2, $3, $4, $5, $6, 0, $7 FROM agents a WHERE a.public_id = $1
		 ON CONFLICT (agent_id, game, season) DO UPDATE SET elo = $4, wins = $5, losses = $6, coins_earned = $7`,
		agentPub, game, season, elo, w, l, coins); err != nil {
		t.Fatalf("insert rating %s/%s: %v", agentPub, game, err)
	}
}

func findModel(models []rating.ModelStat, name string) (rating.ModelStat, bool) {
	for _, m := range models {
		if m.Model == name {
			return m, true
		}
	}
	return rating.ModelStat{}, false
}

// isolate returns a season number and a name suffix unique to this run.
//
// The harness reuses whatever database PYYOL_TEST_DATABASE_URL points at and never
// truncates it, so a fixed season plus fixed model names means the SECOND run of this
// test aggregates the first run's rows too — which shows up as every count being an
// exact multiple. Both the season and the model names have to vary, because the season
// isolates the ratings join and the names isolate the lookup.
func isolate() (season int, suffix string) {
	n := time.Now().UnixNano()
	return 900_000 + int(n/1e3%90_000), time.Now().Format("150405.000000")
}

func TestModelBenchmarkAggregatesLive(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	rr := NewRatingRepo(pool)

	season, run := isolate()
	start, end := time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour)
	// Model names carry the run suffix so a lookup cannot match a previous run's rows.
	var (
		verifiedName = "verified-model-" + run
		observedName = "observed-model-" + run
		declaredName = "manifest-only-model-" + run
		sdkClaim     = "sdk-said-this-" + run
		gwClaim      = "declared-but-not-run-" + run
		sdkManifest  = "declared-model-ignored-" + run
	)

	// Three agents, one per attribution tier, so the ladder is exercised end to end.
	gwAg, gwOw := mkUserAgent(t, pool, "bmgw-"+run)
	sdkAg, sdkOw := mkUserAgent(t, pool, "bmsdk-"+run)
	mfAg, mfOw := mkUserAgent(t, pool, "bmmf-"+run)
	ownerID := func(pub string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE public_id = $1`, pub).Scan(&id); err != nil {
			t.Fatalf("owner id %s: %v", pub, err)
		}
		return id
	}
	gwOwner, sdkOwner, mfOwner := ownerID(gwOw), ownerID(sdkOw), ownerID(mfOw)

	// ── the gateway-verified agent: two Mafia wins and one Monopoly loss ─────────
	// It DECLARES a different model than it actually ran, which is exactly the case
	// the ladder exists for: the board must report what the provider confirmed.
	for i, g := range []string{"mafia", "mafia", "monopoly"} {
		res := "win"
		if g == "monopoly" {
			res = "loss"
		}
		writeBenchFixture(t, pool, benchFixture{
			matchID: fmt.Sprintf("m-gw-%s-%d", run, i), game: g, agentPub: gwAg, ownerID: gwOwner,
			result: res, decisions: 100, legal: 98, tokens: 10_000, estCost: 0.10, durationSec: 60,
			declaredProvider: "openai", declaredModel: gwClaim,
			observedProvider: "anthropic", observedModel: sdkClaim,
			verifiedProvider: "anthropic", verifiedModel: verifiedName, verifiedCost: 0.50,
		})
	}
	writeRating(t, pool, gwAg, "mafia", season, 1600, 2, 0, 500)
	writeRating(t, pool, gwAg, "monopoly", season, 1400, 0, 1, 0)

	// ── the SDK-observed agent: one Goofspiel win, no gateway rows ───────────────
	writeBenchFixture(t, pool, benchFixture{
		matchID: "m-sdk-" + run, game: "goofspiel", agentPub: sdkAg, ownerID: sdkOwner,
		result: "win", decisions: 50, legal: 50, tokens: 4_000, estCost: 0.04, durationSec: 120,
		observedProvider: "openai", observedModel: observedName,
		declaredProvider: "openai", declaredModel: sdkManifest,
	})
	writeRating(t, pool, sdkAg, "goofspiel", season, 1520, 1, 0, 100)

	// ── the manifest-only agent: nothing observed, nothing verified ──────────────
	writeBenchFixture(t, pool, benchFixture{
		matchID: "m-mf-" + run, game: "goofspiel", agentPub: mfAg, ownerID: mfOwner,
		result: "loss", decisions: 40, legal: 30, tokens: 2_000, estCost: 0.02, durationSec: 30,
		declaredProvider: "google", declaredModel: declaredName,
	})
	writeRating(t, pool, mfAg, "goofspiel", season, 1480, 0, 1, 0)

	// ── a match OUTSIDE the season window, which must not be counted anywhere ────
	writeBenchFixture(t, pool, benchFixture{
		matchID: "m-old-" + run, game: "mafia", agentPub: gwAg, ownerID: gwOwner,
		result: "win", decisions: 999, legal: 999, tokens: 999_000, estCost: 9.99, durationSec: 60,
		verifiedProvider: "anthropic", verifiedModel: verifiedName, verifiedCost: 9.99,
		finishedAt: time.Now().Add(-90 * 24 * time.Hour),
	})

	models, err := rr.ModelBenchmark(ctx, season, "", start, end)
	if err != nil {
		t.Fatalf("ModelBenchmark: %v", err)
	}

	// ── attribution: the provider's confirmation beats the SDK's and the manifest's
	gw, ok := findModel(models, verifiedName)
	if !ok {
		t.Fatalf("gateway-verified model missing; got %+v", modelNames(models))
	}
	if gw.AttrRank != 1 {
		t.Errorf("verified model AttrRank = %d, want 1", gw.AttrRank)
	}
	if _, found := findModel(models, sdkClaim); found {
		t.Error("a gateway-verified match must not ALSO be counted under the SDK's model name")
	}
	if _, found := findModel(models, gwClaim); found {
		t.Error("a match must never be attributed to a manifest claim the gateway contradicted")
	}

	sdk, ok := findModel(models, observedName)
	if !ok {
		t.Fatalf("SDK-observed model missing; got %+v", modelNames(models))
	}
	if sdk.AttrRank != 2 {
		t.Errorf("observed model AttrRank = %d, want 2", sdk.AttrRank)
	}
	if _, found := findModel(models, sdkManifest); found {
		t.Error("the manifest claim must not win when the SDK reported the real model")
	}

	mf, ok := findModel(models, declaredName)
	if !ok {
		t.Fatalf("manifest-declared model missing; got %+v", modelNames(models))
	}
	if mf.AttrRank != 3 {
		t.Errorf("declared model AttrRank = %d, want 3", mf.AttrRank)
	}

	// ── the season window is a real filter, not decoration ──────────────────────
	// Three in-window matches for the verified agent; the 999k-token ancient one is out.
	if gw.Matches != 3 {
		t.Errorf("verified model matches = %d, want 3 (the 90-day-old match is out of window)", gw.Matches)
	}
	if gw.Tokens != 30_000 {
		t.Errorf("verified model tokens = %d, want 30000 — an out-of-window match leaked in", gw.Tokens)
	}

	// ── outcomes come from per-match results ────────────────────────────────────
	if gw.Wins != 2 || gw.Losses != 1 || gw.Ties != 0 {
		t.Errorf("verified model W/L/T = %d/%d/%d, want 2/1/0", gw.Wins, gw.Losses, gw.Ties)
	}

	// ── economics ───────────────────────────────────────────────────────────────
	if gw.VerifiedCostUSD < 1.49 || gw.VerifiedCostUSD > 1.51 {
		t.Errorf("verified cost = %.4f, want 3 × 0.50", gw.VerifiedCostUSD)
	}
	if gw.VerifiedCalls != 3 {
		t.Errorf("verified calls = %d, want 3", gw.VerifiedCalls)
	}
	if gw.EstCostUSD < 0.29 || gw.EstCostUSD > 0.31 {
		t.Errorf("self-reported cost = %.4f, want 3 × 0.10 (kept alongside verified, not replaced)", gw.EstCostUSD)
	}

	// ── wall-clock: every match ran 60s, so 3 timed matches and 180s of play ─────
	if gw.TimedMatches != 3 || gw.PlaySeconds < 179 || gw.PlaySeconds > 181 {
		t.Errorf("timed=%d playSeconds=%.1f, want 3 / ~180", gw.TimedMatches, gw.PlaySeconds)
	}

	// ── decisions and latency range ─────────────────────────────────────────────
	if gw.Decisions != 300 || gw.Legal != 294 {
		t.Errorf("decisions=%d legal=%d, want 300/294", gw.Decisions, gw.Legal)
	}
	if gw.AvgLatencyMs != 400 {
		t.Errorf("avg latency = %dms, want 400 (latency_sum ÷ decisions)", gw.AvgLatencyMs)
	}
	if gw.MinLatencyMs != 100 || gw.MaxLatencyMs != 900 {
		t.Errorf("latency range = %d..%dms, want 100..900", gw.MinLatencyMs, gw.MaxLatencyMs)
	}

	// ── the per-arena breakdown: 2 Mafia + 1 Monopoly, split correctly ───────────
	byArena := map[string]rating.ArenaStat{}
	for _, a := range gw.Arenas {
		byArena[a.Game] = a
	}
	if len(gw.Arenas) != 2 {
		t.Fatalf("arenas = %+v, want mafia + monopoly", gw.Arenas)
	}
	if m := byArena["mafia"]; m.Matches != 2 || m.Wins != 2 || m.Tokens != 20_000 {
		t.Errorf("mafia arena = %d matches / %dW / %d tokens, want 2/2/20000", m.Matches, m.Wins, m.Tokens)
	}
	if m := byArena["monopoly"]; m.Matches != 1 || m.Losses != 1 || m.Tokens != 10_000 {
		t.Errorf("monopoly arena = %d matches / %dL / %d tokens, want 1/1/10000", m.Matches, m.Losses, m.Tokens)
	}
	// Arena rows must sum back to the aggregate, or one of the two is wrong.
	var sum int64
	for _, a := range gw.Arenas {
		sum += a.Tokens
	}
	if sum != gw.Tokens {
		t.Errorf("arena tokens sum to %d but the aggregate says %d", sum, gw.Tokens)
	}

	// ── ELO/coins come from ratings, per (agent, arena) ──────────────────────────
	// 1600 (mafia) and 1400 (monopoly) ⇒ mean 1500.
	if gw.AvgElo != 1500 {
		t.Errorf("avg elo = %d, want 1500 (mean of the agent's two arena ratings)", gw.AvgElo)
	}
	if gw.CoinsWon != 500 {
		t.Errorf("coins = %d, want 500", gw.CoinsWon)
	}
	if gw.Agents != 1 {
		t.Errorf("agents = %d, want 1 distinct agent across both arenas", gw.Agents)
	}

	// ── narrowing to one arena drops the others entirely ─────────────────────────
	mafiaOnly, err := rr.ModelBenchmark(ctx, season, "mafia", start, end)
	if err != nil {
		t.Fatalf("ModelBenchmark(mafia): %v", err)
	}
	gwMafia, ok := findModel(mafiaOnly, verifiedName)
	if !ok {
		t.Fatalf("verified model missing from the mafia board; got %+v", modelNames(mafiaOnly))
	}
	if gwMafia.Matches != 2 || gwMafia.Tokens != 20_000 {
		t.Errorf("mafia-only = %d matches / %d tokens, want 2/20000", gwMafia.Matches, gwMafia.Tokens)
	}
	if _, found := findModel(mafiaOnly, observedName); found {
		t.Error("a Goofspiel-only model appeared on the Mafia board")
	}
}

// A house bot must never appear on the model board: it is platform furniture, and
// counting it would let the operator's own bots set the ranking.
func TestModelBenchmarkExcludesHouseAgents(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	season, run := isolate()
	start, end := time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour)
	houseModel := "house-bot-model-" + run

	houseAg, houseOw := mkUserAgent(t, pool, "bmhouse-"+run)
	if _, err := pool.Exec(ctx, `UPDATE agents SET kind = 'house' WHERE public_id = $1`, houseAg); err != nil {
		t.Fatalf("mark house: %v", err)
	}
	var ownerID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE public_id = $1`, houseOw).Scan(&ownerID); err != nil {
		t.Fatalf("owner: %v", err)
	}
	writeBenchFixture(t, pool, benchFixture{
		matchID: "m-house-" + run, game: "mafia", agentPub: houseAg, ownerID: ownerID,
		result: "win", decisions: 10, legal: 10, tokens: 1_000, durationSec: 10,
		verifiedProvider: "anthropic", verifiedModel: houseModel, verifiedCost: 0.01,
	})
	writeRating(t, pool, houseAg, "mafia", season, 2000, 1, 0, 10)

	models, err := rr(pool).ModelBenchmark(ctx, season, "", start, end)
	if err != nil {
		t.Fatalf("ModelBenchmark: %v", err)
	}
	if _, found := findModel(models, houseModel); found {
		t.Fatalf("a house agent's model reached the public board: %+v", modelNames(models))
	}
}

func rr(pool *pgxpool.Pool) *RatingRepo { return NewRatingRepo(pool) }

func modelNames(models []rating.ModelStat) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, fmt.Sprintf("%s/%s(rank=%d)", m.Provider, m.Model, m.AttrRank))
	}
	return out
}
