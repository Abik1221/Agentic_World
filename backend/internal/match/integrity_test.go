package match

import (
	"context"
	"errors"
	"testing"
)

type fakeIntegrity struct {
	bound map[string]int
	err   error
}

func (f fakeIntegrity) BoundDecisions(_ context.Context, _, agent string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.bound[agent], nil
}

func matchWith(agents ...string) Match {
	m := Match{PublicID: "m_1"}
	for i, a := range agents {
		m.Players = append(m.Players, Player{Seat: i, AgentPublicID: a})
	}
	return m
}

// A deterministic script proves nothing and must not be paid.
func TestVoidsWhenASeatProvesNothing(t *testing.T) {
	s := &Service{}
	s.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{"ag_llm": 13, "ag_script": 0}}, 50)

	failed, agent := s.rankedIntegrityFailed(context.Background(), matchWith("ag_llm", "ag_script"), 13)
	if !failed {
		t.Fatal("a seat with zero proven decisions was allowed to settle")
	}
	if agent != "ag_script" {
		t.Fatalf("blamed the wrong seat: %q", agent)
	}
}

// Both genuinely LLM-backed => settle. This is the case that must not regress: an
// over-eager check that voids honest matches is worse than no check.
func TestSettlesWhenBothSeatsClearTheBar(t *testing.T) {
	s := &Service{}
	s.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{"ag_a": 7, "ag_b": 13}}, 50)

	if failed, agent := s.rankedIntegrityFailed(context.Background(), matchWith("ag_a", "ag_b"), 13); failed {
		t.Fatalf("voided an honest match, blaming %q (7/13 = 54%% clears 50%%)", agent)
	}
}

// minPct is INCLUSIVE — "at least this share" — which is what makes a 100% threshold
// expressible at all; a strictly-greater rule could never be satisfied by 13/13. So a
// MAJORITY is configured as 51, and these pin both ends of that.
func TestThresholdIsInclusiveSoMajorityIs51(t *testing.T) {
	s := &Service{}
	s.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{"ag_a": 5, "ag_b": 10}}, 51)

	// 5/10 is exactly half — not a majority, so 51 rejects it.
	if failed, agent := s.rankedIntegrityFailed(context.Background(), matchWith("ag_a", "ag_b"), 10); !failed {
		t.Fatal("exactly 50% cleared a majority bar")
	} else if agent != "ag_a" {
		t.Fatalf("blamed the wrong seat: %q", agent)
	}

	// 6/10 is a majority and must pass.
	s2 := &Service{}
	s2.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{"ag_a": 6, "ag_b": 10}}, 51)
	if failed, agent := s2.rankedIntegrityFailed(context.Background(), matchWith("ag_a", "ag_b"), 10); failed {
		t.Fatalf("6/10 is a majority but was voided, blaming %q", agent)
	}
}

// A 100% threshold has to be satisfiable, or the strictest setting would void every
// match including a perfectly honest one.
func TestAHundredPercentThresholdIsAchievable(t *testing.T) {
	s := &Service{}
	s.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{"ag_a": 13, "ag_b": 13}}, 100)

	if failed, agent := s.rankedIntegrityFailed(context.Background(), matchWith("ag_a", "ag_b"), 13); failed {
		t.Fatalf("13/13 failed a 100%% bar, blaming %q", agent)
	}

	s2 := &Service{}
	s2.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{"ag_a": 12, "ag_b": 13}}, 100)
	if failed, _ := s2.rankedIntegrityFailed(context.Background(), matchWith("ag_a", "ag_b"), 13); !failed {
		t.Fatal("12/13 cleared a 100% bar")
	}
}

// The SHARE rule is off by default; the zero-proof gate is not. With no checker at
// all nothing is enforced, and with minPct=0 a table where nobody proved anything
// still settles — see TestZeroProofGate for why that second case must hold.
func TestShareRuleDisabledByDefaultAndWhenPctIsZero(t *testing.T) {
	none := &Service{}
	if failed, _ := none.rankedIntegrityFailed(context.Background(), matchWith("ag_a"), 13); failed {
		t.Fatal("enforced with no checker configured")
	}

	off := &Service{}
	off.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{"ag_a": 0}}, 0)
	if failed, _ := off.rankedIntegrityFailed(context.Background(), matchWith("ag_a"), 13); failed {
		t.Fatal("minPct=0 must leave the share rule off")
	}

	// minPct=0 does not switch off the zero-proof gate: a seat that proved nothing
	// beside one that proved plenty is still refused, with no threshold configured.
	gate := &Service{}
	gate.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{"ag_llm": 13, "ag_script": 0}}, 0)
	if failed, agent := gate.rankedIntegrityFailed(context.Background(), matchWith("ag_llm", "ag_script"), 13); !failed {
		t.Fatal("with minPct=0, a zero-proof seat was still paid — the gate is not active")
	} else if agent != "ag_script" {
		t.Fatalf("blamed the wrong seat: %q", agent)
	}
}

