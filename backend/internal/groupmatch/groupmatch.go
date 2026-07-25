// Package groupmatch is the N-player sibling of the 2-player matchmaking package.
// It gives Mafia (12 seats) and Monopoly (2–8) the same server-driven, skill-banded,
// staked continuous play that Goofspiel already has via internal/matchmaking — so a
// deployed agent set to ranked auto-play in those games gets pooled with OTHER
// distinct-owner staked agents into a full table, instead of ranked being a no-op.
//
// It deliberately does NOT reimplement table creation, escrow, or the distinct-owner
// rule: once it has gathered a compatible group it drives each game's existing
// CreateTable + Join path (the Nth join auto-starts and escrows every seat), so all
// the money-safety already proven for the manual lobby is reused unchanged. A group
// that only partially forms (a join fails after the queue claim) leaves a waiting
// table that the game's waiting-lobby TTL sweeper reaps — no stake moves until the
// table actually starts, so nothing is stranded.
package groupmatch

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/prometheus/client_golang/prometheus"
)

// ErrNotQueued is returned by Status/Cancel when the agent has no queue entry.
var ErrNotQueued = httpx.NewError(http.StatusNotFound, "not_queued", "This agent is not in the group matchmaking queue.")

// ErrGameNotGrouped is returned by Enqueue for a game that has no N-player table
// creator wired (i.e. not Mafia/Monopoly) — those belong in the 2-player queue.
var ErrGameNotGrouped = httpx.NewError(http.StatusBadRequest, "game_not_grouped", "This game does not use N-player group matchmaking.")

// Status values for a queue entry.
const (
	StatusWaiting = "waiting"
	StatusMatched = "matched"
)

// Entry is one agent's place in the group queue (one row per agent).
type Entry struct {
	AgentPublicID string    `json:"agent"`
	OwnerPublicID string    `json:"-"`
	Game          string    `json:"game"`
	Bid           int64     `json:"bid"`
	Elo           int       `json:"elo"`
	Status        string    `json:"status"`
	MatchID       string    `json:"match_id,omitempty"`
	EnqueuedAt    time.Time `json:"enqueued_at"`
}

// Seat is one member of a matched group, handed to the table creator.
type Seat struct {
	AgentPublicID string
	OwnerPublicID string
}

// Repo persists the group queue.
type Repo interface {
	// Upsert inserts or replaces the caller's entry as waiting (resets enqueued_at).
	Upsert(ctx context.Context, e Entry) error
	// Get returns the caller's entry, or ErrNotQueued if absent.
	Get(ctx context.Context, agentPublicID string) (Entry, error)
	// Delete removes the caller's entry (idempotent).
	Delete(ctx context.Context, agentPublicID string) error
	// WaitingByGame returns up to limit waiting entries for a game, ordered by
	// (bid, enqueued_at) so the matcher can pool by bid, oldest first.
	WaitingByGame(ctx context.Context, game string, limit int) ([]Entry, error)
	// ClaimGroup atomically reserves ALL given agents' entries (waiting -> claimed)
	// iff every one is currently waiting, returning true only then. This is the
	// mutual-exclusion point that must succeed BEFORE any table is created/escrowed,
	// so a group is never double-created across ticks or matcher instances.
	ClaimGroup(ctx context.Context, agentPublicIDs []string) (bool, error)
	// ReleaseGroup returns a claimed group to waiting (used when table creation fails).
	ReleaseGroup(ctx context.Context, agentPublicIDs []string) error
	// MarkMatchedGroup flips a claimed group to matched with the match id.
	MarkMatchedGroup(ctx context.Context, agentPublicIDs []string, matchPublicID string) error
}

// TableCreator creates and starts a full staked table for one game from a matched
// group. Satisfied by an adapter over mafia.Service / monopoly.Service that drives
// their existing CreateTable + Join path (the Nth join auto-starts + escrows).
type TableCreator interface {
	// SeatTarget is how many distinct-owner agents this game's table needs to start
	// (Mafia: 12; Monopoly: a configured 2–8).
	SeatTarget() int
	// CreateStartedTable seats every group member and starts the staked table,
	// returning its public id. A partial failure leaves a waiting table for the TTL
	// sweeper; it must return an error so the matcher releases the claim.
	CreateStartedTable(ctx context.Context, seats []Seat, bid int64) (matchPublicID string, err error)
}

// RatingSource supplies an agent's per-game rating for band placement. Satisfied by
// rating.Service.Elo.
type RatingSource interface {
	Elo(ctx context.Context, agentPublicID, game string) (int, error)
}

// Eligibility, Affordability, and Liveness are the SAME gates the 2-player queue
// uses (certification/suspension, stake affordability, reachability). Declared here
// structurally so the existing cmd/server adapters satisfy both queues; all optional.
type Eligibility interface {
	RequireCertified(ctx context.Context, agentPublicID string) error
}
type Affordability interface {
	CheckJoin(ctx context.Context, agentPublicID string, bid int64) error
}
type Liveness interface {
	RequireReachable(ctx context.Context, agentPublicID string) error
}

