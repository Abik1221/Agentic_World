package bot

import (
	"context"
	"log/slog"
	"time"

	"github.com/agent-arena/arena/internal/demo"
	mf "github.com/agent-arena/arena/internal/engine/mafia"
	mono "github.com/agent-arena/arena/internal/engine/monopoly"
	"github.com/agent-arena/arena/internal/mafia"
	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/monopoly"
)

// Runner drives rule-based demo agents (no LLM). Users bring real agents in prod;
// this fills tables locally so matches run end-to-end.
// StakeSource exposes the lowest enabled tier for a game. Satisfied by *gamestakes.Service.
type StakeSource interface {
	LowestEnabledCoins(ctx context.Context, game string) (int64, bool)
}

type Runner struct {
	// stakes supplies the game's configured tiers so house tables stake an amount the platform
	// actually offers. Optional (nil = fall back to the default floor).
	stakes   StakeSource
	match    *match.Service
	mafia    *mafia.Service
	monopoly *monopoly.Service
	agents   []demo.Agent
	log      *slog.Logger

	// monoMatch is the ONE demo Monopoly match currently in progress. Monopoly games
	// are long (up to DefaultMaxTurns dice rolls), so — unlike the short Goofspiel/Mafia
	// matches — we drive a single table a batch of moves per tick and only start a new
	// one once it finishes, instead of spawning a fresh table every tick (which would
	// pile up hundreds of unfinished tables).
	monoMatch  string // current demo Monopoly match id ("" ⇒ none in progress)
	monoDriver string // the demo agent seated at seat 0 of monoMatch
}

func NewRunner(matchSvc *match.Service, mafiaSvc *mafia.Service, agents []demo.Agent, log *slog.Logger) *Runner {
	return &Runner{match: matchSvc, mafia: mafiaSvc, agents: agents, log: log}
}

// WithMonopoly enables demo Monopoly self-play (chainable). Optional so callers that
// don't wire it keep compiling and simply skip Monopoly.
func (r *Runner) WithMonopoly(svc *monopoly.Service) *Runner { r.monopoly = svc; return r }

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
	if r.monopoly != nil {
		r.tickMonopoly(ctx, idx)
	}
}

// tickMonopoly advances a single demo Monopoly practice table (no entry fee): the
// Service seats server bots in the other seats and auto-drives them inside each Act;
// we drive only the creator seat. One table at a time — a batch of moves per tick —
// starting a new one only when the previous finishes. (Staked/ranked Monopoly is
// agent-vs-agent and is covered by the group queue + the money E2E, not here.)
func (r *Runner) tickMonopoly(ctx context.Context, idx *int) {
	if r.monoMatch == "" {
		a := r.next(idx)
		mid, err := r.monopoly.CreateTable(ctx, a.PublicID, a.OwnerPublicID, 0, monopoly.DefaultPlayers)
		if err != nil {
			return
		}
		r.monoMatch = mid
		r.monoDriver = a.PublicID
	}
	if r.driveMonopolyBatch(ctx, r.monoDriver, r.monoMatch, 80) { // finished?
		r.monoMatch, r.monoDriver = "", ""
	}
}

// driveMonopolyBatch plays up to `budget` creator-seat moves. Returns true when the
// match has finished (or errored / vanished), so the caller can start a fresh table.
func (r *Runner) driveMonopolyBatch(ctx context.Context, agentID, matchID string, budget int) bool {
	for step := 0; step < budget; step++ {
		v, err := r.monopoly.State(ctx, matchID, agentID, false, 0)
		if err != nil {
			return true // match gone / errored: drop it and start over next time
		}
		if v.Status == monopoly.StatusFinished {
			return true
		}
		if !v.YourTurn || len(v.Legal) == 0 {
			return false // Service still resolving bot seats; try again next tick
		}
		if _, err := r.monopoly.Act(ctx, agentID, matchID, PickMonopolyAction(v.Legal), "", true); err != nil {
			return true
		}
	}
	return false // budget spent, match still going — continue it next tick
}

