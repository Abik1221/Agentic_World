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

// Agent badge codes.
const (
	Certified      = "certified"       // earned an endpoint-verified manifest
	SeasonChampion = "season_champion" // finished #1 in a completed season
	FirstWin       = "first_win"       // won a first competitive match
)

// Developer (P-Index) badge codes — awarded to the OWNER, not an agent.
const (
	DevTop100      = "dev_top_100"            // P-Index global rank ≤ 100
	DevTop10       = "dev_top_10"             // rank ≤ 10
	DevTop1Pct     = "dev_top_1pct"           // top 1% by P-Index
	DevChampion    = "dev_champion"           // #1 developer by P-Index
	DevConsistency = "dev_consistency_master" // consistency dimension ≥ 800
	DevUnderdog    = "dev_underdog_winner"    // beat an opponent 200+ rating above
	DevStreak10    = "dev_streak_10"          // a 10-match win streak
	DevWins50      = "dev_wins_50"            // 50 career wins (this season)
	DevWins100     = "dev_wins_100"           // 100 career wins (this season)
)

// Labels maps codes to human display names (for clients that want them).
var Labels = map[string]string{
	Certified:      "Certified Agent",
	SeasonChampion: "Season Champion",
	FirstWin:       "First Win",
	DevTop100:      "Top 100",
	DevTop10:       "Top 10",
	DevTop1Pct:     "Top 1%",
	DevChampion:    "Champion",
	DevConsistency: "Consistency Master",
	DevUnderdog:    "Underdog Winner",
	DevStreak10:    "10 Win Streak",
	DevWins50:      "50 Wins",
	DevWins100:     "100 Wins",
}

// Thresholds for developer badges (kept here, alongside the codes).
const (
	underdogGap    = 200 // opponent rating above yours to count as an upset
	consistencyMin = 800 // consistency sub-score for the Consistency Master badge
)

// Repo persists awards and reads the facts the developer badges depend on.
type Repo interface {
	// Award grants an AGENT badge idempotently; awarded is false if already held.
	Award(ctx context.Context, agentPublicID, code string) (awarded bool, err error)
	// AwardDeveloper grants a DEVELOPER badge idempotently.
	AwardDeveloper(ctx context.Context, userPublicID, code string) (awarded bool, err error)
	// OwnerOf resolves an agent's owning developer (public id).
	OwnerOf(ctx context.Context, agentPublicID string) (userPublicID string, found bool, err error)
	// DeveloperRank returns a developer's P-Index rank + percentile for a season.
	DeveloperRank(ctx context.Context, userPublicID string, season int) (rank int, percentile float64, found bool, err error)
	// DeveloperTotals returns a developer's aggregate wins + best streak for a season.
	DeveloperTotals(ctx context.Context, userPublicID string, season int) (wins, bestStreak int, err error)
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

func (s *Service) awardDeveloper(ctx context.Context, userPublicID, code string) error {
	awarded, err := s.repo.AwardDeveloper(ctx, userPublicID, code)
	if err != nil {
		return err
	}
	if awarded {
		s.log.Info("developer badge awarded", "developer", userPublicID, "badge", code)
	}
	return nil
}

// OnPIndexUpdated awards rank-threshold + consistency developer badges. It reads the
// developer's freshly-ranked position (the recompute worker ranks the season right
// after emitting this), so awards reflect current standing. Idempotent.
func (s *Service) OnPIndexUpdated(ctx context.Context, e events.Event) error {
	var p struct {
		Developer     string `json:"developer"`
		Season        int    `json:"season"`
		Contributions []struct {
			Key   string  `json:"key"`
			Score float64 `json:"score"`
		} `json:"contributions"`
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	if p.Developer == "" {
		return nil
	}
	// Consistency Master: the consistency dimension cleared the bar this recompute.
	for _, c := range p.Contributions {
		if c.Key == "consistency" && c.Score >= consistencyMin {
			if err := s.awardDeveloper(ctx, p.Developer, DevConsistency); err != nil {
				return err
			}
		}
	}
	// Rank thresholds.
	rank, percentile, found, err := s.repo.DeveloperRank(ctx, p.Developer, p.Season)
	if err != nil {
		return err
	}
	if !found || rank <= 0 {
		return nil
	}
	if rank == 1 {
		if err := s.awardDeveloper(ctx, p.Developer, DevChampion); err != nil {
			return err
		}
	}
	if rank <= 10 {
		if err := s.awardDeveloper(ctx, p.Developer, DevTop10); err != nil {
			return err
		}
	}
	if rank <= 100 {
		if err := s.awardDeveloper(ctx, p.Developer, DevTop100); err != nil {
			return err
		}
	}
	if percentile > 0 && percentile <= 1 {
		if err := s.awardDeveloper(ctx, p.Developer, DevTop1Pct); err != nil {
			return err
		}
	}
	return nil
}

// OnRatingUpdated awards win-count, streak, and underdog developer badges. The event
// carries every participant's rating_before + finishing rank, so the upset check
// needs no extra reads; win/streak totals come from the developer's aggregate record.
func (s *Service) OnRatingUpdated(ctx context.Context, e events.Event) error {
	var p struct {
		Season int `json:"season"`
		Agents []struct {
			Agent        string `json:"agent"`
			RatingBefore int    `json:"rating_before"`
			Rank         int    `json:"rank"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	if len(p.Agents) == 0 {
		return nil
	}
	// Winning rank = the minimum finishing rank in the match.
	minRank := p.Agents[0].Rank
	for _, a := range p.Agents {
		if a.Rank < minRank {
			minRank = a.Rank
		}
	}
	// The strongest opponent's pre-match rating (for the upset check).
	maxOppBefore := 0
	for _, a := range p.Agents {
		if a.RatingBefore > maxOppBefore {
			maxOppBefore = a.RatingBefore
		}
	}

	seen := map[string]bool{} // dedupe owners within a match
	for _, a := range p.Agents {
		owner, found, err := s.repo.OwnerOf(ctx, a.Agent)
		if err != nil {
			return err
		}
		if !found || seen[owner] {
			continue
		}
		seen[owner] = true

		// Win-count + streak from the developer's aggregate record.
		wins, streak, err := s.repo.DeveloperTotals(ctx, owner, p.Season)
		if err != nil {
			return err
		}
		if streak >= 10 {
			if err := s.awardDeveloper(ctx, owner, DevStreak10); err != nil {
				return err
			}
		}
		if wins >= 100 {
			if err := s.awardDeveloper(ctx, owner, DevWins100); err != nil {
				return err
			}
		} else if wins >= 50 {
			if err := s.awardDeveloper(ctx, owner, DevWins50); err != nil {
				return err
			}
		}
		// Underdog: this agent won while an opponent was 200+ rating above it.
		if a.Rank == minRank && maxOppBefore-a.RatingBefore >= underdogGap {
			if err := s.awardDeveloper(ctx, owner, DevUnderdog); err != nil {
				return err
			}
		}
	}
	return nil
}
