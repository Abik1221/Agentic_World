package mafia

import "testing"

// The house exemption is a hole in a fraud control. These pin exactly how wide it is.

// A USER's agent must certify on a free table. This is the gap that existed: certification sat
// inside `if entryFee > 0`, so every practice table skipped it.
//
// A practice match is not inert — it writes decision and benchmark rows that feed the P-Index,
// the model board and the deception index. A scripted agent farming free tables earns a public
// record it did not deserve, which is the same fraud as winning coins with one, paid in
// reputation instead of currency.
func TestUserAgentMustCertifyOnAFreeTable(t *testing.T) {
	s := &Service{}
	s.SetHouseRoster([]string{"ag_house1"})
	if !s.mustCertify("ag_user", 0) {
		t.Fatal("a user's agent skipped certification on a free table")
	}
}

// The platform's own bots may fill a practice seat. That asymmetry is the whole rule.
func TestHouseAgentSkipsCertificationOnlyWhenFree(t *testing.T) {
	s := &Service{}
	s.SetHouseRoster([]string{"ag_house1"})
	if s.mustCertify("ag_house1", 0) {
		t.Fatal("a house bot was blocked from a practice table it is meant to play")
	}
	// The exemption must NOT survive a fee. A house agent at a paid table is a bug elsewhere,
	// and failing here is the correct outcome rather than something to smooth over.
	if !s.mustCertify("ag_house1", 500) {
		t.Fatal("a house agent skipped certification on a PAID table — the exemption must never " +
			"reach money")
	}
}

// With no roster injected, NOTHING is exempt. The safe default is that the hole does not exist.
func TestNoRosterMeansNoExemption(t *testing.T) {
	s := &Service{}
	if !s.mustCertify("ag_anyone", 0) {
		t.Fatal("an agent was exempted with no house roster configured")
	}
}

// The roster is an explicit id set, so nothing can match into it by resembling a house agent.
func TestExemptionCannotBeInheritedByResemblance(t *testing.T) {
	s := &Service{}
	s.SetHouseRoster([]string{"ag_house1"})
	for _, impostor := range []string{"ag_house2", "ag_house1x", "demo_house1", "ag_HOUSE1", ""} {
		if !s.mustCertify(impostor, 0) {
			t.Errorf("%q inherited the house exemption; the roster must be exact ids, never a "+
				"pattern something can grow into", impostor)
		}
	}
}
