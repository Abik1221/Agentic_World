package match

import (
	"time"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
)

// AgentView is the redacted, per-viewer state returned to an agent. It never
// exposes the opponent's sealed card before reveal, nor the future prize order.
type AgentView struct {
	MatchID          string      `json:"match_id"`
	Game             string      `json:"game"`
	Status           string      `json:"status"`
	Mode             string      `json:"mode"` // "competitive" | "sandbox"
	Round            int         `json:"round"`
	TotalRounds      int         `json:"total_rounds"`
	CurrentPrize     int         `json:"current_prize"`
	PrizePool        int         `json:"prize_pool"`
	YourTurn         bool        `json:"your_turn"`
	Deadline         *time.Time  `json:"deadline,omitempty"`
	MoveWindowMs     int64       `json:"move_window_ms"`        // total per-move budget (the shot clock)
	DeadlineMs       int64       `json:"deadline_ms,omitempty"` // ms remaining until the deadline (0 once elapsed / not your turn)
	You              sideView    `json:"you"`
	Opponent         oppView     `json:"opponent"`
	LegalActions     legalView   `json:"legal_actions"`
	History          []roundView `json:"history"`
	Stake            stakeView   `json:"stake"`
	PrizeOrderCommit string      `json:"prize_order_commit"`
	Result           *resultView `json:"result,omitempty"`
}

type sideView struct {
	Hand  []int `json:"hand"`
	Score int   `json:"score"`
}
type oppView struct {
	Hand     []int `json:"hand"`
	Score    int   `json:"score"`
	HasActed bool  `json:"has_acted"`
}
type legalView struct {
	PlayCardFrom []int `json:"play_card_from"`
}
type roundView struct {
	Round     int    `json:"round"`
	Prize     int    `json:"prize"`
	PrizePool int    `json:"prize_pool"`
	YourCard  int    `json:"your_card"`
	OppCard   int    `json:"opp_card"`
	Winner    string `json:"winner"` // "you" | "opponent" | "tie"
}
type stakeView struct {
	YourCoins int64 `json:"your_coins"`
	OppCoins  int64 `json:"opp_coins"`
	RakePct   int   `json:"rake_pct"`
}
type resultView struct {
	Winner     string `json:"winner"`
	YourScore  int    `json:"your_score"`
	OppScore   int    `json:"opp_score"`
	CoinsDelta int64  `json:"coins_delta"`
}

// ReplayDoc is the public, verifiable match record. Seed is populated only once
// the match is finished (provable-fairness reveal). MoveProofs + MovesVerified
// expose per-move authenticity: each signature is re-checkable against the
// signer's public key, proving the agent authored that exact card.
type ReplayDoc struct {
	MatchID       string          `json:"match_id"`
	EngineVersion string          `json:"engine_version"`
	Commit        string          `json:"prize_seed_commit"`
	Seed          []byte          `json:"prize_seed,omitempty"`
	ReplayHash    string          `json:"replay_hash"`
	Status        string          `json:"status"`
	Events        []gs.Event      `json:"events"`
	MoveProofs    []MoveSignature `json:"move_proofs,omitempty"`
	MovesVerified *bool           `json:"moves_verified,omitempty"` // true only if every move is signed + valid
}

// view projects a Match into the redacted AgentView for a given viewer.
func (s *Service) view(m Match, viewerAgentPublicID string) AgentView {
	seat := -1
	if p := m.playerByAgent(viewerAgentPublicID); p != nil {
		seat = p.Seat
	}
	st := m.State

	mode := m.Mode
	if mode == "" {
		mode = ModeCompetitive
	}
	v := AgentView{
		MatchID: m.PublicID, Game: m.Game, Status: m.Status, Mode: mode,
		Round: st.Round, TotalRounds: m.TotalRounds,
		CurrentPrize: st.CurrentPrize(), PrizePool: st.PrizePool,
		PrizeOrderCommit: m.Commit,
		Stake:            stakeView{YourCoins: m.Bid, OppCoins: m.Bid, RakePct: m.RakePct},
		MoveWindowMs:     s.cfg.MoveWindow.Milliseconds(),
	}
	if m.Status == StatusActive {
		v.Deadline = m.RoundDeadline
		if m.RoundDeadline != nil {
			if rem := m.RoundDeadline.Sub(s.clock.Now()).Milliseconds(); rem > 0 {
				v.DeadlineMs = rem
			}
		}
	}

	if seat == gs.SeatA || seat == gs.SeatB {
		opp := 1 - seat
		v.You = sideView{Hand: append([]int(nil), st.Hands[seat]...), Score: st.Scores[seat]}
		v.Opponent = oppView{
			Hand:     append([]int(nil), st.Hands[opp]...),
			Score:    st.Scores[opp],
			HasActed: st.Sealed[opp] != nil,
		}
		v.YourTurn = m.Status == StatusActive && !st.Finished && st.Sealed[seat] == nil
		if v.YourTurn {
			v.LegalActions.PlayCardFrom = append([]int(nil), st.Hands[seat]...)
		}
		for _, r := range st.History {
			v.History = append(v.History, roundView{
				Round: r.Round, Prize: r.Prize, PrizePool: r.PrizePool,
				YourCard: r.Cards[seat], OppCard: r.Cards[opp],
				Winner: winnerLabel(r.Winner, seat),
			})
		}
		if st.Finished {
			cd := int64(0)
			if p := m.playerBySeat(seat); p != nil {
				cd = p.CoinsDelta
			}
			v.Result = &resultView{
				Winner: winnerLabel(st.Winner, seat), YourScore: st.Scores[seat],
				OppScore: st.Scores[opp], CoinsDelta: cd,
			}
		}
	}
	return v
}

func winnerLabel(winnerSeat, viewerSeat int) string {
	switch {
	case winnerSeat == gs.Tie:
		return "tie"
	case winnerSeat == viewerSeat:
		return "you"
	default:
		return "opponent"
	}
}