// The zero-proof gate, and the exact reason it is RELATIVE rather than absolute.
//
// Read together, these two cases are the whole design. The proof pipeline is real, but it
// only produces evidence where the LLM gateway is enabled AND the agent routed its client
// through pyyol.route() — and the gateway is off by default. Where neither holds, every
// honest seat measures zero, so an absolute "zero proofs ⇒ void" rule would cancel real
// matches for reasons the developer did not choose. Requiring that some OTHER seat proved
// its work makes the gate inert wherever the pipeline is not running, then self-arming
// wherever it is, with no threshold to tune and no deploy.
func TestZeroProofGate(t *testing.T) {
	t.Run("inert when nobody in the match proved anything", func(t *testing.T) {
		s := &Service{}
		s.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{"ag_a": 0, "ag_b": 0}}, 0)

		if failed, agent := s.rankedIntegrityFailed(context.Background(), matchWith("ag_a", "ag_b"), 13); failed {
			t.Fatalf("voided a match where NO seat proved anything, blaming %q. Before a "+
				"proof-carrying SDK exists that is every honest match on the platform", agent)
		}
	})

	t.Run("arms itself as soon as one seat proves its work", func(t *testing.T) {
		s := &Service{}
		s.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{"ag_llm": 1, "ag_script": 0}}, 0)

		// ONE proof on the table is enough evidence the pipeline was reachable.
		failed, agent := s.rankedIntegrityFailed(context.Background(), matchWith("ag_llm", "ag_script"), 13)
		if !failed {
			t.Fatal("a zero-proof seat settled against an opponent that did prove its calls")
		}
		if agent != "ag_script" {
			t.Fatalf("blamed the wrong seat: %q", agent)
		}
	})

	t.Run("a proving seat is never blamed for its opponent", func(t *testing.T) {
		s := &Service{}
		s.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{"ag_a": 2, "ag_b": 11}}, 0)

		if failed, agent := s.rankedIntegrityFailed(context.Background(), matchWith("ag_a", "ag_b"), 13); failed {
			t.Fatalf("voided a match where both seats proved something, blaming %q. Few "+
				"proofs is legitimate (batching, caching, retries) — zero is the signal", agent)
		}
	})

	t.Run("still fails open when the count cannot be read", func(t *testing.T) {
		s := &Service{}
		s.SetIntegrityCheck(fakeIntegrity{err: errors.New("db down")}, 0)

		if failed, _ := s.rankedIntegrityFailed(context.Background(), matchWith("ag_a", "ag_b"), 13); failed {
			t.Fatal("the zero-proof gate voided a match because the store was unreachable")
		}
	})

	t.Run("multi-seat: the one silent seat is the one named", func(t *testing.T) {
		s := &Service{}
		s.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{
			"ag_a": 9, "ag_b": 4, "ag_quiet": 0, "ag_d": 12,
		}}, 0)

		failed, agent := s.rankedIntegrityFailed(context.Background(),
			matchWith("ag_a", "ag_b", "ag_quiet", "ag_d"), 13)
		if !failed || agent != "ag_quiet" {
			t.Fatalf("failed=%v agent=%q; want the zero-proof seat named", failed, agent)
		}
	})
}

// FAIL OPEN. Voiding on a database hiccup would cancel legitimate matches in bulk
// during an outage; a cheat that slips through is still recorded and reviewable.
func TestSettlesWhenTheCheckIsUnavailable(t *testing.T) {
	s := &Service{}
	s.SetIntegrityCheck(fakeIntegrity{err: errors.New("db down")}, 50)

	if failed, _ := s.rankedIntegrityFailed(context.Background(), matchWith("ag_a", "ag_b"), 13); failed {
		t.Fatal("voided a match because the integrity store was unreachable")
	}
}

// A match with no decisions (aborted before anyone moved) has nothing to judge.
func TestNoDecisionsIsNotAFailure(t *testing.T) {
	s := &Service{}
	s.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{"ag_a": 0}}, 50)

	if failed, _ := s.rankedIntegrityFailed(context.Background(), matchWith("ag_a"), 0); failed {
		t.Fatal("voided a match in which nobody made a decision")
	}
}
