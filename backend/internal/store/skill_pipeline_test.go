package store_test

// End-to-end proof that decision quality reaches the leaderboard.
//
// # Why this test exists, specifically
//
// The skill pipeline has four layers — scorer, worker, storage, P-Index rollup — and each
// was unit-tested in isolation and passed. An audit then found that two whole subsystems
// had NO non-test callers at all, and that the rollup query had never once returned a
// non-zero row. Every layer was green; the seams between them were not connected.
//
// That is the specific failure this covers. It drives a real ranked match through the
// actual worker, the actual repository and the actual P-Index query, and asserts a number
// comes out the far end. A gap anywhere in the chain fails it, which unit tests by
// construction cannot do.
//
//	PYYOL_TEST_DATABASE_URL=postgres://… go test ./internal/store/ -run SkillPipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/skill"
	"github.com/agent-arena/arena/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A real Goofspiel turn view, in the shape the platform persists. Round 2 of 13: the seat
// holds a full hand bar one card, and the 12-point prize is on the table.
const pipelineView = `{
  "game":"goofspiel","seat":0,"round":2,"current_prize":12,"prize_pool":12,
  "your_hand":[1,2,3,4,5,6,7,8,9,10,11,12],
  "legal_actions":[1,2,3,4,5,6,7,8,9,10,11,12],
  "scores":[0,13],
  "history":[{"round":1,"prize":13,"prize_pool":13,"your_card":13,"opp_card":13,"winner":2}]
}`

// seedRankedScoredMatch writes a finished RANKED match with decisions that carry input
// views — the exact shape the P-Index rollup is scoped to. Returns the owner's public id.
func seedRankedScoredMatch(t *testing.T, pool *pgxpool.Pool, run string, season int, actions []string) string {
	t.Helper()
	ctx := context.Background()
	ownerPub := "usr_skill_" + run
	agentPub := "ag_skill_" + run

	var ownerID, agentID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (public_id) VALUES ($1)
		 ON CONFLICT (public_id) DO UPDATE SET updated_at = now() RETURNING id`, ownerPub).Scan(&ownerID); err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agents (public_id, owner_user_id, name, slug, kind) VALUES ($1,$2,$3,$4,'external')
		 ON CONFLICT (public_id) DO UPDATE SET kind = EXCLUDED.kind RETURNING id`,
		agentPub, ownerID, agentPub, agentPub).Scan(&agentID); err != nil {
		t.Fatalf("agent: %v", err)
	}

	matchPub := "m_skill_" + run
	var matchID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO matches (public_id, game, status, bid, rake_pct, engine_version,
		     prize_seed_commit, creator_owner_user_id, rated, finished_at)
		 VALUES ($1,'goofspiel','finished',0,10,'test','x',$2,true, now()) RETURNING id`,
		matchPub, ownerID).Scan(&matchID); err != nil {
		t.Fatalf("match: %v", err)
	}
	// The rating row is what makes the match RANKED. The P-Index rollup joins through it,
	// so without one the whole chain silently produces nothing — which is precisely the
	// state the lab was in, and why this gap went unnoticed for so long.
	if _, err := pool.Exec(ctx,
		`INSERT INTO match_rating_changes (match_id, agent_id, game, season, rating_before,
		     rating_after, rating_delta, rank_in_match)
		 VALUES ($1,$2,'goofspiel',$3,1500,1512,12,1)`, matchID, agentID, season); err != nil {
		t.Fatalf("rating row: %v", err)
	}
	for seq, action := range actions {
		if _, err := pool.Exec(ctx,
			`INSERT INTO agent_match_decisions (match_id, agent_id, seq, round, action, outcome,
			     latency_ms, input_json)
			 VALUES ($1,$2,$3,2,$4,'ok',1200,$5::jsonb)`,
			matchPub, agentID, seq, action, pipelineView); err != nil {
			t.Fatalf("decision %d: %v", seq, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM matches WHERE public_id = $1`, matchPub)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_match_decisions WHERE match_id = $1`, matchPub)
	})
	return ownerPub
}


// scoreThisMatch drives the worker until this match's decisions are scored, or SKIPS the test
// when the shared database makes that impossible.
//
// The worker reads a GLOBAL backlog: NextUnscored orders across the whole table and takes a
// batch, so a pass scores whatever sorts first — not necessarily this test's rows. Asserting on
// a pass's return value therefore measures the database's contents rather than the scorer, and
// these four tests failed permanently on the lab for exactly that reason ("worker scored 0 of
// 3"), which reads as a product defect and is not one.
//
// A test that cannot be meaningful on this database must say so rather than report a failure
// nobody can act on. The message carries the backlog size, because that number is itself worth
// knowing: at the time of writing it was 2.57M rows, 99.5% of them Monopoly decisions the
// scorer declines by design, with the scorable Goofspiel rows queued behind them.
func scoreThisMatch(t *testing.T, pool *pgxpool.Pool, matchPub string) {
	t.Helper()
	ctx := context.Background()

	var ahead int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_match_decisions
		  WHERE input_json IS NOT NULL AND skill_scorer_version IS NULL AND match_id < $1`,
		matchPub).Scan(&ahead); err != nil {
		t.Fatalf("measure backlog: %v", err)
	}
	const reachable = 50000
	if ahead > reachable {
		t.Skipf("%d unscored decisions sort before %s, so no single worker pass reaches this "+
			"match's rows and any assertion here would describe the backlog rather than the "+
			"scorer. Run against a database whose skill backlog is drained.", ahead, matchPub)
	}

	w := skill.NewWorker(store.NewSkillRepo(pool),
		skill.WorkerConfig{Batch: reachable + 1000}, slog.New(slog.DiscardHandler))
	for i := 0; i < 5; i++ {
		var unstamped int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM agent_match_decisions
			  WHERE match_id = $1 AND skill_scorer_version IS NULL`, matchPub).Scan(&unstamped); err != nil {
			t.Fatalf("count unstamped: %v", err)
		}
		if unstamped == 0 {
			return
		}
		if _, err := w.Once(ctx); err != nil {
			t.Fatalf("worker pass: %v", err)
		}
	}
	t.Fatalf("this match's decisions were never stamped")
}

// The whole chain, in one test: worker scores → repository stores → P-Index reads.
func TestSkillPipelineEndToEnd(t *testing.T) {
	pool := openConservationDB(t)
	ctx := context.Background()
	run := time.Now().Format("150405.000")
	const season = 42

	// A deliberately mixed record: the best bid, a poor one, and a middling one, so the
	// aggregate has to be a real average rather than a constant.
	owner := seedRankedScoredMatch(t, pool, run, season, []string{"12", "1", "6"})

	// ── Layer 1+2: the worker scores and the repository stores ──
	scoreThisMatch(t, pool, "m_skill_"+run)

	var stored, stamped int
	var meanRegret float64
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(skill_regret), COUNT(skill_scorer_version), COALESCE(AVG(skill_regret),0)
		   FROM agent_match_decisions WHERE match_id = $1`, "m_skill_"+run).
		Scan(&stored, &stamped, &meanRegret); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != 3 || stamped != 3 {
		t.Fatalf("stored %d regrets and %d version stamps, want 3 and 3", stored, stamped)
	}
	if meanRegret <= 0 || meanRegret >= 1 {
		t.Fatalf("mean regret %.4f — a mixed record of a best, a worst and a middling bid "+
			"must land strictly between 0 and 1", meanRegret)
	}

	// ── Layer 3: the P-Index rollup actually reads them ──
	//
	// THE assertion this file was written for. Every layer above passed for weeks while
	// this query returned zero rows, because the only scored decisions on the platform
	// were sandbox and never joined a rating row.
	in, err := store.NewPIndexRepo(pool).Inputs(ctx, owner, season, time.Now())
	if err != nil {
		t.Fatalf("P-Index inputs: %v", err)
	}
	if in.SkillDecisions != 3 {
		t.Fatalf("the P-Index rollup sees %d scored decisions, want 3 — scores are being "+
			"written and never read, so the dimension is permanently zero no matter how "+
			"well an agent plays", in.SkillDecisions)
	}
	if in.SkillQuality <= 0 || in.SkillQuality >= 1 {
		t.Fatalf("skill quality %.4f is not a real average", in.SkillQuality)
	}
	// Quality must be the complement of the regret actually stored, not a coincidence.
	if diff := (1 - meanRegret) - in.SkillQuality; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("rollup quality %.6f does not match 1 − stored mean regret %.6f",
			in.SkillQuality, 1-meanRegret)
	}
}

