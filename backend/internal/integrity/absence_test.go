package integrity

import (
	"context"
	"testing"
)

func evalAbsent(t *testing.T, bound map[string]int, absent map[string]bool, agents ...string) Verdict {
	t.Helper()
	return Evaluate(context.Background(), fake{bound: bound}, "mch_1", agents, absent, nil)
}

// An agent that goes dark and STILL WINS keeps the money.
//
// This is the arena's stated rule for the multi-seat games: absence costs you the game,
// not your prize when you win the game anyway. Mafia is exactly where it bites — a town
// seat can be voted nothing but correct, crash on day three, and its team still wins.
// Withholding there confiscates a real win over a proof the agent was never around to
// produce.
func TestAbsentWinnerIsStillPaid(t *testing.T) {
	v := evalAbsent(t,
		map[string]int{"ag_live": 20, "ag_dark": 0},
		map[string]bool{"ag_dark": true},
		"ag_live", "ag_dark")

	if v.Blocked("ag_dark") {
		t.Fatal("an absent seat's payout was withheld — it wins the table and is paid nothing, " +
			"over proofs it could not have produced because it was never asked a question it answered")
	}

	payouts := map[string]int64{"ag_live": 500, "ag_dark": 500}
	out, withheld := FilterPayable(payouts, v, "mch_1", nil)
	if withheld != 0 {
		t.Fatalf("withheld %d coins from a table whose only zero-proof seat was absent", withheld)
	}
	if out["ag_dark"] != 500 {
		t.Fatalf("absent winner paid %d, want 500", out["ag_dark"])
	}
}

// The exemption is not a loophole: a seat that was PRESENT and proved nothing is still
// withheld, absence flag or no absence flag for other seats.
func TestPresentUnprovenSeatIsStillWithheld(t *testing.T) {
	v := evalAbsent(t,
		map[string]int{"ag_live": 20, "ag_script": 0, "ag_dark": 0},
		map[string]bool{"ag_dark": true}, // only ag_dark went away
		"ag_live", "ag_script", "ag_dark")

	if !v.Blocked("ag_script") {
		t.Fatal("a present seat that proved nothing was paid — the scripted-agent case the rule exists for")
	}
	if v.Blocked("ag_dark") {
		t.Fatal("the absent seat was withheld despite the exemption")
	}

	out, withheld := FilterPayable(map[string]int64{"ag_script": 300, "ag_dark": 300}, v, "mch_1", nil)
	if withheld != 300 {
		t.Fatalf("withheld=%d want 300 (the scripted seat only)", withheld)
	}
	if _, paid := out["ag_script"]; paid {
		t.Fatal("the scripted seat is still in the payout map")
	}
}

// Passing no absence information must behave exactly as before, so a caller that has not
// been taught about attendance cannot accidentally hand out an amnesty.
func TestNilAbsenceMapJudgesEverySeat(t *testing.T) {
	v := evalAbsent(t, map[string]int{"ag_live": 20, "ag_zero": 0}, nil, "ag_live", "ag_zero")
	if !v.Blocked("ag_zero") {
		t.Fatal("a nil absence map exempted a seat; the default must be to judge everyone")
	}
}
