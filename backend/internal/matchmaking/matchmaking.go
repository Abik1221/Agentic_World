// Package matchmaking turns the old "grab matches[0] from a FIFO lobby" free-for-all
// into a server-driven, skill-banded queue. An agent asks for an opponent at a bid;
// a background matcher (matcher.go) pairs two queued agents whose ratings are within
// a band that widens the longer they wait, never pairs same-owner agents, and seats
// them in an already-active match. This makes the rating system load-bearing and
// removes the deterministic-rendezvous collusion vector of an open lobby.
package matchmaking

import (
	"sync"

	"context"
	"fmt"
	"github.com/agent-arena/arena/internal/antifraud"
	"log/slog"
	"net/http"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/prometheus/client_golang/prometheus"
)

// ErrNotQueued is returned by Status/Cancel when the agent has no queue entry.
var ErrNotQueued = httpx.NewError(http.StatusNotFound, "not_queued", "This agent is not in the matchmaking queue.")

// ErrAgentOffline is returned by Enqueue when the reachability gate is wired and the
// agent is neither holding a live connection nor exposing a verified, resolvable
// endpoint — so a match would only forfeit-and-bleed its stake. Callers surface it as
// "connect your agent first"; the auto-play reconciler simply skips and retries the
// next tick, so the agent resumes automatically once it reconnects.
var ErrAgentOffline = httpx.NewError(http.StatusConflict, "agent_offline", "This agent is not currently reachable (no live connection and no verified endpoint). Connect it (pyyol run) or fix its endpoint before entering ranked.")

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
	// affordability; live rejects an unreachable agent. All optional (nil = skip),
	// injected via their setters.
	elig   Eligibility
	afford Affordability
	// links resolves same-beneficiary account groups. Optional: nil falls back to a bare
	// owner-id comparison, which is the pre-existing behaviour — a platform that has not
	// wired the lookup must still match players, just with the weaker rule.
	links   BeneficiaryLinks
	linkMu  sync.RWMutex
	linkIdx *antifraud.LinkIndex
	live    Liveness
	// stakes rejects a bid that is not an enabled tier for the game. Optional (nil = skip)
	// only so a deployment with no tier table configured still works; once tiers exist it is
	// the authority.
	stakes StakeFloor
}

// StakeFloor validates that a coin amount is a stake the game actually offers.
//
// This lives on the SERVICE, not only on the HTTP handler, and that placement is the whole
// point. The handler already resolved tiers correctly — but autoplay and the pairing driver
// call Enqueue directly, so their bids never met that check. The result was 870 live matches
// staked at 50 and 100 coins against a configured floor of 500, starting two seconds after
// the tiers were seeded and still going.
//
// A guard beside one caller is one new caller away from being bypassed. This one cannot be,
// because nothing enters the queue without passing through here.
type StakeFloor interface {
	// ValidStake reports whether coins matches an enabled tier for game, and the lowest
	// enabled tier so the error can say what to use instead.
	ValidStake(ctx context.Context, game string, coins int64) (ok bool, lowest int64, err error)
}

// SetStakeFloor wires tier enforcement into the queue itself.
func (s *Service) SetStakeFloor(f StakeFloor) { s.stakes = f }

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

// Liveness rejects entry for an agent that isn't reachable RIGHT NOW — one that
// holds neither a live connection nor a verified, resolvable hosted endpoint.
// Without it, an offline-but-certified agent (crashed worker, dead endpoint) is
// enqueued and matched, then forfeits every move via the sweeper's ForceTimeout and
// loses its escrowed stake each match — silently, and repeatedly under auto-play.
// Enforcing reachability here makes it fail fast (manual) or be skipped-and-retried
// (auto-play) instead of bleeding coins. Satisfied by an adapter over the agent
// gateway (live socket) + manifest resolver (verified endpoint). Injected via
// SetLiveness so New stays unchanged; nil = skip (backward compatible).
type Liveness interface {
	RequireReachable(ctx context.Context, agentPublicID string) error
}

// SetEligibility installs the ranked-entry gate (call once during wiring).
func (s *Service) SetEligibility(e Eligibility) { s.elig = e }

// SetAffordability installs the stake-affordability preflight (call once during wiring).
func (s *Service) SetAffordability(a Affordability) { s.afford = a }

