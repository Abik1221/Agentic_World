package verification

import (
	"context"
	"errors"
	"testing"
)

// A cryptographic proof outranks the statistical timing guess — and only when it covers
// enough of the agent's play to be an answer rather than an anecdote.

type fakeSamples struct{ ms []int }

func (f fakeSamples) InsertSample(context.Context, string, *string, int) error { return nil }
func (f fakeSamples) RecentSamples(context.Context, string, int) ([]int, error) {
	return f.ms, nil
}
func (f fakeSamples) Review(context.Context, string, string, string) error { return nil }

type fakeProven struct {
	bound, decisions int
	err              error
}

func (f fakeProven) ProvenShare(context.Context, string) (int, int, error) {
	return f.bound, f.decisions, f.err
}

// humanLooking builds a sample set the timing detector flags. Widely spread, seconds-scale
// response times are what a person clicking looks like; a bot's are tight and short.
func humanLooking() []int {
	ms := make([]int, 0, 60)
	for i := 0; i < 60; i++ {
		ms = append(ms, 1500+((i*937)%9000))
	}
	return ms
}

func mustFlagWithoutProof(t *testing.T) Eligibility {
	t.Helper()
	svc := New(fakeSamples{ms: humanLooking()})
	e, err := svc.CheckEligibility(context.Background(), "ag_x")
	if err != nil {
		t.Fatalf("CheckEligibility: %v", err)
	}
	if e.Eligible {
		t.Skipf("this sample set is not flagged by the timing detector "+
			"(human_likelihood %.2f over %d samples); the override cannot be tested without a "+
			"flagged baseline", e.Profile.HumanLikelihood, e.Profile.Count)
	}
	return e
}

func TestCompletionBindingOverridesTheTimingFlag(t *testing.T) {
	mustFlagWithoutProof(t) // baseline: this agent IS flagged on timing alone

	svc := New(fakeSamples{ms: humanLooking()})
	svc.SetProvenShare(fakeProven{bound: 95, decisions: 100})

	e, err := svc.CheckEligibility(context.Background(), "ag_x")
	if err != nil {
		t.Fatalf("CheckEligibility: %v", err)
	}
	if !e.Eligible {
		t.Fatalf("still ineligible (%s) despite 95%% of its decisions being PROVEN "+
			"LLM-backed. A human cannot produce a bound decision — the match rejects any move "+
			"that is not the one the model emitted — so the proof settles what the timing "+
			"profile was only estimating", e.Reason)
	}
	if e.Reason != "timing_flag_overridden_by_completion_binding" {
		t.Errorf("reason = %q, want the override to be NAMED: a silently-reversed fraud "+
			"verdict is one nobody can audit", e.Reason)
	}
}

// THE exploit this floor exists to stop. The model board learned it expensively: resolving a
// tier from the best evidence ever seen let one verified call in ten thousand decisions label a
// whole row verified. Here it would be worse — one bound call buying permanent exemption from a
// fraud control.
func TestASingleBoundCallDoesNotBuyExemption(t *testing.T) {
	mustFlagWithoutProof(t)

	svc := New(fakeSamples{ms: humanLooking()})
	svc.SetProvenShare(fakeProven{bound: 1, decisions: 10_000})

	e, err := svc.CheckEligibility(context.Background(), "ag_x")
	if err != nil {
		t.Fatalf("CheckEligibility: %v", err)
	}
	if e.Eligible {
		t.Fatal("one bound decision in ten thousand cleared the timing flag — that rewards " +
			"routing 1% of your calls and playing the rest by hand, which is precisely the " +
			"behaviour the detector exists to catch")
	}
	if e.Reason != "high_human_likelihood" {
		t.Errorf("reason = %q, want the timing verdict to stand", e.Reason)
	}
}

// An unreadable proof must leave the flag STANDING. This is the opposite direction to
// movebind.Enforce, and deliberately so: there, absence must not reject an honest move; here,
// absence must not excuse a flagged one. Otherwise the exemption is reachable by breaking the
// database.
func TestAnUnreadableProofDoesNotClearTheFlag(t *testing.T) {
	mustFlagWithoutProof(t)

	svc := New(fakeSamples{ms: humanLooking()})
	svc.SetProvenShare(fakeProven{err: errors.New("database is down")})

	e, err := svc.CheckEligibility(context.Background(), "ag_x")
	if err != nil {
		t.Fatalf("CheckEligibility must not fail on a proof lookup error: %v", err)
	}
	if e.Eligible {
		t.Fatal("a failed proof lookup granted eligibility — an exemption reachable by " +
			"breaking the database is not a control")
	}
}

// No prover wired: exactly the previous behaviour, so installing this changes nothing until
// someone chooses to install the evidence.
func TestWithoutTheProofPortNothingChanges(t *testing.T) {
	e := mustFlagWithoutProof(t)
	if e.Reason != "high_human_likelihood" {
		t.Errorf("reason = %q, want the unmodified timing verdict", e.Reason)
	}
}
