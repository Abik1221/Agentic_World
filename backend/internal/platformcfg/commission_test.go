package platformcfg

import "testing"

// The commission arrives over the config bus from another service, so the bound is a
// safety rail, not input validation for our own code. A corrupt or hostile publisher
// must not be able to set a 100% rake and take the entire pot.
func TestCommissionRejectsValuesOutsideTheSafeBand(t *testing.T) {
	const fallback = 5
	for _, published := range []int{-1, -100, 51, 100, 1000} {
		s := &Snapshot{Economy: Economy{PlatformCommissionPct: published}}
		if got := s.CommissionPct(fallback); got != fallback {
			t.Fatalf("published %d%% was accepted as %d%%; want the fallback %d%%", published, got, fallback)
		}
	}
}

// Zero is a legitimate setting — a rake-free promotional table — and must not be
// mistaken for "unset" and silently replaced by the default.
func TestZeroCommissionIsHonoured(t *testing.T) {
	s := &Snapshot{Economy: Economy{PlatformCommissionPct: 0}}
	if got := s.CommissionPct(10); got != 0 {
		t.Fatalf("a deliberate 0%% rake became %d%%", got)
	}
}

func TestCommissionInBandIsUsed(t *testing.T) {
	s := &Snapshot{Economy: Economy{PlatformCommissionPct: 8}}
	if got := s.CommissionPct(5); got != 8 {
		t.Fatalf("got %d%%, want the published 8%%", got)
	}
}

// A nil snapshot happens before the first successful bus refresh. Charging the
// caller's own default then is correct; panicking on a cold start is not.
func TestNilSnapshotFallsBack(t *testing.T) {
	var s *Snapshot
	if got := s.CommissionPct(7); got != 7 {
		t.Fatalf("got %d%%, want the fallback 7%%", got)
	}
}
