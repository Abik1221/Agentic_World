package store

import (
	"context"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/devtrace"
)

// CROSS-MATCH AGENT TELEMETRY, against real Postgres.
//
// Two properties, and the second is the one that would ship broken:
//
//  1. The rollup is arithmetically right — percentiles, failure taxonomy, hotspots.
//  2. It is SCOPED. An aggregate that forgets its filter returns a summary of every agent
//     on the platform and looks entirely plausible doing it. There is no number a reviewer
//     could look at and tell. So the isolation assertions live here, next to the query.

func TestAgentTelemetryLive(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	repo := NewPIndexRepo(pool)
	trace := NewDevTraceRepo(pool)
	_, run := isolate()

	mine, _ := mkUserAgent(t, pool, "tel-mine-"+run)
	theirs, _ := mkUserAgent(t, pool, "tel-theirs-"+run)

	// Two matches for MY agent, in two arenas, with a deliberate failure pattern:
	// round 3 of mafia fails repeatedly; goofspiel is clean but slow once.
	mkMatch := func(pub, game string, owner string) {
		var uid int64
		if err := pool.QueryRow(ctx, `SELECT owner_user_id FROM agents WHERE public_id = $1`, owner).Scan(&uid); err != nil {
			t.Fatalf("owner of %s: %v", owner, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO matches (public_id, game, status, bid, engine_version, prize_seed_commit,
			                      creator_owner_user_id, started_at, finished_at)
			 VALUES ($1,$2,'finished',100,'tel','tel',$3, now() - interval '5 minutes', now())
			 ON CONFLICT (public_id) DO NOTHING`, pub, game, uid); err != nil {
			t.Fatalf("match %s: %v", pub, err)
		}
	}
	mafia := "m_tel_mafia_" + run
	goof := "m_tel_goof_" + run
	other := "m_tel_other_" + run
	mkMatch(mafia, "mafia", mine)
	mkMatch(goof, "goofspiel", mine)
	mkMatch(other, "mafia", theirs)

	now := time.Now().UTC()
	// Mafia: 10 decisions, rounds 1-2 clean, round 3 fails 3 of 4 times.
	var mafiaDecs []MatchDecision
	for i := 0; i < 10; i++ {
		// round is assigned by the switch below on every branch; declaring it without a
		// value keeps the initial 1 from reading as meaningful (ineffassign).
		var round int
		outcome, lat := "ok", int64(300)
		switch {
		case i < 3:
			round = 1
		case i < 6:
			round = 2
		default:
			round = 3
			if i < 9 {
				outcome, lat = "timeout", 5000
			}
		}
		mafiaDecs = append(mafiaDecs, MatchDecision{
			Seq: i, Round: round, Action: "vote", Outcome: outcome, LatencyMS: lat,
			TotalTokens: 500, EstimatedCost: 0.002, StartedAt: now.Add(time.Duration(i) * time.Second),
			Rationale: "because",
		})
	}
	if err := repo.RecordMatchDecisions(ctx, mafia, mine, mafiaDecs); err != nil {
		t.Fatal(err)
	}
	// Goofspiel: 5 clean decisions, one very slow — this must set the max and the p99.
	var goofDecs []MatchDecision
	for i := 0; i < 5; i++ {
		lat := int64(200)
		if i == 4 {
			lat = 9000
		}
		goofDecs = append(goofDecs, MatchDecision{
			Seq: i, Round: i + 1, Action: "9", Outcome: "ok", LatencyMS: lat,
			TotalTokens: 400, EstimatedCost: 0.001, StartedAt: now.Add(time.Duration(i) * time.Second),
		})
	}
	if err := repo.RecordMatchDecisions(ctx, goof, mine, goofDecs); err != nil {
		t.Fatal(err)
	}
	// Somebody ELSE's agent, with a loud failure pattern that must never appear in mine.
	var theirDecs []MatchDecision
	for i := 0; i < 40; i++ {
		theirDecs = append(theirDecs, MatchDecision{
			Seq: i, Round: 7, Action: "vote", Outcome: "illegal_move", LatencyMS: 12345,
			TotalTokens: 99999, EstimatedCost: 9.99, Rationale: "THEIR REASONING",
			StartedAt: now,
		})
	}
	if err := repo.RecordMatchDecisions(ctx, other, theirs, theirDecs); err != nil {
		t.Fatal(err)
	}

	since := now.Add(-24 * time.Hour)
	got, err := trace.AgentTelemetry(ctx, []string{mine}, since)
	if err != nil {
		t.Fatalf("AgentTelemetry: %v", err)
	}

	// ── totals ──────────────────────────────────────────────────────────────────
	if got.Matches != 2 {
		t.Errorf("matches = %d, want 2", got.Matches)
	}
	if got.Decisions != 15 {
		t.Errorf("decisions = %d, want 15 (10 mafia + 5 goofspiel)", got.Decisions)
	}
	if got.Failures != 3 {
		t.Errorf("failures = %d, want 3 timeouts", got.Failures)
	}
	if got.LegalRate < 0.79 || got.LegalRate > 0.81 {
		t.Errorf("legal rate = %.3f, want 12/15", got.LegalRate)
	}
	// The tail is the point: the 9s goofspiel turn must set the max, and p50 must NOT be
	// dragged up by it.
	if got.MaxMs != 9000 {
		t.Errorf("max = %dms, want 9000", got.MaxMs)
	}
	if got.P50Ms > 1000 {
		t.Errorf("p50 = %dms — a mean would be dragged up by the tail; a median must not be", got.P50Ms)
	}
	if got.P99Ms < got.P50Ms {
		t.Errorf("p99 (%d) < p50 (%d)", got.P99Ms, got.P50Ms)
	}
	if got.Tokens != 10*500+5*400 {
		t.Errorf("tokens = %d, want 7000", got.Tokens)
	}

	// ── failure taxonomy ────────────────────────────────────────────────────────
	if len(got.FailuresByCause) != 1 || got.FailuresByCause[0].Outcome != "timeout" ||
		got.FailuresByCause[0].Count != 3 {
		t.Errorf("failures by cause = %+v, want one timeout×3", got.FailuresByCause)
	}

	// ── per arena, never averaged away ──────────────────────────────────────────
	byArena := map[string]int{}
	for _, a := range got.Arenas {
		byArena[a.Game] = a.Decisions
	}
	if byArena["mafia"] != 10 || byArena["goofspiel"] != 5 {
		t.Errorf("arena split = %+v, want mafia 10 / goofspiel 5", byArena)
	}

	// ── the hotspot: round 3 of mafia, the strongest signal on the page ─────────
	var found bool
	for _, h := range got.Hotspots {
		if h.Game == "mafia" && h.Round == 3 {
			found = true
			if h.Failures != 3 || h.Decisions != 4 {
				t.Errorf("hotspot = %+v, want 3 failures of 4 decisions", h)
			}
		}
	}
	if !found {
		t.Errorf("round 3 of mafia fails 3 of 4 times and is not a hotspot: %+v", got.Hotspots)
	}
	// The noise floor: a round with too few decisions must NOT rank, however bad its
	// rate. Round 5 of goofspiel is a single clean decision; a 1-of-1 failure elsewhere
	// would be a 100%% rate and would otherwise top this list forever.
	for _, h := range got.Hotspots {
		if h.Decisions < 3 {
			t.Errorf("hotspot %+v has too small a sample to mean anything", h)
		}
	}

	// ── ISOLATION: none of the other developer's record may appear ───────────────
	if got.Decisions > 15 || got.Tokens > 7000 || got.CostUSD > 0.03 {
		t.Fatalf("LEAK: another agent's decisions are in this rollup (decisions=%d tokens=%d cost=%.2f)",
			got.Decisions, got.Tokens, got.CostUSD)
	}
	for _, f := range got.FailuresByCause {
		if f.Outcome == "illegal_move" {
			t.Fatal("LEAK: the other developer's illegal-move failures reached this rollup")
		}
	}
	for _, h := range got.Hotspots {
		if h.Round == 7 {
			t.Fatal("LEAK: the other developer's round-7 hotspot reached this rollup")
		}
	}

	// ── worst decisions, and their isolation ────────────────────────────────────
	slowest, failures, err := trace.AgentWorstDecisions(ctx, []string{mine}, since, 10)
	if err != nil {
		t.Fatalf("AgentWorstDecisions: %v", err)
	}
	if len(slowest) == 0 || slowest[0].LatencyMS != 9000 {
		t.Errorf("slowest[0] = %+v, want the 9s turn first", slowest)
	}
	if len(failures) != 3 {
		t.Errorf("recent failures = %d, want 3", len(failures))
	}
	for _, group := range [][]devtrace.WorstDecision{slowest, failures} {
		for _, w := range group {
			if w.MatchID == other || w.Rationale == "THEIR REASONING" {
				t.Fatalf("LEAK: another developer's decision surfaced: %+v", w)
			}
		}
	}
	// Every row carries what the UI needs to link to its inspector.
	for _, w := range slowest {
		if w.MatchID == "" {
			t.Error("a worst-decision row cannot be linked without its match id")
		}
	}

	// ── the empty owned-set must match nothing, never everything ────────────────
	empty, err := trace.AgentTelemetry(ctx, nil, since)
	if err != nil || empty.Decisions != 0 {
		t.Fatalf("empty owned-set returned %d decisions (err=%v) — an unscoped aggregate "+
			"would summarise the whole platform here", empty.Decisions, err)
	}
	s2, f2, err := trace.AgentWorstDecisions(ctx, nil, since, 10)
	if err != nil || len(s2) != 0 || len(f2) != 0 {
		t.Fatalf("empty owned-set returned %d/%d worst rows", len(s2), len(f2))
	}
}
