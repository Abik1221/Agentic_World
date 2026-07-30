package gamespec

import (
	"sort"
	"strings"
	"testing"

	"github.com/agent-arena/arena/internal/arena"
	"github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/engine/monopoly"
)

// byID indexes the spec games for lookup.
func byID(t *testing.T) map[string]Game {
	t.Helper()
	m := map[string]Game{}
	for _, g := range All() {
		m[g.ID] = g
	}
	return m
}

// wantSame asserts two string sets are equal, reporting what's missing/extra —
// this is the guard that fails the build when the engine's declared vocabulary
// (engine/*/vocab.go) gains or drops a value that the docs don't mirror.
func wantSame(t *testing.T, what string, doc, engine []string) {
	t.Helper()
	ds, es := append([]string(nil), doc...), append([]string(nil), engine...)
	sort.Strings(ds)
	sort.Strings(es)
	dset, eset := map[string]bool{}, map[string]bool{}
	for _, v := range ds {
		dset[v] = true
	}
	for _, v := range es {
		eset[v] = true
	}
	for _, v := range es {
		if !dset[v] {
			t.Errorf("%s: engine declares %q but gamespec docs omit it — add it to internal/gamespec/spec.go", what, v)
		}
	}
	for _, v := range ds {
		if !eset[v] {
			t.Errorf("%s: gamespec docs list %q but the engine does not declare it — stale doc value", what, v)
		}
	}
}

func eventStrings[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

func docTerms(terms []Term) []string {
	out := make([]string, len(terms))
	for i, t := range terms {
		out[i] = t.Value
	}
	return out
}

func docActions(as []ActionSpec) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Value
	}
	return out
}

// TestVocabularyMatchesEngine is the anti-drift core: every enumerable value the
// docs publish must equal the engine's declared vocabulary, exactly.
func TestVocabularyMatchesEngine(t *testing.T) {
	g := byID(t)

	// Goofspiel — events only (no roles/phases/actions).
	wantSame(t, "goofspiel.events", docTerms(g["goofspiel"].Events), eventStrings(goofspiel.AllEventTypes))

	// Mafia.
	wantSame(t, "mafia.phases", docTerms(g["mafia"].Phases), mafia.AllPhases)
	wantSame(t, "mafia.roles", docTerms(g["mafia"].Roles), mafia.AllRoles)
	wantSame(t, "mafia.actions", docActions(g["mafia"].Actions), mafia.AllActions)
	wantSame(t, "mafia.events", docTerms(g["mafia"].Events), eventStrings(mafia.AllEventTypes))

	// Monopoly.
	wantSame(t, "monopoly.phases", docTerms(g["monopoly"].Phases), monopoly.AllPhases)
	wantSame(t, "monopoly.actions", docActions(g["monopoly"].Actions), monopoly.AllActions)
	wantSame(t, "monopoly.events", docTerms(g["monopoly"].Events), eventStrings(monopoly.AllEventTypes))
}

// TestActionPhasesAreRealPhases ensures every phase referenced by an action is a
// declared phase of that game (no typos in the action→phase mapping).
func TestActionPhasesAreRealPhases(t *testing.T) {
	for _, game := range All() {
		phases := map[string]bool{}
		for _, p := range game.Phases {
			phases[p.Value] = true
		}
		if len(phases) == 0 {
			continue // goofspiel has no phases
		}
		for _, a := range game.Actions {
			for _, p := range a.Phases {
				if !phases[p] {
					t.Errorf("%s: action %q lists phase %q which is not a declared phase", game.ID, a.Value, p)
				}
			}
		}
	}
}

// TestMatchesArenaCatalog cross-checks the two independent sources of truth (the
// docs and the public /v1/arenas discovery list) so they can never disagree on
// player counts or availability.
func TestMatchesArenaCatalog(t *testing.T) {
	cat := map[string]arena.Arena{}
	for _, a := range arena.Arenas {
		cat[a.ID] = a
	}
	for _, g := range All() {
		a, ok := cat[g.ID]
		if !ok {
			t.Errorf("game %q is documented but absent from arena.Arenas", g.ID)
			continue
		}
		if g.MinPlayers != a.MinPlayers || g.MaxPlayers != a.MaxPlayers {
			t.Errorf("%s: player counts differ — docs %d..%d, arena %d..%d",
				g.ID, g.MinPlayers, g.MaxPlayers, a.MinPlayers, a.MaxPlayers)
		}
		if g.Status != a.Status {
			t.Errorf("%s: status differs — docs %q, arena %q", g.ID, g.Status, a.Status)
		}
	}
}

// TestEveryGameIsComplete guards the hand-written prose fields so a new game can't
// ship half-documented.
func TestEveryGameIsComplete(t *testing.T) {
	for _, g := range All() {
		if g.Tagline == "" || g.Overview == "" || g.WinCondition == "" {
			t.Errorf("%s: missing tagline/overview/win_condition", g.ID)
		}
		if len(g.ViewFields) == 0 || g.MoveSchema == "" || len(g.MoveFields) == 0 {
			t.Errorf("%s: missing view/move schema", g.ID)
		}
		if len(g.Events) == 0 {
			t.Errorf("%s: no events documented", g.ID)
		}
		if g.Example.Python == "" || g.Example.JS == "" {
			t.Errorf("%s: missing a Python or JS example", g.ID)
		}
	}
}

// TestRoundNumberingMatchesEngine pins the documented round semantics to what the
// engine actually does.
//
// The docs said "0-based index of the round now being bid" while the engine starts at
// Round 1 and indexes its prize order as PrizeOrder[Round-1]. That is not a cosmetic
// slip: an agent that trusts it reads the wrong prize for every round, and the error
// is invisible locally because the SDK simulator was 0-based too — so the harness
// agreed with the docs and disagreed with the platform.
//
// TestVocabularyMatchesEngine did not catch it because it compares vocabulary (event,
// phase and action NAMES), not field semantics. This closes that gap for the one
// field an agent must echo back on every single turn.
func TestRoundNumberingMatchesEngine(t *testing.T) {
	st, _ := goofspiel.New(goofspiel.Config{}).Init([]byte("round-numbering"))
	if st.Round != 1 {
		t.Fatalf("engine first round = %d, want 1 — if the engine really became "+
			"0-based, the docs and the SDK simulator must change with it", st.Round)
	}

	var desc string
	for _, f := range byID(t)["goofspiel"].ViewFields {
		if f.Name == "round" {
			desc = f.Meaning
		}
	}
	if desc == "" {
		t.Fatal("goofspiel view has no documented `round` field")
	}
	if strings.Contains(desc, "0-based") {
		t.Fatalf("docs describe `round` as 0-based, engine starts at %d: %q", st.Round, desc)
	}
	if !strings.Contains(desc, "1-based") {
		t.Fatalf("docs must state the round is 1-based, got: %q", desc)
	}
}
