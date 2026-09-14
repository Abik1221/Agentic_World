package verification_test

import (
	"context"
	"testing"

	"github.com/agent-arena/arena/internal/verification"
)

// The trap this fixes, stated as a test.
//
// An agent is flagged when its recent response times look human — slow and erratic. That
// verdict is computed from the agent's OWN samples, samples are produced only by playing,
// and a flagged agent may not play: not ranked, not a private room, and not sandbox
// either, because CreateSandbox consults the same gate. So a flagged agent could never
// produce the faster samples that would clear it. The one documented alternative — a
// completion-binding proof outranking the timing guess — needs 90% of the agent's WHOLE
// history bound, which an agent that already has unbound history cannot reach.
//
// Both doors closed at once, and the refusal says "flagged for review" while nothing in
// the system could perform a review. A review is the missing door.

// reviewableRepo is the slate rule in miniature: samples carry an ordinal "time", a review
// records one, and RecentSamples returns only what came after the newest review. That is
// exactly what the SQL does with created_at.
type reviewableRepo struct {
	samples    []timedSample
	reviewedAt int // 0 = never reviewed
	reviews    []string
}

type timedSample struct {
	at int
	ms int
}

func (r *reviewableRepo) InsertSample(context.Context, string, *string, int) error { return nil }

func (r *reviewableRepo) RecentSamples(_ context.Context, _ string, limit int) ([]int, error) {
	var out []int
	for _, s := range r.samples {
		if s.at > r.reviewedAt {
			out = append(out, s.ms)
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (r *reviewableRepo) Review(_ context.Context, _, reviewedBy, reason string) error {
	// A review always lands after every sample recorded so far, as `now()` does.
	r.reviewedAt = len(r.samples)
	r.reviews = append(r.reviews, reviewedBy+":"+reason)
	return nil
}

// humanLooking builds n samples that are slow AND erratic — mean and variance both pinned
// high, which is what a provider timing out on alternate turns actually produces. This is
// the shape an unset API key creates: some turns fail instantly, others burn the client
// timeout.
func humanLooking(n, from int) []timedSample {
	out := make([]timedSample, 0, n)
	for i := 0; i < n; i++ {
		ms := 20000
		if i%2 == 0 {
			ms = 300
		}
		out = append(out, timedSample{at: from + i + 1, ms: ms})
	}
	return out
}

// botLooking builds n fast, consistent samples — a model answering normally.
func botLooking(n, from int) []timedSample {
	out := make([]timedSample, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, timedSample{at: from + i + 1, ms: 700 + (i % 3)})
	}
	return out
}

func TestSlowErraticPlayIsStillFlagged(t *testing.T) {
	// The control must keep working. If this stops flagging, the review below is
	// clearing something that was never set and the test beneath it proves nothing.
	repo := &reviewableRepo{samples: humanLooking(40, 0)}
	e, err := verification.New(repo).CheckEligibility(context.Background(), "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if e.Eligible {
		t.Fatalf("slow, erratic play was admitted — the detector is not detecting: %+v", e.Profile)
	}
	if e.Reason != "high_human_likelihood" {
		t.Fatalf("reason = %q, want high_human_likelihood", e.Reason)
	}
}

func TestAReviewLetsAFlaggedAgentPlayAgain(t *testing.T) {
	ctx := context.Background()
	repo := &reviewableRepo{samples: humanLooking(40, 0)}
	svc := verification.New(repo)

	if e, _ := svc.CheckEligibility(ctx, "agt_1"); e.Eligible {
		t.Fatal("precondition: the agent should start out flagged")
	}

	if err := svc.Review(ctx, "agt_1", "usr_admin", "provider outage, key was unset"); err != nil {
		t.Fatal(err)
	}

	// Nothing about the agent changed, and no sample was deleted — but it is judged on
	// what it does from here, and so far that is nothing.
	e, err := svc.CheckEligibility(ctx, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if !e.Eligible {
		t.Fatalf("a reviewed agent is still refused — the flag is still terminal: %s", e.Reason)
	}
	if len(repo.samples) != 40 {
		t.Fatalf("the review deleted evidence: %d samples left, want 40 kept", len(repo.samples))
	}
	if len(repo.reviews) != 1 || repo.reviews[0] != "usr_admin:provider outage, key was unset" {
		t.Fatalf("the decision was not attributed: %v", repo.reviews)
	}
}

func TestAReviewIsNotAnExemption(t *testing.T) {
	// The important half. A cleared agent that goes on behaving like a human at a keyboard
	// must be flagged again by the ordinary rule — otherwise a review is a permanent hole
	// punched in a fraud control, which is exactly what this codebase forbids.
	ctx := context.Background()
	repo := &reviewableRepo{samples: humanLooking(40, 0)}
	svc := verification.New(repo)
	if err := svc.Review(ctx, "agt_1", "usr_admin", "false positive"); err != nil {
		t.Fatal(err)
	}

	repo.samples = append(repo.samples, humanLooking(40, 40)...)

	e, err := svc.CheckEligibility(ctx, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if e.Eligible {
		t.Fatal("a reviewed agent kept playing by hand and was NOT re-flagged — the review became an exemption")
	}
	if e.Reason != "high_human_likelihood" {
		t.Fatalf("reason = %q, want high_human_likelihood", e.Reason)
	}
}

func TestAReviewedAgentPlayingNormallyStaysEligible(t *testing.T) {
	// The other direction: a genuine false positive that resumes answering from a model
	// is admitted on its new evidence, and the old samples never drag it back down.
	ctx := context.Background()
	repo := &reviewableRepo{samples: humanLooking(40, 0)}
	svc := verification.New(repo)
	if err := svc.Review(ctx, "agt_1", "usr_admin", "key was unset"); err != nil {
		t.Fatal(err)
	}

	repo.samples = append(repo.samples, botLooking(40, 40)...)

	e, err := svc.CheckEligibility(ctx, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if !e.Eligible {
		t.Fatalf("an agent answering normally after review was refused: %s / %+v", e.Reason, e.Profile)
	}
}

func TestSparseHistoryIsAdmittedSoANewAgentCanStart(t *testing.T) {
	// Pins the innocent-until-proven default, which is also why a brand-new agent is the
	// only workaround available while a flag is terminal.
	repo := &reviewableRepo{samples: humanLooking(19, 0)}
	e, err := verification.New(repo).CheckEligibility(context.Background(), "agt_new")
	if err != nil {
		t.Fatal(err)
	}
	if !e.Eligible {
		t.Fatal("an agent with fewer than 20 samples was judged — sparse history must admit")
	}
}
