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
	"fmt"
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

// PoolStats describes the (game, bid) pool one agent is waiting in. It exists for
// queue visibility: the wait itself is acceptable, but silence during it is not, and
// before this an agent polling GET /v1/group-queue learned only "waiting" — no
// position, no idea whether anybody else was even queued.
type PoolStats struct {
	// Position is the agent's 1-based place in its pool by enqueue time, or 0 when the
	// agent is not currently waiting (already claimed/matched, or gone).
	Position int
	// Waiting is how many agents are waiting in this pool.
	Waiting int
	// DistinctOwners is how many DIFFERENT owners those agents belong to — the real
	// ceiling on a single table, since two agents of one owner may never be seated
	// together. Twelve queued agents from two owners can never fill a Mafia table, and
	// reporting only `Waiting` would make that look imminent.
	DistinctOwners int
}

// QueueStatus is the agent-facing view of a queue entry: the entry itself plus enough
// of its pool to explain what is happening. Entry is embedded, so the JSON keeps every
// field the endpoint already returned and adds to it.
type QueueStatus struct {
	Entry
	Position       int   `json:"position"`        // 1-based place in the pool; 0 once matched
	PoolSize       int   `json:"pool_size"`       // agents waiting at this game+bid
	DistinctOwners int   `json:"distinct_owners"` // how many owners they represent
	SeatsNeeded    int   `json:"seats_needed"`    // full roster for this game
	MinSeats       int   `json:"min_seats"`       // fewest real agents this game will start with
	WaitedMs       int64 `json:"waited_ms"`
	// ShortFormInMs is how long until the matcher may start this pool below a full
	// roster. 0 means that point has passed (so the only thing still missing is
	// MinSeats' worth of distinct owners); it is omitted for a game that always needs a
	// full roster.
	ShortFormInMs int64 `json:"short_form_in_ms,omitempty"`
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
	// PoolStats reports the size, owner diversity, and the agent's place in one
	// (game, bid) waiting pool. Read-only; used by Status for queue visibility.
	PoolStats(ctx context.Context, game string, bid int64, agentPublicID string) (PoolStats, error)
}

// TableCreator creates and starts a full staked table for one game from a matched
// group. Satisfied by an adapter over mafia.Service / monopoly.Service that drives
// their existing CreateTable + Join path (the Nth join auto-starts + escrows).
type TableCreator interface {
	// SeatTarget is how many distinct-owner agents this game's table needs to start
	// (Mafia: 12; Monopoly: a configured 2–8).
	SeatTarget() int
	// MinSeats is the fewest REAL queued agents this game will start a table with once
	// Config.ShortFormAfter has elapsed, so a thin queue is not an indefinite wait.
	// Returning SeatTarget() opts out and keeps full-roster-only behaviour.
	//
	// How the remaining chairs are handled is the GAME's business, not the matcher's:
	// Monopoly is natively playable at 2–8 so it just sizes the table to the group,
	// while Mafia's engine deals from a fixed 12-role pool and so fills the gap with
	// house bots inside CreateStartedTable. The matcher only decides how few real
	// agents is acceptable.
	MinSeats() int
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
	// ShortFormAfter is how long the OLDEST waiter in a pool must have waited before
	// the matcher will form a table below SeatTarget (down to the game's MinSeats).
	//
	// This is the fix for the launch-day failure mode: band widening alone can never
	// start a Mafia table, because widening makes waiters more compatible with each
	// other without ever producing a 12th one. Four agents queued for a twelve-seat
	// table waited forever, which is indistinguishable from a dead platform.
	//
	// Anchored on the anchor's wait (not the pool's), so a newcomer who joins a thin
	// pool does not reset anybody's clock.
	ShortFormAfter time.Duration
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
	if c.ShortFormAfter <= 0 {
		c.ShortFormAfter = 90 * time.Second
	}
}

type clock interface{ Now() time.Time }

// Service is the agent-facing group queue API (enqueue / status / cancel). Pairing
// runs in the background Matcher.
// StakeFloor validates a coin amount against the game's enabled tiers. See the identical port
// in matchmaking for why this is enforced on the service rather than the handler.
type StakeFloor interface {
	ValidStake(ctx context.Context, game string, coins int64) (ok bool, lowest int64, err error)
}

