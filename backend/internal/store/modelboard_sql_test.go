package store

import (
	"regexp"
	"strings"
	"testing"
)

// The two model boards share one tail SQL so that a rating on one means what a rating on
// the other means. They no longer share one PLAN — the harness board scopes its child CTEs
// to the seat set and the developer board does not, because measurement says opposite
// things at 44 seats and at 320,000 (see seatsTailSQL).
//
// That is a licence to drift, so it is fenced. These tests do not check that the SQL is
// fast; they check that the only difference between the two renderings is join clauses,
// which cannot change WHICH rows come back.

func TestSeatsTailSQLDiffersOnlyInScoping(t *testing.T) {
	unscoped, scoped := seatsTailSQL(false), seatsTailSQL(true)

	if unscoped == scoped {
		t.Fatal("scoped and unscoped renderings are identical — the scoping is not being applied")
	}

	// Strip the added lines from the scoped rendering; what remains must be the unscoped
	// one exactly. Anything else — a changed predicate, a different column, an extra
	// GROUP BY term — surfaces here as a mismatch.
	var kept []string
	inSeatKeys := false
	for _, line := range strings.Split(scoped, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "seat_keys AS MATERIALIZED ("):
			// Skip the whole inserted CTE, opening line through its closing "),".
			inSeatKeys = true
			continue
		case inSeatKeys:
			if trimmed == ")," {
				inSeatKeys = false
			}
			continue
		case strings.HasPrefix(trimmed, "JOIN seat_keys"):
			continue
		}
		kept = append(kept, line)
	}
	if got := strings.Join(kept, "\n"); got != unscoped {
		t.Fatalf("removing the scoping joins did not recover the unscoped SQL — the two boards "+
			"differ by more than a join strategy, which means they can now disagree about a "+
			"rating.\n\nfirst divergence:\n%s", firstDiff(got, unscoped))
	}
}

// No placeholder may survive into SQL sent to Postgres. A leftover "{{...}}" is a syntax
// error at best and, if it landed inside a comment, a silently unscoped board.
func TestNoScopingPlaceholdersReachPostgres(t *testing.T) {
	// Asserted against renderSeatsSQL, not seatsTailSQL: the tail deliberately still carries
	// {{PAIRWISE_FILTER}} — it is the renderer's job to substitute it, and the renderer is what
	// actually reaches Postgres. Checking the tail alone would pass while a caller that skipped
	// the renderer shipped a placeholder to the database.
	placeholder := regexp.MustCompile(`\{\{[A-Z_]+\}\}`)
	for _, seat := range []struct{ name, cte string }{
		{"developer", modelBoardSeatDeveloperCTE}, {"harness", modelBoardSeatHarnessCTE},
	} {
		for _, scoped := range []bool{false, true} {
			for _, filtered := range []bool{false, true} {
				sql := renderSeatsSQL(seat.cte, scoped, filtered)
				if m := placeholder.FindString(sql); m != "" {
					t.Fatalf("%s/scoped=%v/filtered=%v still contains %s",
						seat.name, scoped, filtered, m)
				}
				if filtered && !strings.Contains(sql, "WHERE s.game = ANY($5)") {
					t.Fatalf("%s/scoped=%v: filtered rendering lost the game filter — the query "+
						"would return every seat in the window and the board would look correct "+
						"while reading 98.8%% more rows than it uses", seat.name, scoped)
				}
				if !filtered && strings.Contains(sql, "WHERE s.game = ANY($5)") {
					t.Fatalf("%s/scoped=%v: unfiltered rendering carries the filter, so the "+
						"exclusion count would count nothing", seat.name, scoped)
				}
			}
		}
	}
}

// Both renderings must compose with either seat CTE into something whose CTE list is
// well-formed — the scoped one inserts a CTE, and an inserted CTE that lost its comma is
// the classic way this breaks.
func TestBothRenderingsComposeWithBothSeatCTEs(t *testing.T) {
	for _, seat := range []struct {
		name string
		cte  string
	}{{"developer", modelBoardSeatDeveloperCTE}, {"harness", modelBoardSeatHarnessCTE}} {
		for _, scoped := range []bool{false, true} {
			sql := seat.cte + seatsTailSQL(scoped)
			if !strings.Contains(sql, "WITH seat AS (") {
				t.Fatalf("%s/scoped=%v: lost the seat CTE", seat.name, scoped)
			}
			if strings.Contains(sql, "),verified") || strings.Contains(sql, ",,") {
				t.Fatalf("%s/scoped=%v: malformed CTE list", seat.name, scoped)
			}
			if scoped && !strings.Contains(sql, "seat_keys AS MATERIALIZED (") {
				t.Fatalf("%s: scoped rendering is missing seat_keys", seat.name)
			}
			if !scoped && strings.Contains(sql, "seat_keys") {
				t.Fatalf("%s: unscoped rendering mentions seat_keys", seat.name)
			}
		}
	}
}

// The scoped rendering must scope EVERY table that is read per-decision. One left unscoped
// keeps a full scan of the largest table in the system while the board still returns the
// right rows — so tests pass and only the disk notices.
func TestScopedRenderingScopesEveryPerDecisionTable(t *testing.T) {
	sql := seatsTailSQL(true)
	for _, table := range []string{"agent_model_calls", "agent_match_decisions", "agent_match_bound_decisions"} {
		idx := strings.Index(sql, "FROM "+table)
		if idx < 0 {
			t.Fatalf("%s is no longer read here; this test needs updating", table)
		}
		for idx >= 0 {
			rest := sql[idx:]
			cut := len(rest)
			if n := strings.Index(rest[5:], "\n     WHERE"); n >= 0 {
				cut = n + 5
			}
			if !strings.Contains(rest[:min(cut, 400)], "JOIN seat_keys") {
				t.Fatalf("FROM %s is not scoped to seat_keys in the scoped rendering — that is a "+
					"full scan of a 22 GB table that no assertion about output would catch", table)
			}
			next := strings.Index(sql[idx+1:], "FROM "+table)
			if next < 0 {
				break
			}
			idx += 1 + next
		}
	}
}

func firstDiff(a, b string) string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(la) && i < len(lb); i++ {
		if la[i] != lb[i] {
			return "  got:  " + la[i] + "\n  want: " + lb[i]
		}
	}
	return "  (one is a prefix of the other)"
}
