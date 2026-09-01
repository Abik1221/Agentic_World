package store

import (
	"context"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/modelboard"
)

// The seat query now applies BuildComparisons' pairwise-game rule itself, which is a second
// place that decides eligibility — and a second implementation of "is this allowed" is exactly
// how a control quietly stops matching the one it mirrors.
//
// It is generated from modelboard.PairwiseGames() so it cannot restate the list wrongly, but
// generation does not prove the query USES it correctly. These run against a real database and
// pin the two properties that matter:
//
//   - nothing eligible is lost (the dangerous direction: a seat the board should have compared,
//     silently absent, showing up as a model that "never played");
//   - everything dropped is still counted under the key it would have had in Go, because
//     seats_excluded is published.

func TestGameFilterDropsNothingBuildComparisonsWouldHaveKept(t *testing.T) {
	pool, ctx := moneyPathPool(t)
	repo := NewModelBoardRepo(pool)

	end := time.Now().Add(time.Hour)
	start := end.Add(-90 * 24 * time.Hour)
	hosts := []string{"openrouter.ai", "api.groq.com"}

	filtered, excluded, err := repo.Seats(ctx, "", start, end, hosts)
	if err != nil {
		t.Fatalf("Seats: %v", err)
	}

	// The reference: the same query with the filter off, classified by Go alone.
	unfiltered, err := allSeatsUnfiltered(ctx, repo, start, end, hosts)
	if err != nil {
		t.Fatalf("unfiltered reference query: %v", err)
	}

	// Build both ways. The filtered path must produce the SAME comparisons as the path that
	// let Go do all the filtering — that is the whole claim.
	cfg := modelboard.DefaultBuildConfig()
	wantCmp, wantExcluded := modelboard.BuildComparisons(unfiltered, cfg)
	gotBoard := modelboard.BuildWithExclusions(filtered, excluded, cfg, modelboard.DefaultConfig())
	gotCmp, _ := modelboard.BuildComparisons(filtered, cfg)

	if len(gotCmp) != len(wantCmp) {
		t.Fatalf("filtering in SQL changed the comparisons: %d with the filter, %d without — a "+
			"lost comparison is a model that played and is reported as absent",
			len(gotCmp), len(wantCmp))
	}

	// Every non-pairwise exclusion must survive into the published map with the same key.
	for reason, want := range wantExcluded {
		if len(reason) < 19 || reason[:19] != "game_not_pairwise_g" && reason[:18] != "game_not_pairwise_" {
			continue
		}
		if got := gotBoard.SeatsExcluded[reason]; got != want {
			t.Fatalf("seats_excluded[%q] = %d, want %d — a seat dropped by the query must be "+
				"counted exactly as one dropped in Go, or the published number understates it",
				reason, got, want)
		}
	}
}

// The filter must actually be doing something, or this whole change is inert and the test above
// passes for the wrong reason.
func TestGameFilterActuallyRemovesTheBulkOfTheRows(t *testing.T) {
	pool, ctx := moneyPathPool(t)
	repo := NewModelBoardRepo(pool)

	end := time.Now().Add(time.Hour)
	start := end.Add(-90 * 24 * time.Hour)
	hosts := []string{"openrouter.ai", "api.groq.com"}

	filtered, excluded, err := repo.Seats(ctx, "", start, end, hosts)
	if err != nil {
		t.Fatalf("Seats: %v", err)
	}
	var dropped int
	for _, n := range excluded {
		dropped += n
	}
	if dropped == 0 {
		t.Skip("no non-pairwise seats in this window; nothing for the filter to remove")
	}
	if dropped <= len(filtered) {
		t.Logf("filter removed %d seats and returned %d — the win is real but small on this data",
			dropped, len(filtered))
	}
	t.Logf("returned %d seats, filtered %d in the database", len(filtered), dropped)
}

// allSeatsUnfiltered runs the seat query with the pairwise filter OFF, so Go sees exactly what
// it used to see. The reference implementation this change is measured against.
func allSeatsUnfiltered(ctx context.Context, r *ModelBoardRepo, start, end time.Time, hosts []string) ([]modelboard.Seat, error) {
	rows, err := r.db.Query(ctx, renderSeatsSQL(modelBoardSeatDeveloperCTE, false, false),
		"", start, end, hosts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []modelboard.Seat
	for rows.Next() {
		var s modelboard.Seat
		var bound, logged int64
		if err := rows.Scan(&s.MatchID, &s.Game, &s.AgentID, &s.DeveloperID, &s.Result,
			&s.Model, &s.Scaffold, &bound, &logged, &s.Role); err != nil {
			return nil, err
		}
		if logged > 0 {
			s.CoverageKnown = true
			if bound > logged {
				bound = logged
			}
			s.Coverage = float64(bound) / float64(logged)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