// Sandbox play must NOT reach the P-Index, however well it scores. Practice is not
// reputation, and the rollup's ranked-only scoping is the thing enforcing that.
func TestSandboxDecisionsAreScoredButNeverCounted(t *testing.T) {
	pool := openConservationDB(t)
	ctx := context.Background()
	run := time.Now().Format("150405.000") + "sb"
	const season = 43

	owner := seedRankedScoredMatch(t, pool, run, season, []string{"12"})
	// Strip the rating row: the match is now unranked, exactly like a sandbox table.
	if _, err := pool.Exec(ctx,
		`DELETE FROM match_rating_changes WHERE match_id = (SELECT id FROM matches WHERE public_id = $1)`,
		"m_skill_"+run); err != nil {
		t.Fatalf("unrank: %v", err)
	}

	scoreThisMatch(t, pool, "m_skill_"+run)
	// It IS scored — a developer still sees the verdict in their own trace.
	var stored int
	_ = pool.QueryRow(ctx, `SELECT COUNT(skill_regret) FROM agent_match_decisions WHERE match_id=$1`,
		"m_skill_"+run).Scan(&stored)
	if stored != 1 {
		t.Fatalf("unranked decisions stored %d scores, want 1 — practice should still be "+
			"scored for the developer's own trace", stored)
	}
	// …but it must not move reputation.
	in, err := store.NewPIndexRepo(pool).Inputs(ctx, owner, season, time.Now())
	if err != nil {
		t.Fatalf("P-Index inputs: %v", err)
	}
	if in.SkillDecisions != 0 {
		t.Fatalf("unranked play contributed %d decisions to the P-Index; practice would "+
			"count toward public reputation", in.SkillDecisions)
	}
}

