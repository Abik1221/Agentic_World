package match

import (
	"context"
	"strings"
	"testing"
)

// kindRepo is a Repo stub that answers only AgentKind. Every other method panics, so if the
// service reaches the database before finishing its checks the test says so loudly rather
// than passing on a nil.
type kindRepo struct {
	Repo
	kinds map[string]string
}

func (k kindRepo) AgentKind(_ context.Context, id string) (string, error) {
	return k.kinds[id], nil
}

// TestHarnessTableRefusesNonHarnessSeats is the authorization story for the zero-stake table.
//
// The route is operator-only, but that is the wrong control to rely on: it says who may ASK,
// not what may be SEATED. A zero-stake table holding a DEVELOPER's agent would be free
// ranked-looking play, and "an admin asked for it" is not evidence about what the agent is.
//
// So the kind is read from the database and checked here, for BOTH seats. The test drives
// every mixed combination because a check written for one seat and forgotten for the other
// is the obvious way to get this wrong, and it looks correct in review.
func TestHarnessTableRefusesNonHarnessSeats(t *testing.T) {
	s := &Service{repo: kindRepo{kinds: map[string]string{
		"ag_bench_a": "harness",
		"ag_bench_b": "harness",
		"ag_dev":     "external",
		"ag_bot":     "house",
	}}}

	for _, tc := range []struct{ name, a, b string }{
		{"developer in seat A", "ag_dev", "ag_bench_b"},
		{"developer in seat B", "ag_bench_a", "ag_dev"},
		{"house bot in seat A", "ag_bot", "ag_bench_b"},
		{"house bot in seat B", "ag_bench_a", "ag_bot"},
		{"both developers", "ag_dev", "ag_dev2"},
		{"unknown agent", "ag_bench_a", "ag_nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateHarnessPaired(context.Background(), tc.a, "usr_system", tc.b, "usr_system")
			if err == nil {
				t.Fatal("a non-harness agent was seated at a zero-stake table")
			}
			// The refusal must name the reason. A generic 400 here would send an operator
			// hunting through the runner for a fault that is a deliberate policy.
			if !strings.Contains(err.Error(), "harness") {
				t.Errorf("refusal should name the harness requirement, got: %v", err)
			}
		})
	}
}

// TestHarnessTableRefusesOneAgentInBothSeats pins the degenerate case.
//
// A model cannot be compared with itself: the table would produce a comparison that always
// ties, which is not a null result but a fabricated one — it would enter the fit and pull
// every rating toward the middle.
func TestHarnessTableRefusesOneAgentInBothSeats(t *testing.T) {
	s := &Service{repo: kindRepo{kinds: map[string]string{"ag_bench_a": "harness"}}}
	_, err := s.CreateHarnessPaired(context.Background(), "ag_bench_a", "usr_system", "ag_bench_a", "usr_system")
	if err == nil {
		t.Fatal("the same agent was seated in both seats")
	}
	if !strings.Contains(err.Error(), "distinct") {
		t.Errorf("refusal should say the seats must differ, got: %v", err)
	}
}
