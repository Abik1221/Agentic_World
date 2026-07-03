package bot

import (
	"context"
	"log/slog"
	"time"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/demo"
	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/mafia"
)

// Runner drives rule-based demo agents (no LLM). Users bring real agents in prod;
// this fills tables locally so matches run end-to-end.
type Runner struct {
	match *match.Service
	mafia *mafia.Service
	agents []demo.Agent
	log   *slog.Logger
}

func NewRunner(matchSvc *match.Service, mafiaSvc *mafia.Service, agents []demo.Agent, log *slog.Logger) *Runner {
	return &Runner{match: matchSvc, mafia: mafiaSvc, agents: agents, log: log}
}

func (r *Runner) Run(ctx context.Context) {
	if len(r.agents) == 0 {
		return
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	idx := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick(ctx, &idx)
		}
	}
}

func (r *Runner) tick(ctx context.Context, idx *int) {
	if r.match != nil {
		r.tickGoofspiel(ctx, idx)
	}
	if r.mafia != nil {
		r.tickMafia(ctx, idx)
	}
}

func (r *Runner) tickGoofspiel(ctx context.Context, idx *int) {
	bid := int64(50)
	lobby, err := r.match.Lobby(ctx, "goofspiel", bid, "")
	if err != nil {
		return
	}
	a := r.next(idx)
	if len(lobby) > 0 {
		mid := lobby[0].PublicID
		if _, err := r.match.Join(ctx, a.PublicID, a.OwnerPublicID, mid); err == nil {
			r.playGoofspiel(ctx, a.PublicID, mid)
		}
		return
	}
	mid, err := r.match.CreateOpen(ctx, a.PublicID, a.OwnerPublicID, bid)
	if err != nil {
		return
	}
	b := r.next(idx)
	if _, err := r.match.Join(ctx, b.PublicID, b.OwnerPublicID, mid); err == nil {
		r.playGoofspiel(ctx, a.PublicID, mid)
		r.playGoofspiel(ctx, b.PublicID, mid)
	}
}

func (r *Runner) playGoofspiel(ctx context.Context, agentID, matchID string) {
	for step := 0; step < 20; step++ {
		v, err := r.match.State(ctx, matchID, agentID, false, 0)
		if err != nil || v.Status == match.StatusFinished {
			return
		}
		if v.YourTurn && len(v.You.Hand) > 0 {
			card := PickGoofspielCard(v)
			if _, err := r.match.Act(ctx, agentID, matchID, v.Round, card, ""); err != nil {
				return
			}
		}
	}
}

func (r *Runner) tickMafia(ctx context.Context, idx *int) {
	entry := int64(100)
	lobby, err := r.mafia.Lobby(ctx, entry, "")
	if err != nil {
		return
	}
	if len(lobby) > 0 {
		item := lobby[0]
		if item.SeatsFilled < item.SeatsTotal {
			a := r.next(idx)
			view, err := r.mafia.Join(ctx, a.PublicID, a.OwnerPublicID, item.PublicID)
			if err == nil {
				r.playMafia(ctx, a, view.MatchID)
			}
		}
		return
	}
	a := r.next(idx)
	mid, err := r.mafia.CreateTable(ctx, a.PublicID, a.OwnerPublicID, entry)
	if err != nil {
		return
	}
	for i := 0; i < mf.RosterSize-1; i++ {
		b := r.next(idx)
		if _, err := r.mafia.Join(ctx, b.PublicID, b.OwnerPublicID, mid); err != nil {
			return
		}
	}
	for _, ag := range r.agents[:min(len(r.agents), mf.RosterSize)] {
		r.playMafia(ctx, ag, mid)
	}
}

func (r *Runner) playMafia(ctx context.Context, a demo.Agent, matchID string) {
	for step := 0; step < 8; step++ {
		view, err := r.mafia.State(ctx, matchID, a.PublicID)
		if err != nil || view.Status == mafia.StatusFinished {
			return
		}
		act, ok := PickMafiaAction(view)
		if !ok {
			return
		}
		if _, err := r.mafia.Act(ctx, a.PublicID, matchID, act); err != nil {
			return
		}
	}
}

func (r *Runner) next(idx *int) demo.Agent {
	a := r.agents[*idx%len(r.agents)]
	*idx++
	return a
}

// PickGoofspielCard is a simple rules engine (not an LLM).
func PickGoofspielCard(v match.AgentView) int {
	best, bestDist := v.You.Hand[0], 1<<30
	target := v.PrizePool
	if target <= 0 {
		target = v.CurrentPrize
	}
	for _, c := range v.You.Hand {
		d := c - target
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			best, bestDist = c, d
		}
	}
	return best
}

// PickMafiaAction returns a deterministic rules-based move for the current phase.
func PickMafiaAction(v mafia.AgentView) (mf.Action, bool) {
	if v.Status != mafia.StatusActive || v.YourSeat == 0 {
		return mf.Action{}, false
	}
	seat := v.YourSeat
	target := firstOtherAlive(v.Alive, seat)
	if target == 0 {
		return mf.Action{}, false
	}
	switch v.Phase {
	case mf.PhaseNight:
		switch v.YourRole {
		case mf.RoleMafia:
			return mf.Action{Kind: "night_kill", Target: target}, true
		case mf.RoleDetective:
			return mf.Action{Kind: "investigate", Target: target}, true
		case mf.RoleDoctor:
			return mf.Action{Kind: "protect", Target: seat}, true
		case mf.RoleSheriff:
			return mf.Action{Kind: "profile", Target: target}, true
		}
	case mf.PhaseDiscussion:
		return mf.Action{Kind: "message", Tone: "info", Text: "Observing the table.", Target: target}, true
	case mf.PhaseVoting:
		return mf.Action{Kind: "vote", Target: target}, true
	}
	return mf.Action{}, false
}

func firstOtherAlive(alive map[int]bool, seat int) int {
	for s, ok := range alive {
		if ok && s != seat {
			return s
		}
	}
	return 0
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
