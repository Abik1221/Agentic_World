package devtrace

import (
	"context"
	"testing"
)

// THE OWNERSHIP GATE on the match-history paths.
//
// Every read port scopes to the agent-id list it is handed — verified against real
// Postgres in store/trace_isolation_integration_test.go. That makes this gate the single
// place a foreign agent id could ever reach one, because it is what turns a
// user-supplied `?agent=` filter into a list.
//
// So the property asserted here is not "the result is empty" but the stronger one: the
// port is never CALLED with an id the caller does not own.

// gateRepo records exactly which ids reached each port.
type gateRepo struct {
	owned []string

	sawMatches   [][]string
	sawSummary   [][]string
	sawRoster    [][]string
	sawEvents    [][]string
	sawDecisions [][]string
}

func (g *gateRepo) OwnedAgentIDs(context.Context, string) ([]string, error) { return g.owned, nil }

func (g *gateRepo) Matches(_ context.Context, ids []string, _ MatchMode, _, _ int) ([]MatchSummary, int, error) {
	g.sawMatches = append(g.sawMatches, ids)
	return nil, 0, nil
}
func (g *gateRepo) MatchSummaryFor(_ context.Context, ids []string, _ string) (MatchSummary, error) {
	g.sawSummary = append(g.sawSummary, ids)
	return MatchSummary{}, ErrNoMatch
}
func (g *gateRepo) Roster(_ context.Context, _ string, ids []string) ([]RosterSeat, error) {
	g.sawRoster = append(g.sawRoster, ids)
	return nil, nil
}
func (g *gateRepo) MatchEvents(_ context.Context, ids []string, _ string, _ int) ([]MatchRow, error) {
	g.sawEvents = append(g.sawEvents, ids)
	return nil, nil
}
func (g *gateRepo) MatchDecisions(_ context.Context, ids []string, _ string, _ int) ([]Decision, error) {
	g.sawDecisions = append(g.sawDecisions, ids)
	return nil, nil
}

// assertOnlyOwned fails if any recorded call carried an id outside the owned set.
func assertOnlyOwned(t *testing.T, port string, calls [][]string, owned []string) {
	t.Helper()
	ok := map[string]bool{}
	for _, id := range owned {
		ok[id] = true
	}
	for _, call := range calls {
		for _, id := range call {
			if !ok[id] {
				t.Fatalf("%s was called with %q, which the caller does not own — the port "+
					"filters on exactly this list, so a foreign id here IS the leak", port, id)
			}
		}
	}
}

func gateService(g *gateRepo) *Service {
	// No Lens endpoint: this test is about the ownership gate, which runs before any
	// remote is consulted.
	s := New(g, "", "", "", nil)
	s.SetMatchRepo(g)
	return s
}

func TestMatchesGateNarrowsToOwnedAgent(t *testing.T) {
	g := &gateRepo{owned: []string{"ag_mine", "ag_also_mine"}}
	svc := gateService(g)

	// No filter ⇒ all of the caller's own agents, and nothing else.
	if _, _, err := svc.Matches(context.Background(), "u_me", "", ModeAny, 20, 0); err != nil {
		t.Fatal(err)
	}
	assertOnlyOwned(t, "Matches", g.sawMatches, g.owned)
	if len(g.sawMatches) != 1 || len(g.sawMatches[0]) != 2 {
		t.Fatalf("unfiltered call passed %v, want both owned agents", g.sawMatches)
	}

	// A filter on an OWNED agent narrows to exactly that one.
	g.sawMatches = nil
	if _, _, err := svc.Matches(context.Background(), "u_me", "ag_mine", ModeAny, 20, 0); err != nil {
		t.Fatal(err)
	}
	if len(g.sawMatches) != 1 || len(g.sawMatches[0]) != 1 || g.sawMatches[0][0] != "ag_mine" {
		t.Fatalf("owned filter passed %v, want [ag_mine]", g.sawMatches)
	}
}

func TestMatchesGateRefusesUnownedAgent(t *testing.T) {
	g := &gateRepo{owned: []string{"ag_mine"}}
	svc := gateService(g)

	// Asking for somebody else's agent must not reach the port with that id at all.
	list, total, err := svc.Matches(context.Background(), "u_me", "ag_rival", ModeAny, 20, 0)
	if err != nil {
		t.Fatalf("an unowned filter should be an empty result, not an error: %v", err)
	}
	if len(list) != 0 || total != 0 {
		t.Fatalf("unowned filter returned %d rows (total %d)", len(list), total)
	}
	assertOnlyOwned(t, "Matches", g.sawMatches, g.owned)
	// Deliberately NOT an authorisation error: a 403 for "ag_rival" and an empty result
	// for a typo would confirm which agent ids exist.
}

func TestMatchDetailGatePassesOnlyOwnedIDs(t *testing.T) {
	g := &gateRepo{owned: []string{"ag_mine"}}
	svc := gateService(g)

	// The summary is the first gate and it reports no match, so the detail stops there —
	// but every port it did touch must have been scoped.
	if _, err := svc.MatchDetail(context.Background(), "u_me", "m_someone_elses"); err == nil {
		t.Fatal("a match the caller has no seat in must not resolve")
	}
	assertOnlyOwned(t, "MatchSummaryFor", g.sawSummary, g.owned)
	assertOnlyOwned(t, "Roster", g.sawRoster, g.owned)
	assertOnlyOwned(t, "MatchEvents", g.sawEvents, g.owned)
	assertOnlyOwned(t, "MatchDecisions", g.sawDecisions, g.owned)
}

func TestMatchDetailRefusesCallerWithNoAgents(t *testing.T) {
	g := &gateRepo{owned: nil}
	svc := gateService(g)

	if _, err := svc.MatchDetail(context.Background(), "u_new", "m_1"); err == nil {
		t.Fatal("a caller with no agents must not resolve any match")
	}
	// And it must not have queried at all: an empty id list reaching a port is one
	// `ANY(NULL)` bug away from matching every row on the platform.
	if len(g.sawSummary)+len(g.sawEvents)+len(g.sawDecisions) != 0 {
		t.Fatalf("a caller with no agents still hit the ports: summary=%d events=%d decisions=%d",
			len(g.sawSummary), len(g.sawEvents), len(g.sawDecisions))
	}
}
