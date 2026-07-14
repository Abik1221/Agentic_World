// Package matchmaking turns the old "grab matches[0] from a FIFO lobby" free-for-all
// into a server-driven, skill-banded queue. An agent asks for an opponent at a bid;
// a background matcher (matcher.go) pairs two queued agents whose ratings are within
// a band that widens the longer they wait, never pairs same-owner agents, and seats
// them in an already-active match. This makes the rating system load-bearing and
// removes the deterministic-rendezvous collusion vector of an open lobby.
package matchmaking

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/prometheus/client_golang/prometheus"
)

// ErrNotQueued is returned by Status/Cancel when the agent has no queue entry.
var ErrNotQueued = httpx.NewError(http.StatusNotFound, "not_queued", "This agent is not in the matchmaking queue.")

// Status values for a queue entry.
const (
	StatusWaiting = "waiting"
	StatusMatched = "matched"
)

// Entry is one agent's place in the queue (one row per agent).
type Entry struct {
	AgentPublicID string    `json:"agent"`
	OwnerPublicID string    `json:"-"`
	Bid           int64     `json:"bid"`
	Elo           int       `json:"elo"`
	Status        string    `json:"status"`
	MatchID       string    `json:"match_id,omitempty"`
	EnqueuedAt    time.Time `json:"enqueued_at"`
}

// Repo persists the queue.
type Repo interface {
	// Upsert inserts or replaces the caller's entry as waiting (resets enqueued_at).
	Upsert(ctx context.Context, e Entry) error
	// Get returns the caller's entry, or ErrNotQueued if absent.
	Get(ctx context.Context, agentPublicID string) (Entry, error)
	// Delete removes the caller's entry (idempotent).
	Delete(ctx context.Context, agentPublicID string) error
	// Waiting returns up to limit waiting entries ordered by (bid, enqueued_at).
	Waiting(ctx context.Context, limit int) ([]Entry, error)
	// ClaimPair atomically reserves both agents' entries (waiting -> claimed) iff
	// BOTH are currently waiting, returning true only then. This is the
	// mutual-exclusion point that must succeed BEFORE any stake is escrowed, so a
	// pair is never double-escrowed across ticks or matcher instances.
	ClaimPair(ctx context.Context, agentA, agentB string) (bool, error)
	// ReleasePair returns a claimed pair to waiting (used when escrow fails).
	ReleasePair(ctx context.Context, agentA, agentB string) error
	// MarkMatched flips both agents' claimed entries to matched with the match id.
	MarkMatched(ctx context.Context, agentA, agentB, matchPublicID string) error
}

// Pairer creates the actual match once two agents are paired. Satisfied by an
// adapter over match.Service.CreatePaired.
type Pairer interface {
	CreatePaired(ctx context.Context, aAgent, aOwner, bAgent, bOwner string, bid int64) (matchPublicID string, err error)
}

// RatingSource supplies an agent's current rating for band placement. Satisfied
// by rating.Service.Elo.
type RatingSource interface {
	Elo(ctx context.Context, agentPublicID string) (int, error)
}

