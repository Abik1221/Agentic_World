package query

import "testing"

func TestIsMonopolyID(t *testing.T) {
	for _, id := range []string{"mp_old", "match_mp_old", "monopoly"} {
		if !isMonopolyID(id) {
			t.Errorf("%q should be treated as Monopoly", id)
		}
	}
	for _, id := range []string{"m_abc", "mf_1", "match_m_abc", "goofspiel"} {
		if isMonopolyID(id) {
			t.Errorf("%q must not be Monopoly", id)
		}
	}
}

func TestGameFromMatchID(t *testing.T) {
	if g := gameFromMatchID("m_1", ""); g != "goofspiel" {
		t.Errorf("m_ → %q", g)
	}
	if g := gameFromMatchID("mf_1", ""); g != "mafia" {
		t.Errorf("mf_ → %q", g)
	}
	if g := gameFromMatchID("mp_1", "goofspiel"); g != "goofspiel" {
		t.Errorf("session wins over prefix: %q", g)
	}
	if g := gameFromMatchID("m_1", "mafia"); g != "mafia" {
		t.Errorf("explicit session should win: %q", g)
	}
}

func TestIsLiveGame(t *testing.T) {
	if !isLiveGame("goofspiel") || !isLiveGame("Mafia") {
		t.Fatal("live games missing")
	}
	if isLiveGame("monopoly") || isLiveGame("") {
		t.Fatal("monopoly / empty must not be live")
	}
}
