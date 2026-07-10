package match

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// mover is the socket surface the driver needs — satisfied by *agentgw.Gateway.
// Kept as a narrow interface so the drive loop is decoupled and unit-testable.
type mover interface {
	Connected(agentID string) bool
	Turn(ctx context.Context, agentID string, view, out any) error
	GameEnd(ctx context.Context, agentID, game, matchID string, result json.RawMessage) error
}

// driver auto-plays a paired, staked match by driving each agent's seat over its
// authenticated socket: it asks the connected agent for a card (over the local-
// runtime WebSocket) and submits it via DriveAct. A seat whose agent is NOT
// connected is left to self-drive (HTTP) + the sweeper's deterministic timeout, so
// an offline / hosted-endpoint agent never blocks the match.
//
// This is the "agents-vs-agents" live loop — both seats staked equally at pairing,
// so no house money is involved; settlement is the existing finalize/Settle path.
// Enabled by cfg.RankedAutoDrive (off by default until integration-tested with two
// live agents); when off, paired agents self-drive over HTTP exactly as before.
type driver struct {
	gw       mover
	log      *slog.Logger
	maxMatch time.Duration
}

// EnableRankedDrive turns on socket auto-driving of paired matches.
func (s *Service) EnableRankedDrive(gw mover, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	s.driver = &driver{gw: gw, log: log, maxMatch: 5 * time.Minute}
}

// goofspielTurnView is the self-contained per-seat JSON asked over the socket; it
// mirrors what the SDK's goofspiel handler expects.
type goofspielTurnView struct {
	Game         string `json:"game"`
	MatchID      string `json:"match_id"`
	Round        int    `json:"round"`
	CurrentPrize int    `json:"current_prize"`
	PrizePool    int    `json:"prize_pool"`
	YourHand     []int  `json:"your_hand"`
	LegalActions []int  `json:"legal_actions"`
	YourScore    int    `json:"your_score"`
	OppScore     int    `json:"opponent_score"`
}

type goofspielTurnMove struct {
	Round int `json:"round"`
	Card  int `json:"card"`
}

// maybeDrive spawns the auto-driver for a freshly-paired match when auto-driving is
// enabled and at least one seat's agent is connected over the socket.
func (s *Service) maybeDrive(matchID, aAgent, bAgent string) {
	if s.driver == nil || s.driver.gw == nil {
		return
	}
	if !s.driver.gw.Connected(aAgent) && !s.driver.gw.Connected(bAgent) {
		return
	}
	go s.driver.run(s, matchID, aAgent, bAgent)
}

func (d *driver) run(s *Service, matchID, aAgent, bAgent string) {
	ctx, cancel := context.WithTimeout(context.Background(), d.maxMatch)
	defer cancel()
	agents := []string{aAgent, bAgent}
	for {
		if ctx.Err() != nil {
			d.log.Warn("ranked drive: deadline exceeded", "match", matchID)
			return
		}
		base, err := s.State(ctx, matchID, aAgent, false, 0)
		if err != nil {
			d.log.Warn("ranked drive: state read failed", "match", matchID, "err", err)
			return
		}
		if base.Status != StatusActive {
			// Best-effort game-end to each connected agent.
			for _, id := range agents {
				if d.gw.Connected(id) {
					v, _ := s.State(ctx, matchID, id, false, 0)
					result, _ := json.Marshal(v.Result)
					_ = d.gw.GameEnd(context.WithoutCancel(ctx), id, "goofspiel", matchID, result)
				}
			}
			return
		}
		acted := false
		for _, id := range agents {
			if !d.gw.Connected(id) {
				continue // not connected — self-drive + sweeper timeout cover this seat
			}
			v, err := s.State(ctx, matchID, id, false, 0)
			if err != nil || !v.YourTurn || len(v.You.Hand) == 0 {
				continue
			}
			card := d.decide(ctx, id, matchID, v)
			if _, err := s.DriveAct(ctx, id, matchID, v.Round, card); err == nil {
				acted = true
			}
		}
		if !acted {
			// Round resolving, or the only actionable seat is a self-drive one — yield.
			time.Sleep(150 * time.Millisecond)
		}
	}
}

// decide asks the connected agent for its card; on transport failure or an illegal
// card it falls back to the lowest card in hand (deterministic, engine-legal) so a
// flaky agent loses the round rather than wedging the match.
func (d *driver) decide(ctx context.Context, agentID, matchID string, v AgentView) int {
	req := goofspielTurnView{
		Game: "goofspiel", MatchID: matchID, Round: v.Round,
		CurrentPrize: v.CurrentPrize, PrizePool: v.PrizePool,
		YourHand: v.You.Hand, LegalActions: v.LegalActions.PlayCardFrom,
		YourScore: v.You.Score, OppScore: v.Opponent.Score,
	}
	var move goofspielTurnMove
	if err := d.gw.Turn(ctx, agentID, req, &move); err == nil && containsInt(v.You.Hand, move.Card) {
		return move.Card
	}
	return lowestInt(v.You.Hand)
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func lowestInt(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	best := xs[0]
	for _, x := range xs[1:] {
		if x < best {
			best = x
		}
	}
	return best
}