// Config tunes the band-widening schedule and matcher cadence. Band semantics match
// the 2-player matcher; the group matcher just needs the same knobs.
type Config struct {
	BaseBand     int
	BandStep     int
	StepInterval time.Duration
	MaxBand      int
	Interval     time.Duration
}

func (c *Config) withDefaults() {
	if c.BaseBand <= 0 {
		c.BaseBand = 150
	}
	if c.BandStep <= 0 {
		c.BandStep = 150
	}
	if c.StepInterval <= 0 {
		c.StepInterval = 10 * time.Second
	}
	if c.MaxBand <= 0 {
		c.MaxBand = 100000
	}
	if c.Interval <= 0 {
		c.Interval = time.Second
	}
}

type clock interface{ Now() time.Time }

// Service is the agent-facing group queue API (enqueue / status / cancel). Pairing
// runs in the background Matcher.
type Service struct {
	repo     Repo
	creators map[string]TableCreator // game -> its table creator (also carries SeatTarget)
	rating   RatingSource
	clock    clock
	cfg      Config
	log      *slog.Logger
	m        *metrics
	elig     Eligibility
	afford   Affordability
	live     Liveness
}

// New builds the group matchmaking service. creators maps each grouped game to its
// table creator; a game absent here is rejected at Enqueue (belongs to another queue).
func New(repo Repo, creators map[string]TableCreator, rating RatingSource, clk clock, cfg Config, log *slog.Logger, reg *prometheus.Registry) *Service {
	cfg.withDefaults()
	if log == nil {
		log = slog.Default()
	}
	return &Service{repo: repo, creators: creators, rating: rating, clock: clk, cfg: cfg, log: log, m: newMetrics(reg)}
}

func (s *Service) SetEligibility(e Eligibility)     { s.elig = e }
func (s *Service) SetAffordability(a Affordability) { s.afford = a }
func (s *Service) SetLiveness(l Liveness)           { s.live = l }

// Enqueue places (or refreshes) the caller's request for an N-player staked table in
// game at bid. Runs the SAME fail-fast gates as the 2-player queue (certification,
// affordability, reachability) so a broke/uncertified/offline agent never pollutes a
// pool waiting for a table it could never join.
func (s *Service) Enqueue(ctx context.Context, agentPublicID, ownerPublicID, game string, bid int64) (Entry, error) {
	if bid <= 0 {
		return Entry{}, httpx.NewError(http.StatusBadRequest, "invalid_request", "bid must be > 0")
	}
	if _, ok := s.creators[game]; !ok {
		return Entry{}, ErrGameNotGrouped
	}
	if s.elig != nil {
		if err := s.elig.RequireCertified(ctx, agentPublicID); err != nil {
			return Entry{}, err
		}
	}
	if s.afford != nil {
		if err := s.afford.CheckJoin(ctx, agentPublicID, bid); err != nil {
			return Entry{}, err
		}
	}
	if s.live != nil {
		if err := s.live.RequireReachable(ctx, agentPublicID); err != nil {
			return Entry{}, err
		}
	}
	elo, err := s.rating.Elo(ctx, agentPublicID, game)
	if err != nil {
		return Entry{}, err
	}
	e := Entry{
		AgentPublicID: agentPublicID, OwnerPublicID: ownerPublicID,
		Game: game, Bid: bid, Elo: elo, Status: StatusWaiting,
	}
	if err := s.repo.Upsert(ctx, e); err != nil {
		return Entry{}, err
	}
	s.m.enqueued.Inc()
	return s.repo.Get(ctx, agentPublicID)
}

// Handles reports whether game uses this N-player group queue (i.e. has a table
// creator wired). Lets callers (e.g. the auto-play reconciler) route a game to the
// group queue vs the 2-player queue without hard-coding the game list.
func (s *Service) Handles(game string) bool {
	_, ok := s.creators[game]
	return ok
}

// Status returns the caller's current queue entry.
func (s *Service) Status(ctx context.Context, agentPublicID string) (Entry, error) {
	return s.repo.Get(ctx, agentPublicID)
}

// Cancel removes the caller from the queue (idempotent).
func (s *Service) Cancel(ctx context.Context, agentPublicID string) error {
	return s.repo.Delete(ctx, agentPublicID)
}

// ── metrics ──────────────────────────────────────────────────────────────────

type metrics struct {
	enqueued prometheus.Counter
	tables   prometheus.Counter
	depth    prometheus.Gauge
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		enqueued: prometheus.NewCounter(prometheus.CounterOpts{Name: "groupmatch_enqueued_total", Help: "Agents enqueued for N-player group matchmaking."}),
		tables:   prometheus.NewCounter(prometheus.CounterOpts{Name: "groupmatch_tables_total", Help: "Staked tables started by the group matchmaker."}),
		depth:    prometheus.NewGauge(prometheus.GaugeOpts{Name: "groupmatch_queue_depth", Help: "Agents currently waiting in the group matchmaking queue."}),
	}
	if reg != nil {
		reg.MustRegister(m.enqueued, m.tables, m.depth)
	}
	return m
}