// PickMonopolyAction returns a legal, PARAMETER-FREE action for whatever phase the
// creator seat is in, so a demo bot can drive any Monopoly match to completion without
// ever submitting an illegal move. The priority order plays sensibly (advance, buy
// landed property, end the turn) and safely declines the param-bearing options — skip
// an open trade window, reject an offered trade, drop out of an auction, and go bankrupt
// to settle an unpayable debt (mortgage/sell/build all need a property arg, so they are
// intentionally avoided here). Rules engine, not LLM.
func PickMonopolyAction(legal []string) mono.Action {
	has := func(k string) bool {
		for _, l := range legal {
			if l == k {
				return true
			}
		}
		return false
	}
	// First legal match wins, in this order. Every entry is a no-arg action.
	for _, k := range []string{
		mono.ActRoll, mono.ActRollJail, mono.ActPayJail, mono.ActUseJailCard,
		mono.ActBuy, mono.ActEndTurn, mono.ActSkipTrade, mono.ActRejectTrade,
		mono.ActPass, mono.ActDecline, mono.ActBankrupt,
	} {
		if has(k) {
			return mono.Action{Kind: k}
		}
	}
	return mono.Action{Kind: legal[0]} // last resort (should not happen)
}

func (r *Runner) tickGoofspiel(ctx context.Context, idx *int) {
	// The stake comes from the game's tier table, not from a constant here. It WAS `int64(50)`,
	// against a configured floor of 500, and because this runner calls CreateOpen directly it
	// never met the handler's tier check — 711 goofspiel tables were opened at a stake the game
	// does not offer. A house bot advertising an impossible stake is also a lie to any developer
	// browsing the lobby.
	bid := r.lowestStake(ctx, "goofspiel", 500)
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
		// Goofspiel is SIMULTANEOUS: each round resolves only once BOTH seats have
		// sealed. Driving one seat to completion then the other stalls the match
		// after round 1 (each waits on the other); interleave them instead so the
		// full match plays out and finalises (which is what feeds the ratings).
		r.driveGoofspielPair(ctx, mid, a.PublicID, b.PublicID)
	}
}

// driveGoofspielPair plays both demo seats round-by-round until the match ends.
func (r *Runner) driveGoofspielPair(ctx context.Context, matchID, agentA, agentB string) {
	for step := 0; step < 40; step++ { // 13 rounds × 2 seats + headroom
		for _, id := range [2]string{agentA, agentB} {
			v, err := r.match.State(ctx, matchID, id, false, 0)
			if err != nil {
				return
			}
			if v.Status == match.StatusFinished {
				return
			}
			if v.YourTurn && len(v.You.Hand) > 0 {
				if _, err := r.match.Act(ctx, id, matchID, v.Round, PickGoofspielCard(v), ""); err != nil {
					return
				}
			}
		}
	}
}

// playGoofspiel drives a single demo seat (used when a demo bot joins a match a
// real user created — the user drives their own seat).
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
	entry := r.lowestStake(ctx, "mafia", 500) // was int64(100); see tickGoofspiel
	lobby, err := r.mafia.Lobby(ctx, entry, "")
	if err != nil {
		return
	}
	if len(lobby) > 0 {
		item := lobby[0]
		if item.SeatsFilled < item.SeatsTotal {
			a := r.next(idx)
			// Seat one and RETURN. Deliberately no driving from here.
			//
			// Two attempts failed before this. Driving unconditionally burned the whole tick on a
			// table that could not start, and even gated on "this join completes the roster" the
			// lobby path still starves the create path: while any partial table exists the bot
			// only ever adds a single seat per tick, so it never gets to build a full one in a
			// single pass. The create path below fills all 12 at once, and drives what it filled.
			_, _ = r.mafia.Join(ctx, a.PublicID, a.OwnerPublicID, item.PublicID)
		}
		return
	}
	a := r.next(idx)
	mid, err := r.mafia.CreateTable(ctx, a.PublicID, a.OwnerPublicID, entry)
	if err != nil {
		return
	}
	// Fill the roster. A failure here used to `return` silently, abandoning a table with ONE
	// seat that then sat until it timed out and aborted — 190 aborted mafia tables against 1
	// finished, every abort holding exactly 1 seat, and no log line anywhere saying why.
	//
	// The seating is still all-or-nothing (Mafia cannot start short), but the reason is now
	// recorded and the half-filled table is cancelled instead of left to expire. A table nobody
	// can join should not sit in the lobby advertising a game that will never start.
	seated := 1
	for i := 0; i < mf.RosterSize-1; i++ {
		b := r.next(idx)
		if _, err := r.mafia.Join(ctx, b.PublicID, b.OwnerPublicID, mid); err != nil {
			slog.Warn("bot: mafia roster could not be filled; abandoning the table",
				"match", mid, "seated", seated, "need", mf.RosterSize, "entry_fee", entry,
				"error", err)
			if cerr := r.mafia.Cancel(ctx, a.PublicID, mid); cerr != nil {
				slog.Warn("bot: half-filled mafia table could not be cancelled and will sit "+
					"in the lobby until it expires", "match", mid, "error", cerr)
			}
			return
		}
		seated++
	}
	r.driveMafia(ctx, r.agents[:min(len(r.agents), mf.RosterSize)], mid)
}

