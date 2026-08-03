package integrity

import (
	"context"
	"errors"
	"testing"
)

type fake struct {
	bound map[string]int
	err   error
}

func (f fake) BoundDecisions(_ context.Context, _, agent string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.bound[agent], nil
}

func eval(t *testing.T, bound map[string]int, agents ...string) Verdict {
	t.Helper()
	return Evaluate(context.Background(), fake{bound: bound}, "mch_1", agents, nil)
}

// The property that makes this safe to ship BEFORE any SDK sends proofs: with nothing
// measured anywhere, nobody is withheld. Today that is every table on the platform, so
// getting this wrong would stop paying honest players outright.
func TestInertWhenNobodyProvedAnything(t *testing.T) {
	v := eval(t, map[string]int{"a": 0, "b": 0, "c": 0}, "a", "b", "c")
	if v.Armed {
		t.Fatal("armed on a table where nothing was measured")
	}
	if len(v.Unproven) != 0 {
		t.Fatalf("withheld %v with no evidence available", v.Unproven)
	}
	payouts := map[string]int64{"a": 500, "b": 500}
	out, withheld := FilterPayable(payouts, v, "mch_1", nil)
	if withheld != 0 || len(out) != 2 {
		t.Fatalf("filtered a payout while inert: out=%v withheld=%d", out, withheld)
	}
}

// One proof at the table is evidence the pipeline was reachable. The silent seat is then
// an outlier, and it is the only one affected.
func TestArmsAndNamesOnlyTheSilentSeat(t *testing.T) {
	v := eval(t, map[string]int{"llm": 7, "script": 0, "other": 3}, "llm", "script", "other")
	if !v.Armed {
		t.Fatal("did not arm although a seat proved its work")
	}
	if !v.Blocked("script") {
		t.Fatal("the zero-proof seat was not blocked")
	}
	for _, ok := range []string{"llm", "other"} {
		if v.Blocked(ok) {
			t.Fatalf("blocked %q, which proved decisions", ok)
		}
	}
}

// Few proofs is legitimate — batching, caching, a retried call. ZERO is the signal.
func TestASingleProofIsEnoughToBePaid(t *testing.T) {
	v := eval(t, map[string]int{"a": 1, "b": 13}, "a", "b")
	if v.Blocked("a") {
		t.Fatal("blocked a seat that proved one decision; only zero is the signal")
	}
}

// The multi-seat answer: the table settles, and only the bad seat goes unpaid. Voiding an
// eleven-seat game over one seat would be a griefing tool.
func TestTheTableStillPaysEveryoneElse(t *testing.T) {
	v := eval(t, map[string]int{"w1": 5, "w2": 5, "cheat": 0}, "w1", "w2", "cheat")
	payouts := map[string]int64{"w1": 400, "w2": 400, "cheat": 400}

	out, withheld := FilterPayable(payouts, v, "mch_1", nil)

	if withheld != 400 {
		t.Fatalf("withheld %d, want 400", withheld)
	}
	if _, present := out["cheat"]; present {
		t.Fatal("the unproven seat is still in the payout map")
	}
	if out["w1"] != 400 || out["w2"] != 400 {
		t.Fatalf("an honest seat's payout changed: %v", out)
	}
	// Not redistributed. Handing the withheld share to the others would mean honest
	// players profit from an accusation — settlement posts it as the remainder instead.
	var paid int64
	for _, v := range out {
		paid += v
	}
	if paid != 800 {
		t.Fatalf("paid %d, want the two honest shares only (800)", paid)
	}
}

// FAILS OPEN. Refusing to pay because a database read failed would withhold real winnings
// from honest players in bulk during an outage.
func TestFailsOpenWhenTheCountCannotBeRead(t *testing.T) {
	v := Evaluate(context.Background(), fake{err: errors.New("db down")}, "mch_1", []string{"a", "b"}, nil)
	if v.Armed || len(v.Unproven) != 0 {
		t.Fatalf("armed on an unreadable count: %+v", v)
	}
	payouts := map[string]int64{"a": 500}
	out, withheld := FilterPayable(payouts, v, "mch_1", nil)
	if withheld != 0 || len(out) != 1 {
		t.Fatal("withheld a payout because the integrity store was unreachable")
	}
}

// A nil checker (tests, a deployment without the pipeline) changes nothing.
func TestNoCheckerIsANoOp(t *testing.T) {
	v := Evaluate(context.Background(), nil, "mch_1", []string{"a"}, nil)
	if v.Armed {
		t.Fatal("armed with no checker installed")
	}
	payouts := map[string]int64{"a": 500}
	out, withheld := FilterPayable(payouts, v, "mch_1", nil)
	if withheld != 0 || out["a"] != 500 {
		t.Fatal("a nil checker altered a settlement")
	}
}

// An empty table cannot be judged and must not blow up.
func TestNoSeatsIsSafe(t *testing.T) {
	v := eval(t, map[string]int{})
	if v.Armed {
		t.Fatal("armed with no seats")
	}
	out, withheld := FilterPayable(nil, v, "mch_1", nil)
	if withheld != 0 || len(out) != 0 {
		t.Fatalf("unexpected filter result: %v %d", out, withheld)
	}
}

// Every seat silent EXCEPT one proving ⇒ all the others are blocked. Worth pinning: it is
// the shape a real cheat-ring would produce, and the rule must not require a majority.
func TestOneHonestSeatBlocksAllTheRest(t *testing.T) {
	v := eval(t, map[string]int{"honest": 4, "s1": 0, "s2": 0, "s3": 0}, "honest", "s1", "s2", "s3")
	if !v.Armed {
		t.Fatal("did not arm")
	}
	for _, s := range []string{"s1", "s2", "s3"} {
		if !v.Blocked(s) {
			t.Fatalf("%s proved nothing but was not blocked", s)
		}
	}
	if v.Blocked("honest") {
		t.Fatal("blocked the only seat that proved anything")
	}
}
