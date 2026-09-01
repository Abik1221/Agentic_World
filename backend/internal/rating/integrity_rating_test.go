package rating

import (
	"context"
	"testing"

	"github.com/agent-arena/arena/internal/integrity"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
)

// applySpy records whether the rating write was reached, and with which seats.
type applySpy struct {
	Repo
	called bool
	seats  []string
}

func (a *applySpy) ApplyMatch(_ context.Context, in ApplyInput) (bool, error) {
	a.called = true
	a.seats = nil
	for _, p := range in.Players {
		a.seats = append(a.seats, p.AgentPublicID)
	}
	return true, nil
}

func svc(t *testing.T, repo Repo) *Service {
	t.Helper()
	return New(repo, platform.NewClock(), Config{}, prometheus.NewRegistry())
}

// TestVoidedMatchIsNotRated is the regression guard for the free climb.
//
// A staked 1v1 whose seat proved nothing was refunded (match/service.go:1488-1497) and then
// rated anyway (:1551-1558). The scripted seat paid nothing, lost nothing, and gained rating.
func TestVoidedMatchIsNotRated(t *testing.T) {
	spy := &applySpy{}
	err := svc(t, spy).Rate(context.Background(), MatchResult{
		MatchPublicID: "m1", Game: GameGoofspiel,
		Players: []PlayerResult{
			{AgentPublicID: "honest", Seat: 0, Placement: 1},
			{AgentPublicID: "script", Seat: 1, Placement: 2},
		},
		Integrity: integrity.Verdict{Armed: true, Unproven: map[string]bool{"script": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if spy.called {
		t.Fatalf("a voided 1v1 still reached the rating write with seats %v", spy.seats)
	}
}

// TestCleanMatchIsStillRated. The guard must not be a blanket refusal.
func TestCleanMatchIsStillRated(t *testing.T) {
	spy := &applySpy{}
	err := svc(t, spy).Rate(context.Background(), MatchResult{
		MatchPublicID: "m2", Game: GameGoofspiel,
		Players: []PlayerResult{
			{AgentPublicID: "a", Seat: 0, Placement: 1},
			{AgentPublicID: "b", Seat: 1, Placement: 2},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !spy.called || len(spy.seats) != 2 {
		t.Fatalf("a clean match was not rated (called=%v seats=%v)", spy.called, spy.seats)
	}
}

// TestNPlayerTableDropsOnlyTheBlockedSeat. Letting one bad seat cancel eleven other agents'
// rated game would be a griefing tool, mirroring FilterPayable's reasoning.
func TestNPlayerTableDropsOnlyTheBlockedSeat(t *testing.T) {
	var players []PlayerResult
	for i, id := range []string{"a", "b", "c", "cheat"} {
		players = append(players, PlayerResult{AgentPublicID: id, Seat: i, Placement: i + 1})
	}
	spy := &applySpy{}
	err := svc(t, spy).Rate(context.Background(), MatchResult{
		MatchPublicID: "m3", Game: GameMafia, Players: players,
		Integrity: integrity.Verdict{Armed: true, Unproven: map[string]bool{"cheat": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !spy.called {
		t.Fatal("a 3-survivor table must still be rated")
	}
	if len(spy.seats) != 3 {
		t.Fatalf("rated seats %v, want the three clean ones", spy.seats)
	}
	for _, s := range spy.seats {
		if s == "cheat" {
			t.Fatal("the blocked seat was still rated")
		}
	}
}

// TestUnarmedVerdictRatesEverything. "Nothing was measured" must never read as "everyone
// failed" — getting that backwards would void honest play in bulk.
func TestUnarmedVerdictRatesEverything(t *testing.T) {
	spy := &applySpy{}
	err := svc(t, spy).Rate(context.Background(), MatchResult{
		MatchPublicID: "m4", Game: GameGoofspiel,
		Players: []PlayerResult{
			{AgentPublicID: "a", Seat: 0, Placement: 1},
			{AgentPublicID: "b", Seat: 1, Placement: 2},
		},
		Integrity: integrity.Verdict{Armed: false, Unproven: map[string]bool{"a": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !spy.called || len(spy.seats) != 2 {
		t.Fatalf("an unarmed verdict blocked rating (called=%v seats=%v)", spy.called, spy.seats)
	}
}