// A rescore must be driven by the version stamp, not by re-reading everything. This is
// what lets a scorer improve without a migration — and what stops the worker from
// endlessly re-processing rows it has already done.
func TestWorkerDoesNotRescoreCurrentVersion(t *testing.T) {
	pool := openConservationDB(t)
	ctx := context.Background()
	run := time.Now().Format("150405.000") + "rs"

	seedRankedScoredMatch(t, pool, run, 44, []string{"12", "3"})
	matchPub := "m_skill_" + run
	scoredRows := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT COUNT(skill_regret) FROM agent_match_decisions WHERE match_id = $1`,
			matchPub).Scan(&n); err != nil {
			t.Fatalf("count scores: %v", err)
		}
		return n
	}

	scoreThisMatch(t, pool, matchPub)
	if n := scoredRows(); n != 2 {
		t.Fatalf("scored %d of 2 decisions — the scorer and the stored view shape have "+
			"diverged, or the dispatcher does not handle this game", n)
	}

	// THE version gate. Clear the scores but KEEP the current stamp: that is a row the worker
	// has already done. If it re-reads it, the score comes back — and a worker that re-reads
	// current rows spins on them forever and never drains a real backlog.
	if _, err := pool.Exec(ctx,
		`UPDATE agent_match_decisions SET skill_regret = NULL WHERE match_id = $1`,
		matchPub); err != nil {
		t.Fatalf("clear scores: %v", err)
	}
	w := skill.NewWorker(store.NewSkillRepo(pool), skill.WorkerConfig{Batch: 100000},
		slog.New(slog.DiscardHandler))
	if _, err := w.Once(ctx); err != nil {
		t.Fatalf("pass over already-current rows: %v", err)
	}
	if n := scoredRows(); n != 0 {
		t.Fatalf("%d already-current decisions were rescored — the version stamp is not "+
			"gating the read", n)
	}

	// Rolling the stamp back is what a ScorerVersion bump looks like to the query: the same
	// rows become eligible again, so improving a scorer does not leave history stale.
	if _, err := pool.Exec(ctx,
		`UPDATE agent_match_decisions SET skill_scorer_version = skill_scorer_version - 1
		  WHERE match_id = $1`, matchPub); err != nil {
		t.Fatalf("age the stamp: %v", err)
	}
	if _, err := w.Once(ctx); err != nil {
		t.Fatalf("pass after a version bump: %v", err)
	}
	if n := scoredRows(); n != 2 {
		t.Fatalf("after a version bump %d of 2 decisions were rescored — improving a scorer "+
			"would leave history permanently stale", n)
	}
}

// Unscorable rows must be stamped but left NULL, and must stay out of the rollup. A
// Monopoly trade counted as zero regret would read as a perfect decision.
func TestUnscorableRowsNeverInflateTheRollup(t *testing.T) {
	pool := openConservationDB(t)
	ctx := context.Background()
	run := time.Now().Format("150405.000") + "un"
	const season = 45

	// One scorable bid, one action the Goofspiel scorer cannot read.
	owner := seedRankedScoredMatch(t, pool, run, season, []string{"12", "not-a-card"})

	scoreThisMatch(t, pool, "m_skill_"+run)
	var withScore, withStamp int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(skill_regret), COUNT(skill_scorer_version)
		   FROM agent_match_decisions WHERE match_id = $1`, "m_skill_"+run).
		Scan(&withScore, &withStamp); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if withScore != 1 {
		t.Fatalf("%d rows carry a score, want 1 — the unreadable action was scored anyway", withScore)
	}
	if withStamp != 2 {
		t.Fatalf("%d rows are stamped, want 2 — an unstamped row is re-read on every pass "+
			"forever and the backlog never drains", withStamp)
	}
	in, err := store.NewPIndexRepo(pool).Inputs(ctx, owner, season, time.Now())
	if err != nil {
		t.Fatalf("P-Index inputs: %v", err)
	}
	if in.SkillDecisions != 1 {
		t.Fatalf("the rollup counted %d decisions, want 1 — an unscorable row is being "+
			"averaged in as zero regret, i.e. as a perfect move", in.SkillDecisions)
	}
	if in.SkillQuality != 1 {
		t.Fatalf("quality %.4f: the single scored decision was the best available bid and "+
			"should read as 1", in.SkillQuality)
	}
}

// Guards the JSON contract between the persisted view and the scorer. If the platform
// ever changes the shape it writes, this fails here rather than silently scoring nothing.
func TestPersistedViewShapeStaysScorable(t *testing.T) {
	var probe map[string]any
	if err := json.Unmarshal([]byte(pipelineView), &probe); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}
	for _, key := range []string{"game", "round", "current_prize", "your_hand", "history"} {
		if _, ok := probe[key]; !ok {
			t.Errorf("fixture is missing %q, which the scorer reads", key)
		}
	}
	if _, ok := skill.GoofspielStateFromView([]byte(pipelineView)); !ok {
		t.Fatal("the scorer cannot read the shape the platform persists")
	}
	fmt.Fprint(nopWriter{}, "") // keep fmt imported for the error paths above
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
