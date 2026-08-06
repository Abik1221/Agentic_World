package match

import (
	"context"
	"testing"
	"time"
)

type stubWindows struct {
	byAgent map[string]time.Duration
	calls   int
}

func (s *stubWindows) Window(_ context.Context, agent, _ string) time.Duration {
	s.calls++
	return s.byAgent[agent]
}

func svcWithWindow(base time.Duration, w WindowProvider) *Service {
	s := &Service{}
	s.cfg.MoveWindow = base
	s.SetWindowProvider(w)
	return s
}

// A deadline sits on the path of EVERY turn on the platform, so the failure mode that
// matters is not "the window is slightly wrong" — it is "the window is zero", which would
// forfeit every decision the instant it was asked. Each of these must fall back to the
// configured constant rather than propagate a bad answer.
func TestWindowFallsBackOnAnyDoubt(t *testing.T) {
	const base = 45 * time.Second
	cases := map[string]struct {
		svc    *Service
		agents []string
	}{
		"no provider installed":  {svcWithWindow(base, nil), []string{"ag_a"}},
		"provider returns zero":  {svcWithWindow(base, &stubWindows{byAgent: map[string]time.Duration{}}), []string{"ag_a"}},
		"empty agent id":         {svcWithWindow(base, &stubWindows{byAgent: map[string]time.Duration{"": time.Hour}}), []string{""}},
		"no agents at all":       {svcWithWindow(base, &stubWindows{byAgent: map[string]time.Duration{}}), nil},
		"provider says negative": {svcWithWindow(base, &stubWindows{byAgent: map[string]time.Duration{"ag_a": -time.Minute}}), []string{"ag_a"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.svc.moveWindow(context.Background(), tc.agents...); got != base {
				t.Fatalf("window %v, want the %v configured fallback", got, base)
			}
		})
	}
}

// A round deadline is SHARED by both seats, so the table has to run on the slower agent's
// window. Using the faster one would cut the slower agent off mid-decision because of who
// it happened to be matched against — and who you are matched against is the one thing a
// deadline must never depend on.
func TestSharedDeadlineUsesTheSlowerAgent(t *testing.T) {
	w := &stubWindows{byAgent: map[string]time.Duration{
		"ag_fast": 20 * time.Second,  // below the base; must not shrink the table's clock
		"ag_slow": 140 * time.Second, // a local model that genuinely needs the time
	}}
	s := svcWithWindow(45*time.Second, w)

	got := s.moveWindow(context.Background(), "ag_fast", "ag_slow")
	if got != 140*time.Second {
		t.Fatalf("shared window %v, want the slower agent's 2m20s — the slow seat would "+
			"forfeit rounds it was in the middle of deciding", got)
	}
	// Order must not matter: the same pairing gets the same clock either way round.
	if rev := s.moveWindow(context.Background(), "ag_slow", "ag_fast"); rev != got {
		t.Fatalf("swapping seat order changed the window: %v vs %v", rev, got)
	}
}

// An agent faster than the configured base must not SHRINK the window below it. The
// adaptive term exists to give a slow agent room, not to punish a fast one by holding it
// to its own record when it hits one genuinely hard turn.
func TestAFastAgentNeverShrinksTheWindow(t *testing.T) {
	w := &stubWindows{byAgent: map[string]time.Duration{"ag_quick": 3 * time.Second}}
	s := svcWithWindow(45*time.Second, w)
	if got := s.moveWindow(context.Background(), "ag_quick"); got != 45*time.Second {
		t.Fatalf("window shrank to %v for a fast agent; one hard turn would now be "+
			"forfeited because it is usually quick", got)
	}
}

// Installing nil must be a no-op rather than clearing a provider that is already there —
// a stray SetWindowProvider(nil) during wiring would silently revert every table to the
// constant with no error anywhere.
func TestSetWindowProviderIgnoresNil(t *testing.T) {
	w := &stubWindows{byAgent: map[string]time.Duration{"ag_a": 90 * time.Second}}
	s := svcWithWindow(45*time.Second, w)
	s.SetWindowProvider(nil)
	if got := s.moveWindow(context.Background(), "ag_a"); got != 90*time.Second {
		t.Fatalf("window %v — a nil install wiped the working provider", got)
	}
}
