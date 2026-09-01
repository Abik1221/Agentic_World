package integrity

import "testing"

func verdict(armed bool, blocked ...string) Verdict {
	v := Verdict{Armed: armed, Unproven: map[string]bool{}}
	for _, b := range blocked {
		v.Unproven[b] = true
	}
	return v
}

// TestScriptedSeatCannotClimbForFree is the regression guard for the bug this closes.
//
// A staked 1v1 whose seat proved nothing was refunded and then rated anyway, so a
// hand-written script paid nothing, lost nothing, and gained rating. It must now not be
// rated at all.
func TestScriptedSeatCannotClimbForFree(t *testing.T) {
	keep, excluded, ratable := FilterRatable([]string{"honest", "script"}, verdict(true, "script"))
	if ratable {
		t.Fatal("a 1v1 with an unproven seat was still ratable; the free climb survives")
	}
	if len(excluded) != 1 || excluded[0] != "script" {
		t.Fatalf("excluded %v, want [script]", excluded)
	}
	if len(keep) != 1 {
		t.Fatalf("kept %v, want just the honest seat", keep)
	}
}

// TestOneBadSeatCannotCancelAnElevenSeatTable. The mirror of FilterPayable's reasoning:
// letting one cheater void everyone else's rated game would be a griefing tool.
func TestOneBadSeatCannotCancelAnElevenSeatTable(t *testing.T) {
	agents := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "cheat"}
	keep, excluded, ratable := FilterRatable(agents, verdict(true, "cheat"))
	if !ratable {
		t.Fatal("an 11-survivor table must still be rated")
	}
	if len(keep) != 11 {
		t.Fatalf("kept %d seats, want 11", len(keep))
	}
	if len(excluded) != 1 {
		t.Fatalf("excluded %v, want just the cheat", excluded)
	}
	for _, k := range keep {
		if k == "cheat" {
			t.Fatal("the blocked seat survived the filter")
		}
	}
}

// TestUnarmedVerdictIsInert. "Nothing was measured" must never be read as "everyone failed".
// Getting this backwards would void honest play in bulk — the same failure mode the binding
// rules warn about.
func TestUnarmedVerdictIsInert(t *testing.T) {
	agents := []string{"a", "b"}
	keep, excluded, ratable := FilterRatable(agents, verdict(false))
	if !ratable || len(keep) != 2 || len(excluded) != 0 {
		t.Fatalf("unarmed verdict was not inert: keep=%v excluded=%v ratable=%v",
			keep, excluded, ratable)
	}
	// A zero Verdict is the value Evaluate returns on a read error. It must be inert too.
	keep, _, ratable = FilterRatable(agents, Verdict{})
	if !ratable || len(keep) != 2 {
		t.Fatal("a zero verdict (read error / no checker) must settle normally")
	}
}

// TestAWholeTableOfScriptsIsNotRated. When every seat is blocked nothing survives, so there
// is no comparison and nothing moves. Note this is distinct from the unarmed case: here the
// pipeline WAS reachable and every seat still failed it.
func TestAWholeTableOfScriptsIsNotRated(t *testing.T) {
	_, excluded, ratable := FilterRatable([]string{"s1", "s2"}, verdict(true, "s1", "s2"))
	if ratable {
		t.Fatal("a table where every seat is blocked must not be rated")
	}
	if len(excluded) != 2 {
		t.Fatalf("excluded %v, want both", excluded)
	}
}

// TestOrderIsPreserved. Seat order carries placement meaning to the rater; reordering it
// would silently reassign results to the wrong agents.
func TestOrderIsPreserved(t *testing.T) {
	agents := []string{"z", "y", "x", "w"}
	keep, _, _ := FilterRatable(agents, verdict(true, "x"))
	want := []string{"z", "y", "w"}
	for i := range want {
		if keep[i] != want[i] {
			t.Fatalf("keep = %v, want %v", keep, want)
		}
	}
}

// TestTooFewSeatsIsNotRatable. A rating update needs a comparison.
func TestTooFewSeatsIsNotRatable(t *testing.T) {
	if _, _, ratable := FilterRatable([]string{"solo"}, Verdict{}); ratable {
		t.Fatal("a single seat has nothing to compare against")
	}
	if _, _, ratable := FilterRatable(nil, Verdict{}); ratable {
		t.Fatal("an empty table is not ratable")
	}
}
