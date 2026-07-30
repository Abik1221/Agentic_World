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

// Disabled by default. Until the proof ships to developers, every honest agent scores
// zero — enforcing then would void real matches wholesale.
func TestDisabledByDefaultAndWhenPctIsZero(t *testing.T) {
	none := &Service{}
	if failed, _ := none.rankedIntegrityFailed(context.Background(), matchWith("ag_a"), 13); failed {
		t.Fatal("enforced with no checker configured")
	}

	off := &Service{}
	off.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{"ag_a": 0}}, 0)
	if failed, _ := off.rankedIntegrityFailed(context.Background(), matchWith("ag_a"), 13); failed {
		t.Fatal("minPct=0 must leave enforcement off")
	}
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
