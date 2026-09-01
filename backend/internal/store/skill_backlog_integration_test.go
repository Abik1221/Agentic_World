package store

import (
	"context"
	"testing"

	"github.com/agent-arena/arena/internal/skill"
)

// NextUnscored asks its question in TWO queries so each can match a partial index (see
// skill_repo.go and migration 0096; the single-OR version cost 4.7 GB a batch). Splitting a
// predicate is only safe if the halves still cover exactly what the whole did, so that is
// what this pins — against a real Postgres, because the property is about SQL semantics.
//
// Two ways this could break silently, both of which a "does it return rows" test would miss:
// a GAP loses decisions from the backlog forever (they are never scored and nothing reports
// it), and an OVERLAP scores the same decision twice per batch, halving throughput while
// looking healthy.
func TestScorerBacklogPhasesPartitionTheEligibleSet(t *testing.T) {
	pool, ctx := moneyPathPool(t)

	// The version the repo actually uses, so this tracks a bump rather than a copy of it.
	v := skill.ScorerVersion

	var whole, phaseA, phaseB, both, originalOr int64
	// Built from the repo's OWN predicate constants, so a change to either phase in
	// skill_repo.go is evaluated here rather than compared against a restatement of it.
	// This is the difference between guarding the code and describing it.
	q := `SELECT
		  (SELECT count(*) FROM agent_match_decisions d
		    WHERE d.input_json IS NOT NULL
		      AND ((` + scorerPhaseNeverScored + `) OR (` + scorerPhaseStaleScore + `))),
		  (SELECT count(*) FROM agent_match_decisions d
		    WHERE d.input_json IS NOT NULL AND (` + scorerPhaseNeverScored + `)),
		  (SELECT count(*) FROM agent_match_decisions d
		    WHERE d.input_json IS NOT NULL AND (` + scorerPhaseStaleScore + `)),
		  (SELECT count(*) FROM agent_match_decisions d
		    WHERE d.input_json IS NOT NULL
		      AND (` + scorerPhaseNeverScored + `) AND (` + scorerPhaseStaleScore + `)),
		  -- The condition the split replaced, spelled out, so the phases are compared
		  -- against the original rather than only against each other.
		  (SELECT count(*) FROM agent_match_decisions d
		    WHERE d.input_json IS NOT NULL
		      AND (d.skill_scorer_version IS NULL OR d.skill_scorer_version < $1))`
	// ONE query for all five counts, and that is not tidiness. The scorer is running while
	// this test runs, so the backlog shrinks between statements: taking the comparison count
	// separately failed by 466 rows purely because 466 decisions were scored in between.
	// A single statement sees a single snapshot.
	err := pool.QueryRow(ctx, q, v).Scan(&whole, &phaseA, &phaseB, &both, &originalOr)
	if err != nil {
		t.Fatalf("count eligible decisions: %v", err)
	}

	if phaseA+phaseB != whole {
		t.Fatalf("phases cover %d + %d = %d rows but the original condition covers %d — a gap "+
			"means decisions are dropped from the backlog and never scored, an excess means "+
			"they are scored twice",
			phaseA, phaseB, phaseA+phaseB, whole)
	}
	// The phases must also equal the ORIGINAL single-OR condition they replaced. Without
	// this they could drift together and still look self-consistent.
	if whole != originalOr {
		t.Fatalf("the two phases together cover %d rows, the condition they replaced covers %d "+
			"— the split changed WHICH decisions are eligible for scoring", whole, originalOr)
	}

	if both != 0 {
		t.Fatalf("%d rows satisfy BOTH phases; the phases must be disjoint or every batch "+
			"does duplicate work", both)
	}
}

// The repo's own method must return a batch no larger than asked for, including when it
// tops up from the second phase. An off-by-one there would silently hand the scorer more
// work than it budgeted for.
func TestNextUnscoredRespectsTheLimitAcrossBothPhases(t *testing.T) {
	pool, ctx := moneyPathPool(t)
	repo := NewSkillRepo(pool)

	for _, limit := range []int{1, 7, 50} {
		got, err := repo.NextUnscored(ctx, limit)
		if err != nil {
			t.Fatalf("NextUnscored(%d): %v", limit, err)
		}
		if len(got) > limit {
			t.Fatalf("NextUnscored(%d) returned %d rows — the top-up phase must be bounded by "+
				"what the first phase left unfilled", limit, len(got))
		}
		// Every row must actually be eligible; a phase predicate that drifted from the
		// index it targets could return already-scored work.
		for _, d := range got {
			var ver *int
			if err := pool.QueryRow(context.Background(),
				`SELECT skill_scorer_version FROM agent_match_decisions
				  WHERE match_id=$1 AND agent_id=$2 AND seq=$3`,
				d.MatchID, d.AgentID, d.Seq).Scan(&ver); err != nil {
				t.Fatalf("re-read decision: %v", err)
			}
			if ver != nil && *ver >= skill.ScorerVersion {
				t.Fatalf("NextUnscored returned a decision already scored at version %d "+
					"(current %d)", *ver, skill.ScorerVersion)
			}
		}
	}
}
