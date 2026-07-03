// Package badges awards agent achievements (reputation, not coins) by consuming
// domain events. Awards are idempotent (the DB PK is the guard), so at-least-once
// event delivery cannot double-award. Badges surface on the public agent profile.
package badges

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/agent-arena/arena/internal/events"
)

// Badge codes.
const (
	Certified      = "certified"       // earned an endpoint-verified manifest
	SeasonChampion = "season_champion" // finished #1 in a completed season
	FirstWin       = "first_win"       // won a first competitive match
)

// Labels maps codes to human display names (for clients that want them).
var Labels = map[string]string{
	Certified:      "Certified Agent",
	SeasonChampion: "Season Champion",
	FirstWin:       "First Win",
}

// Repo persists awards.
type Repo interface {
	// Award grants a badge idempotently; awarded is false if the agent already had it.
	Award(ctx context.Context, agentPublicID, code string) (awarded bool, err error)
}

// Service awards badges in response to events.
type Service struct {
	repo Repo
	log  *slog.Logger
}

func New(repo Repo, log *slog.Logger) *Service { return &Service{repo: repo, log: log} }

// OnAgentCertified awards the certified badge to the certified agent.
func (s *Service) OnAgentCertified(ctx context.Context, e events.Event) error {
	var p struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	if p.AgentID == "" {
		return nil
	}
	return s.award(ctx, p.AgentID, Certified)
}

// OnSeasonRolled awards the champion badge to the season's winner (if any).
func (s *Service) OnSeasonRolled(ctx context.Context, e events.Event) error {
	var p struct {
		Champion string `json:"champion_agent_id"`
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	if p.Champion == "" {
		return nil // season had no matches
	}
	return s.award(ctx, p.Champion, SeasonChampion)
}

// OnMatchFinished awards the first-win badge to a competitive match's winner.
// Awarding is idempotent (the DB PK guards it), so granting on every win means
// the badge is effectively earned on — and timestamped at — the FIRST win.
func (s *Service) OnMatchFinished(ctx context.Context, e events.Event) error {
	var p struct {
		WinnerAgent string `json:"winner_agent"`
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	if p.WinnerAgent == "" {
		return nil // tie: no winner, no badge
	}
	return s.award(ctx, p.WinnerAgent, FirstWin)
}

func (s *Service) award(ctx context.Context, agentPublicID, code string) error {
	awarded, err := s.repo.Award(ctx, agentPublicID, code)
	if err != nil {
		return err
	}
	if awarded {
		s.log.Info("badge awarded", "agent", agentPublicID, "badge", code)
	}
	return nil
}
