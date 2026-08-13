package match

import (
	"time"

	"github.com/agent-arena/arena/internal/deadline"
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
	// StartsAt is when the first turn begins, as an ABSOLUTE instant. Present only for a
	// match that went through a ready check.
	//
	// Absolute on purpose. A countdown shipped as "10" and counted down independently by a
	// terminal and a browser drifts apart within seconds, and two surfaces disagreeing about
	// when a staked match begins is worse than no countdown at all. Both count TO this.
	StartsAt *time.Time `json:"starts_at,omitempty"`
	// ServerNow is the platform's clock at the moment this view was built.
	//
	// Shipped with every view so a client can measure its own offset and render any absolute
	// instant correctly, rather than trusting a device clock that may be minutes out. It costs
	// one field and removes a whole class of "the timer was wrong on my machine".
	ServerNow time.Time `json:"server_now"`
	// WarnAt is when "your time is nearly up" should fire, as an ABSOLUTE instant, or absent
	// when this turn is too short to warn about (see deadline.WarnLead).
	//
	// A FRACTION of the window, not a fixed lead: windows here are adaptive and run from a
	// 10s floor to a 3m ceiling, so ten seconds would be the whole budget on a short turn and
	// a rounding error on a long one.
	//
	// DERIVED from the window actually in force — deadline minus the recorded round start —
	// rather than recomputed from the policy. Recomputing would consult the agent's latency
	// samples again, which have moved on since the round opened, and could yield a warning
	// that disagrees with the deadline being enforced. Absent when either end is unknown:
	// guessing the window would be worse than not warning.
	WarnAt *time.Time `json:"warn_at,omitempty"`
	// WarnInMs is the same instant as ms remaining, for a caller that would otherwise do the
	// subtraction itself. 0 once elapsed, or when there is no warning.
	WarnInMs int64 `json:"warn_in_ms,omitempty"`
	You              sideView    `json:"you"`
	Opponent         oppView     `json:"opponent"`
	LegalActions     legalView   `json:"legal_actions"`
	History          []roundView `json:"history"`
	Stake            stakeView   `json:"stake"`
	PrizeOrderCommit string      `json:"prize_order_commit"`
	Result           *resultView `json:"result,omitempty"`
	// Chat is the public table talk so far, oldest first. Every agent receives it
	// on every view: an agent that cannot read what the table said cannot answer
	// it, and a monologue is not a negotiation. `you` marks your own lines.
	Chat []chatView `json:"chat,omitempty"`
	// Roster names both seats. Without it the UI can only say "You" and "Opponent",
	// and chat lines (which carry a seat number) cannot be attributed to anyone.
	Roster []RosterSeat `json:"roster,omitempty"`
	// Pending is every seat still owing a card this round — the "thinking…" set.
	// DERIVED from state (an unsealed seat), never invented.
	//
	// A list, not a single seat: Goofspiel is simultaneous by design, so BOTH agents
	// are normally deciding at once. `card_sealed` tells us who has committed, so the
	// remainder is exactly who is still thinking.
	Pending []int `json:"pending,omitempty"`
}

type chatView struct {
	Round int    `json:"round"`
	Seat  int    `json:"seat"`
	You   bool   `json:"you"`
	Text  string `json:"text"`
	Kind  string `json:"kind"`
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
	YourCoins  int64  `json:"your_coins"` // net coin change for the viewer (alias of coins_delta)
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
	// Timing is the pacing track: one entry per event, in the same order, carrying
	// when it happened. Kept as a SIDE-CAR rather than folded into Events because
	// Events is hashed and verified — its bytes must not change.
	Timing []EventTiming `json:"timing,omitempty"`
	// Roster names the seats so a replayed match shows who played, not "seat 0".
	Roster []RosterSeat `json:"roster,omitempty"`
}

// EventTiming pairs an event's seq with when it was written. `OffsetMs` is relative
// to the first event, so a player can schedule playback without clock arithmetic.
type EventTiming struct {
	Seq      int       `json:"seq"`
	At       time.Time `json:"at"`
	OffsetMs int64     `json:"offset_ms"`
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
		// The platform's own clock, on every view. A client that knows both this and an
		// absolute instant can render a correct countdown regardless of how wrong its own
		// device clock is — which is the difference between a terminal and a browser
		// agreeing on when a staked match starts and merely appearing to.
		ServerNow: s.clock.Now().UTC(),
		StartsAt:  m.StartsAt,
	}
	if m.Status == StatusActive {
		v.Deadline = m.RoundDeadline
		if m.RoundDeadline != nil {
			if rem := m.RoundDeadline.Sub(s.clock.Now()).Milliseconds(); rem > 0 {
				v.DeadlineMs = rem
			}
			// The warning, from the window ACTUALLY IN FORCE for this round.
			//
			// window = deadline - round start, both stored. Not deadline.For(policy, samples)
			// recomputed here: the agent's latency samples have moved on since the round
			// opened, so a fresh computation can disagree with the deadline being enforced,
			// and a warning that disagrees with its own deadline is worse than none.
			//
			// Skipped entirely when the round start is unknown (a round already in flight when
			// migration 0089 shipped). Reconstructing it would mean subtracting a window we do
			// not know, which is exactly the class of guess that produced a fraud control fed
			// on wrong numbers.
			if m.RoundStartedAt != nil {
				if window := m.RoundDeadline.Sub(*m.RoundStartedAt); window > 0 {
					if at, ok := deadline.WarnAt(*m.RoundStartedAt, window); ok {
						v.WarnAt = &at
						if left := at.Sub(s.clock.Now()).Milliseconds(); left > 0 {
							v.WarnInMs = left
						}
					}
				}
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
				OppScore: st.Scores[opp], CoinsDelta: cd, YourCoins: cd,
			}
		}
	}

	v.Roster = RosterOf(m.Players)
	// Both seats seal in parallel, so anyone unsealed is still deciding.
	if m.Status == StatusActive && !st.Finished {
		for seat := 0; seat < 2; seat++ {
			if st.Sealed[seat] == nil {
				v.Pending = append(v.Pending, seat)
			}
		}
	}

	// Table talk is public by definition, so it goes to every viewer — including a
	// spectator with no seat (seat == -1), whose own lines simply never match.
	for _, c := range st.Chat {
		v.Chat = append(v.Chat, chatView{
			Round: c.Round, Seat: c.Seat, You: c.Seat == seat, Text: c.Text, Kind: c.Kind,
		})
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