// driveMafia carries a table to its conclusion by cycling EVERY seat each round.
//
// # The bug this replaces
//
// playMafia gave each agent a private budget of 8 steps and was called once per agent in
// sequence. It returned the moment PickMafiaAction found nothing to do — which is immediately,
// because a Mafia phase requires ALL living seats to act before it advances. So seat 1 acted
// once and returned, seat 2 acted once and returned, and after one pass the phase advanced with
// nobody left to play it. Every table sat in 'active' until it timed out.
//
// It looked like a budget that was too small. It was the wrong shape: a per-seat loop cannot
// drive a game whose progress condition is collective.
//
// Nine tables were reaching a full 12 seats and starting; all-time finished stayed at 1.
func (r *Runner) driveMafia(ctx context.Context, agents []demo.Agent, matchID string) {
	// Generous, because a 12-player game runs several days of night/discussion/voting, and the
	// no-progress exit below is what actually ends the loop in the normal case.
	const maxRounds = 200
	for round := 0; round < maxRounds; round++ {
		progressed := false
		for _, a := range agents {
			view, err := r.mafia.State(ctx, matchID, a.PublicID, false, 0)
			if err != nil {
				continue // one unreadable seat must not abandon the table
			}
			if view.Status == mafia.StatusFinished {
				return
			}
			act, ok := PickMafiaAction(view)
			if !ok {
				continue // nothing pending for THIS seat right now; others may still act
			}
			// platform-driven bot: no stale-phase guard, no per-move signature
			if _, err := r.mafia.Act(ctx, a.PublicID, matchID, act, 0, "", "", true); err != nil {
				continue
			}
			progressed = true
		}
		if !progressed {
			// No seat could act anywhere on the table. That is a phase waiting on a timer rather
			// than on us, so spinning would burn CPU without moving the game.
			return
		}
	}
	slog.Warn("bot: mafia table hit the round cap without finishing",
		"match", matchID, "rounds", maxRounds)
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

// lowestStake returns the game's lowest enabled tier, falling back to def when no tier source is
// wired or the table cannot be read.
//
// The fallback is the DEFAULT FLOOR rather than a cheap constant on purpose: if the tiers cannot
// be read, the safe direction is a stake the platform is known to accept, not one it is known to
// reject. Erring cheap is what produced the sub-floor tables in the first place.
func (r *Runner) lowestStake(ctx context.Context, game string, def int64) int64 {
	if r.stakes == nil {
		return def
	}
	if lowest, ok := r.stakes.LowestEnabledCoins(ctx, game); ok && lowest > 0 {
		return lowest
	}
	return def
}

// WithStakes wires the tier table so house tables use a stake the game actually offers.
// Chainable, matching WithMonopoly.
func (r *Runner) WithStakes(src StakeSource) *Runner { r.stakes = src; return r }
