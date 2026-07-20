// Package autoplay keeps a developer's agent playing "at any time" without a
// human re-triggering it. A dev deploys their agent once (WebSocket worker or a
// verified hosted endpoint), flips auto-play on in settings, and this service
// keeps that agent in matches — ranked (real stakes, gated) or sandbox (free
// practice) — by periodically topping it up.
//
// The design is intentionally stateless + self-healing: every Tick re-derives
// what each available agent needs from the *current* world (is it already queued?
// how many sandbox matches are in flight?) and acts only on the gap. So a crash,
// restart, or missed finish-event can never wedge it — the next Tick reconciles.
// Money safety lives entirely in the ranked queue's existing gates (certification,
// affordability, per-owner guardrails); this service never bypasses them.
package autoplay

import (
	"context"
	"log/slog"
)

// Mode selects which arena an available agent auto-plays in.
type Mode string

const (
	ModeSandbox Mode = "sandbox" // free practice vs house bots — no stakes
	ModeRanked  Mode = "ranked"  // real matchmaking with escrowed stakes
)

// Setting is a single agent's auto-play configuration (one row per agent).
type Setting struct {
	AgentPublicID string   `json:"agent_id"`
	OwnerPublicID string   `json:"-"` // resolved from the token, never client-set
	Enabled       bool     `json:"enabled"`
	Mode          Mode     `json:"mode"`
	Bid           int64    `json:"bid,omitempty"`   // ranked stake per match (ignored for sandbox)
	Games         []string `json:"games,omitempty"` // sandbox: games to rotate through; empty ⇒ DefaultGame
}

// Repo persists auto-play settings.
type Repo interface {
	// ListEnabled returns every agent with auto-play currently switched on.
	ListEnabled(ctx context.Context) ([]Setting, error)
	Get(ctx context.Context, agentPublicID string) (Setting, bool, error)
	Set(ctx context.Context, s Setting) error
}

// RankedQueue is the slice of matchmaking this service needs. Enqueue reuses the
// real queue's certification + affordability + guardrail gates, so an unaffordable
// or capped agent is simply skipped (the error is logged, never fatal).
type RankedQueue interface {
	Enqueue(ctx context.Context, agentPublicID, ownerPublicID string, bid int64) error
	// Queued reports whether the agent already has a live queue entry (waiting or
	// matched), so we don't double-enqueue.
	Queued(ctx context.Context, agentPublicID string) (bool, error)
}

// SandboxStarter starts a single no-stakes practice match and reports how many
// auto-play sandbox matches are already in flight for an agent (so we keep at
// most MaxSandboxConcurrent running, rather than spawning one every tick).
type SandboxStarter interface {
	StartSandbox(ctx context.Context, game, agentPublicID, ownerPublicID string) error
	ActiveCount(ctx context.Context, agentPublicID string) (int, error)
}

// DefaultGame is used for sandbox auto-play when a setting names no games.
const DefaultGame = "goofspiel"

// Config tunes the reconciler.
type Config struct {
	MaxSandboxConcurrent int // most simultaneous auto-play sandbox matches per agent
}

func (c *Config) withDefaults() {
	if c.MaxSandboxConcurrent <= 0 {
		c.MaxSandboxConcurrent = 1
	}
}

// Service reconciles available agents into matches on each Tick.
type Service struct {
	repo    Repo
	ranked  RankedQueue
	sandbox SandboxStarter
	cfg     Config
	log     *slog.Logger
	// round-robins the sandbox game per agent across ticks so a multi-game
	// setting rotates instead of always replaying the first game.
	rr map[string]int
}

func New(repo Repo, ranked RankedQueue, sandbox SandboxStarter, cfg Config, log *slog.Logger) *Service {
	cfg.withDefaults()
	if log == nil {
		log = slog.Default()
	}
	return &Service{repo: repo, ranked: ranked, sandbox: sandbox, cfg: cfg, log: log, rr: map[string]int{}}
}

// Tick reconciles every enabled agent once. It never returns an error: a failure
// for one agent (guardrail trip, transient DB blip) is logged and the rest still
// run, so one bad agent can't stall auto-play for everyone.
func (s *Service) Tick(ctx context.Context) {
	settings, err := s.repo.ListEnabled(ctx)
	if err != nil {
		s.log.Warn("autoplay: list enabled failed", "err", err)
		return
	}
	for _, set := range settings {
		if !set.Enabled || set.AgentPublicID == "" {
			continue
		}
		switch set.Mode {
		case ModeRanked:
			s.tickRanked(ctx, set)
		case ModeSandbox:
			s.tickSandbox(ctx, set)
		default:
			s.log.Warn("autoplay: unknown mode", "agent", set.AgentPublicID, "mode", set.Mode)
		}
	}
}

func (s *Service) tickRanked(ctx context.Context, set Setting) {
	if s.ranked == nil {
		return
	}
	queued, err := s.ranked.Queued(ctx, set.AgentPublicID)
	if err != nil {
		s.log.Warn("autoplay: queue status failed", "agent", set.AgentPublicID, "err", err)
		return
	}
	if queued {
		return // already waiting or in a match — nothing to do this tick
	}
	// Enqueue's own gates enforce money safety; a rejection (broke / capped /
	// cooling down) is expected and simply skipped until the next tick.
	if err := s.ranked.Enqueue(ctx, set.AgentPublicID, set.OwnerPublicID, set.Bid); err != nil {
		s.log.Debug("autoplay: ranked enqueue skipped", "agent", set.AgentPublicID, "err", err)
	}
}

func (s *Service) tickSandbox(ctx context.Context, set Setting) {
	if s.sandbox == nil {
		return
	}
	n, err := s.sandbox.ActiveCount(ctx, set.AgentPublicID)
	if err != nil {
		s.log.Warn("autoplay: sandbox active count failed", "agent", set.AgentPublicID, "err", err)
		return
	}
	if n >= s.cfg.MaxSandboxConcurrent {
		return // already at the practice-match ceiling for this agent
	}
	game := s.nextGame(set)
	if err := s.sandbox.StartSandbox(ctx, game, set.AgentPublicID, set.OwnerPublicID); err != nil {
		s.log.Debug("autoplay: sandbox start skipped", "agent", set.AgentPublicID, "game", game, "err", err)
	}
}

// nextGame round-robins across a setting's games (or DefaultGame when unset).
func (s *Service) nextGame(set Setting) string {
	games := set.Games
	if len(games) == 0 {
		return DefaultGame
	}
	i := s.rr[set.AgentPublicID] % len(games)
	s.rr[set.AgentPublicID] = (i + 1) % len(games)
	return games[i]
}