// Config tunes the band-widening schedule and matcher cadence.
type Config struct {
	BaseBand     int           // initial ELO band half-width (default 150)
	BandStep     int           // widen by this each StepInterval (default 150)
	StepInterval time.Duration // how often the band widens (default 10s)
	MaxBand      int           // cap; large => "anyone" after enough wait (default 100000)
	Interval     time.Duration // matcher loop cadence (default 1s)
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

// Service is the agent-facing queue API (enqueue / status / cancel). The pairing
// itself runs in the background Matcher.
type Service struct {
	repo   Repo
	pairer Pairer
	rating RatingSource
	clock  clock
	cfg    Config
	log    *slog.Logger
	m      *metrics
	// elig gates ranked entry (certification); afford preflights stake
	// affordability. Both optional (nil = skip), injected via their setters.
	elig   Eligibility
	afford Affordability
}

// Eligibility gates who may enter the ranked queue — e.g. the certification gate
// (agent must have an active, endpoint-verified manifest). Satisfied by
// manifest.Service. Injected via SetEligibility so New stays unchanged.
type Eligibility interface {
	RequireCertified(ctx context.Context, agentPublicID string) error
}

// Affordability preflights the stake against the agent's balance + owner limits,
// using the SAME check escrow runs at pairing (wallet.CheckJoin). Enforcing it at
// enqueue makes a broke or over-limit agent fail fast with a specific error
// instead of sitting in `waiting` forever for a match that could never escrow.
// Satisfied by wallet.Service. Injected via SetAffordability so New stays unchanged.
type Affordability interface {
	CheckJoin(ctx context.Context, agentPublicID string, bid int64) error
}

// SetEligibility installs the ranked-entry gate (call once during wiring).
func (s *Service) SetEligibility(e Eligibility) { s.elig = e }

// SetAffordability installs the stake-affordability preflight (call once during wiring).
func (s *Service) SetAffordability(a Affordability) { s.afford = a }

// clock is the minimal time port (matches platform.Clock structurally).
type clock interface{ Now() time.Time }

// New builds the matchmaking service.
func New(repo Repo, pairer Pairer, rating RatingSource, clk clock, cfg Config, log *slog.Logger, reg *prometheus.Registry) *Service {
	cfg.withDefaults()
	return &Service{repo: repo, pairer: pairer, rating: rating, clock: clk, cfg: cfg, log: log, m: newMetrics(reg)}
}

// Enqueue places (or refreshes) the caller's request for an opponent at bid. It
// snapshots the agent's current rating so the matcher can band without extra reads.
func (s *Service) Enqueue(ctx context.Context, agentPublicID, ownerPublicID string, bid int64) (Entry, error) {
	if bid <= 0 {
		return Entry{}, httpx.NewError(http.StatusBadRequest, "invalid_request", "bid must be > 0")
	}
	// Certification gate: only verified agents enter the ranked queue (fail fast so
	// uncertified agents never pollute pairing).
	if s.elig != nil {
		if err := s.elig.RequireCertified(ctx, agentPublicID); err != nil {
			return Entry{}, err
		}
	}
	// Affordability preflight: reject a stake the agent can't cover (balance +
	// reserve) or that breaches an owner limit (per-match / max-bid / loss /
	// cooldown / concurrency), with the SAME error escrow would raise at pairing.
	// Without this the agent would enqueue and wait forever for a match that can
	// never escrow (the old "broke agent stuck waiting" foot-gun).
	if s.afford != nil {
		if err := s.afford.CheckJoin(ctx, agentPublicID, bid); err != nil {
			return Entry{}, err
		}
	}
	elo, err := s.rating.Elo(ctx, agentPublicID)
	if err != nil {
		return Entry{}, err
	}
	e := Entry{
		AgentPublicID: agentPublicID, OwnerPublicID: ownerPublicID,
		Bid: bid, Elo: elo, Status: StatusWaiting,
	}
	if err := s.repo.Upsert(ctx, e); err != nil {
		return Entry{}, err
	}
	s.m.enqueued.Inc()
	return s.repo.Get(ctx, agentPublicID)
}

// Status returns the caller's current queue entry (waiting, or matched with the
// match id once the matcher has paired them).
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
	paired   prometheus.Counter
	depth    prometheus.Gauge
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		enqueued: prometheus.NewCounter(prometheus.CounterOpts{Name: "matchmaking_enqueued_total", Help: "Agents enqueued for matchmaking."}),
		paired:   prometheus.NewCounter(prometheus.CounterOpts{Name: "matchmaking_paired_total", Help: "Matches created by the matchmaker."}),
		depth:    prometheus.NewGauge(prometheus.GaugeOpts{Name: "matchmaking_queue_depth", Help: "Agents currently waiting in the matchmaking queue."}),
	}
	reg.MustRegister(m.enqueued, m.paired, m.depth)
	return m
}