type Service struct {
	stakes StakeFloor

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
	// Same enforcement as the ranked queue: the stake must be a tier the game offers. Placed on
	// the service because autoplay enqueues directly and never passes through the handler.
	if s.stakes != nil && bid > 0 {
		ok, lowest, err := s.stakes.ValidStake(ctx, game, bid)
		if err != nil {
			return Entry{}, httpx.NewError(http.StatusServiceUnavailable, "stakes_unavailable",
				"Stake tiers could not be read, so the queue cannot verify your stake. Try again shortly.")
		}
		if !ok {
			return Entry{}, httpx.NewError(http.StatusBadRequest, "stake_not_offered",
				fmt.Sprintf("A stake of %d coins is not offered for %s. The lowest available stake is %d coins.", bid, game, lowest))
		}
	}
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

// Status returns the caller's current queue entry together with its position in the
// pool and when a short-handed table becomes possible.
//
// Pool figures are best-effort: a failure to read them degrades to the bare entry
// rather than failing the poll, because an agent waiting on a match must always be able
// to learn that it is still queued.
func (s *Service) Status(ctx context.Context, agentPublicID string) (QueueStatus, error) {
	e, err := s.repo.Get(ctx, agentPublicID)
	if err != nil {
		return QueueStatus{}, err
	}
	waited := s.clock.Now().Sub(e.EnqueuedAt)
	if waited < 0 {
		waited = 0 // a clock skew must not report a negative wait
	}
	out := QueueStatus{Entry: e, WaitedMs: waited.Milliseconds()}
	if c, ok := s.creators[e.Game]; ok {
		out.SeatsNeeded, out.MinSeats = seatBounds(c)
		if out.MinSeats < out.SeatsNeeded {
			remaining := s.cfg.ShortFormAfter - waited
			if remaining < 0 {
				remaining = 0
			}
			out.ShortFormInMs = remaining.Milliseconds()
		}
	}
	if e.Status != StatusWaiting {
		return out, nil // matched/claimed: it is no longer in a pool
	}
	ps, err := s.repo.PoolStats(ctx, e.Game, e.Bid, agentPublicID)
	if err != nil {
		s.log.Warn("groupmatch pool stats failed", "agent", agentPublicID, "game", e.Game, "error", err)
		return out, nil
	}
	out.Position, out.PoolSize, out.DistinctOwners = ps.Position, ps.Waiting, ps.DistinctOwners
	return out, nil
}

// seatBounds returns a game's full seat target and the minimum the matcher will actually
// honour, clamped into [2, target] so a creator returning something nonsensical falls
// back to full-roster-only. Shared by the matcher and Status: reporting a minimum the
// matcher would ignore is how a queue starts lying about when it will seat you.
func seatBounds(c TableCreator) (target, minSeats int) {
	target = c.SeatTarget()
	minSeats = c.MinSeats()
	if minSeats < 2 || minSeats > target {
		minSeats = target
	}
	return target, minSeats
}

// Cancel removes the caller from the queue (idempotent).
func (s *Service) Cancel(ctx context.Context, agentPublicID string) error {
	return s.repo.Delete(ctx, agentPublicID)
}

// ── metrics ──────────────────────────────────────────────────────────────────

type metrics struct {
	enqueued    prometheus.Counter
	tables      prometheus.Counter
	shortTables prometheus.Counter
	depth       prometheus.Gauge
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		enqueued: prometheus.NewCounter(prometheus.CounterOpts{Name: "groupmatch_enqueued_total", Help: "Agents enqueued for N-player group matchmaking."}),
		tables:   prometheus.NewCounter(prometheus.CounterOpts{Name: "groupmatch_tables_total", Help: "Staked tables started by the group matchmaker."}),
		// The ratio of this to groupmatch_tables_total is the health signal for a thin
		// queue: if most tables are starting short-handed, the pool is too small for the
		// seat target and it is a product problem, not a matchmaking one.
		shortTables: prometheus.NewCounter(prometheus.CounterOpts{Name: "groupmatch_short_tables_total", Help: "Tables started below the game's full seat target (thin queue fallback)."}),
		depth:       prometheus.NewGauge(prometheus.GaugeOpts{Name: "groupmatch_queue_depth", Help: "Agents currently waiting in the group matchmaking queue."}),
	}
	if reg != nil {
		reg.MustRegister(m.enqueued, m.tables, m.shortTables, m.depth)
	}
	return m
}

// SetStakeFloor wires tier enforcement into the group queue itself.
func (s *Service) SetStakeFloor(f StakeFloor) { s.stakes = f }
