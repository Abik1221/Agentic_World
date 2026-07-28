package mafia

import "testing"

// The live source drives new-match pricing when it is sane, and is ignored when it is
// not. Both halves matter: the first is why the admin control is no longer decorative,
// the second is why a bad publish cannot take the pot.
func TestRakeSourceOverridesStaticConfig(t *testing.T) {
	s := &Service{cfg: Config{PlatformFeePct: 10}}
	if got := s.rakePct(); got != 10 {
		t.Fatalf("with no live source, got %d%%, want the static 10%%", got)
	}

	s.SetRakeSource(func() int { return 8 })
	if got := s.rakePct(); got != 8 {
		t.Fatalf("got %d%%, want the live 8%%", got)
	}

	s.SetRakeSource(func() int { return 0 })
	if got := s.rakePct(); got != 0 {
		t.Fatalf("a deliberate rake-free table became %d%%", got)
	}

	for _, bad := range []int{-1, 51, 100} {
		s.SetRakeSource(func() int { return bad })
		if got := s.rakePct(); got != 10 {
			t.Fatalf("live source published %d%% and it was accepted as %d%%", bad, got)
		}
	}
}