// SetLiveness installs the reachability gate (call once during wiring).
func (s *Service) SetLiveness(l Liveness) { s.live = l }

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
	// The stake must be one the game actually offers. Checked here rather than only in the
	// handler because autoplay and the pairing driver enqueue directly; see StakeFloor.
	if s.stakes != nil {
		ok, lowest, err := s.stakes.ValidStake(ctx, "goofspiel", bid)
		if err != nil {
			// Fail CLOSED. An unreadable tier table is not permission to stake an arbitrary
			// amount — that is the failure mode that let sub-floor matches run for two days.
			return Entry{}, httpx.NewError(http.StatusServiceUnavailable, "stakes_unavailable",
				"Stake tiers could not be read, so the queue cannot verify your stake. Try again shortly.")
		}
		if !ok {
			return Entry{}, httpx.NewError(http.StatusBadRequest, "stake_not_offered",
				fmt.Sprintf("A stake of %d coins is not offered for goofspiel. The lowest available stake is %d coins.", bid, lowest))
		}
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
	// Reachability gate: refuse to queue an agent that can't actually play right now
	// (no live connection, no verified endpoint). Without this an offline agent gets
	// matched and forfeits every move, bleeding its stake each match — the whole point
	// of ranked auto-play going wrong. Fails fast for a manual caller; the auto-play
	// reconciler swallows this and retries next tick, so the agent resumes on reconnect.
	if s.live != nil {
		if err := s.live.RequireReachable(ctx, agentPublicID); err != nil {
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

// OrphanSweeper is the reconciler side of queue cleanup. Satisfied by *store.MatchmakingRepo.
type OrphanSweeper interface {
	SweepOrphanedEntries(ctx context.Context) (int64, error)
}

// SweepWorker deletes queue entries left pointing at terminal matches.
//
// The primary cleanup is in match.finalize; this exists because that hook cannot win a race
// against the pairing transaction it is trying to undo. An entry orphaned this way is not
// cosmetic: autoplay counts 'matched' as still-queued, so the agent stops re-entering entirely,
// and the row is unpairable, so it crowds the queue and starves newcomers.
type SweepWorker struct {
	sweeper  OrphanSweeper
	interval time.Duration
	log      *slog.Logger
}

func NewSweepWorker(s OrphanSweeper, interval time.Duration, log *slog.Logger) *SweepWorker {
	if log == nil {
		log = slog.Default()
	}
	return &SweepWorker{sweeper: s, interval: interval, log: log}
}

// Run sweeps until ctx is cancelled, once immediately on start.
func (w *SweepWorker) Run(ctx context.Context) {
	w.log.Info("matchmaking orphan sweeper started", "interval", w.interval.String())
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		n, err := w.sweeper.SweepOrphanedEntries(ctx)
		switch {
		case err != nil:
			w.log.Error("matchmaking orphan sweep failed", "error", err)
		case n > 0:
			// Logged whenever it fires. A steady trickle means the finalize hook is losing the
			// race more often than expected, and that is worth seeing rather than silently
			// papering over on a timer.
			w.log.Warn("swept ranked-queue entries left on terminal matches",
				"deleted", n, "note", "primary cleanup is match.finalize; a nonzero count here "+
					"means it lost a race with the pairing commit")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// BeneficiaryLinks discovers account groups that share a payout identity.
type BeneficiaryLinks interface {
	BeneficiaryLinks(ctx context.Context) ([]antifraud.LinkedGroup, error)
}

// SetBeneficiaryLinks installs same-beneficiary detection. Nil keeps the owner-id-only rule.
func (s *Service) SetBeneficiaryLinks(b BeneficiaryLinks) {
	if b != nil {
		s.links = b
	}
}

// RefreshLinks rebuilds the beneficiary index.
//
// Rebuilt on a schedule and cached, NOT queried per candidate pair: pairing sits on the hot
// path of every queued match, and a database round trip inside that loop would make
// matchmaking latency a function of how many agents are waiting.
//
// A refresh failure keeps the PREVIOUS index rather than clearing it. Dropping to "nobody is
// linked" on a transient error is the wrong direction to fail — it would quietly re-open the
// multi-account hole at exactly the moment the database is unhappy.
func (s *Service) RefreshLinks(ctx context.Context) error {
	if s.links == nil {
		return nil
	}
	groups, err := s.links.BeneficiaryLinks(ctx)
	if err != nil {
		return err
	}
	idx := antifraud.NewLinkIndex(groups)
	s.linkMu.Lock()
	s.linkIdx = idx
	s.linkMu.Unlock()
	s.log.Info("matchmaking: beneficiary index refreshed",
		"groups", len(groups), "linked_owners", idx.Size())
	return nil
}

// linked reports whether two owners are the same beneficiary.
//
// Always true for an identical owner id, index or no index, so the old rule can never be lost
// by a missing refresh.
func (s *Service) linked(a, b string) bool {
	if a != "" && a == b {
		return true
	}
	s.linkMu.RLock()
	idx := s.linkIdx
	s.linkMu.RUnlock()
	return idx.Linked(a, b)
}
