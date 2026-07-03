package badges

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/agent-arena/arena/internal/events"
)

type fakeRepo struct{ awarded map[string]int } // key "agent|code" -> times newly awarded

func newFakeRepo() *fakeRepo { return &fakeRepo{awarded: map[string]int{}} }

func (f *fakeRepo) Award(_ context.Context, agent, code string) (bool, error) {
	key := agent + "|" + code
	if f.awarded[key] > 0 {
		return false, nil // already had it
	}
	f.awarded[key] = 1
	return true, nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func evt(t string, payload any) events.Event {
	b, _ := json.Marshal(payload)
	return events.Event{ID: "evt_x", Type: t, Payload: b}
}

func TestOnAgentCertified_Awards(t *testing.T) {
	repo := newFakeRepo()
	s := New(repo, quiet())
	if err := s.OnAgentCertified(context.Background(), evt(events.TypeAgentCertified, map[string]string{"agent_id": "ag_1"})); err != nil {
		t.Fatal(err)
	}
	if repo.awarded["ag_1|certified"] != 1 {
		t.Fatal("certified badge not awarded")
	}
	// Idempotent: redelivery does not re-award (no error, no duplicate).
	if err := s.OnAgentCertified(context.Background(), evt(events.TypeAgentCertified, map[string]string{"agent_id": "ag_1"})); err != nil {
		t.Fatal(err)
	}
	if repo.awarded["ag_1|certified"] != 1 {
		t.Fatal("redelivery re-awarded")
	}
}

func TestOnSeasonRolled_AwardsChampion(t *testing.T) {
	repo := newFakeRepo()
	s := New(repo, quiet())
	if err := s.OnSeasonRolled(context.Background(), evt(events.TypeSeasonRolled, map[string]any{"season": 3, "champion_agent_id": "ag_champ"})); err != nil {
		t.Fatal(err)
	}
	if repo.awarded["ag_champ|season_champion"] != 1 {
		t.Fatal("champion badge not awarded")
	}
}

func TestOnSeasonRolled_NoChampionNoAward(t *testing.T) {
	repo := newFakeRepo()
	s := New(repo, quiet())
	if err := s.OnSeasonRolled(context.Background(), evt(events.TypeSeasonRolled, map[string]any{"season": 4, "champion_agent_id": ""})); err != nil {
		t.Fatal(err)
	}
	if len(repo.awarded) != 0 {
		t.Fatalf("empty champion must not award, got %v", repo.awarded)
	}
}

func TestOnMatchFinished_AwardsWinnerFirstWin(t *testing.T) {
	repo := newFakeRepo()
	s := New(repo, quiet())
	if err := s.OnMatchFinished(context.Background(), evt(events.TypeMatchFinished, map[string]string{"match_id": "m_1", "winner_agent": "ag_win"})); err != nil {
		t.Fatal(err)
	}
	if repo.awarded["ag_win|first_win"] != 1 {
		t.Fatal("first_win not awarded to winner")
	}
	// Idempotent across wins → effectively "first win only".
	if err := s.OnMatchFinished(context.Background(), evt(events.TypeMatchFinished, map[string]string{"match_id": "m_2", "winner_agent": "ag_win"})); err != nil {
		t.Fatal(err)
	}
	if repo.awarded["ag_win|first_win"] != 1 {
		t.Fatal("second win re-awarded first_win")
	}
}

func TestOnMatchFinished_TieNoAward(t *testing.T) {
	repo := newFakeRepo()
	s := New(repo, quiet())
	if err := s.OnMatchFinished(context.Background(), evt(events.TypeMatchFinished, map[string]string{"match_id": "m_3", "winner_agent": ""})); err != nil {
		t.Fatal(err)
	}
	if len(repo.awarded) != 0 {
		t.Fatalf("tie must not award, got %v", repo.awarded)
	}
}
