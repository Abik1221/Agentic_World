// Package match is the impure shell around the pure Goofspiel engine: lobby,
// pairing, the round loop, the 20s move window + timeouts, idempotent actions,
// and crash-safe persistence. Instances are stateless — the event log + snapshot
// in Postgres are authoritative, and a per-match Redis lock serializes mutations
// so any instance can act on any match (see docs/architecture/concurrency-scaling.md).
package match

import (
	"context"
	"time"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
)

// Status values for a match row.
const (
	StatusWaiting  = "waiting"
	StatusActive   = "active"
	StatusFinished = "finished"
	StatusAborted  = "aborted"
)

// Mode values for a match row. Competitive is the real, paid, ranked economy.
// Sandbox is a risk-free practice match against a platform house agent: NO coins
// are staked or settled, the spending limits are not enforced, and the result
// does not affect ratings. The two share the engine, event log, replay, and SSE
// surface — only the money/rating calls are gated on the mode.
const (
	ModeCompetitive = "competitive"
	ModeSandbox     = "sandbox"
)

// HouseSeat is the seat the platform house agent always occupies in a sandbox
// match (the developer's agent is seated at gs.SeatA).
const HouseSeat = gs.SeatB

// Player is one seat in a match.
type Player struct {
	AgentPublicID string
	OwnerPublicID string
	Seat          int
	FinalScore    int
	CoinsDelta    int64
}

// Match is the full persisted match aggregate (meta + engine state snapshot).
type Match struct {
	PublicID      string
	Game          string
	Status        string
	Mode          string // ModeCompetitive | ModeSandbox
	BotPolicy     string // house strategy for a sandbox match; "" for competitive
	Bid           int64
	RakePct       int
	TotalRounds   int
	EngineVersion string
	Commit        string
	FairnessMode  string
	Seed          []byte // secret until finished; never exposed by the API before then
	State         gs.State
	RoundDeadline *time.Time
	Players       []Player
	WinnerAgent   string // agent public id of the winner; "" for tie/none
	ReplayHash    string
}

func (m *Match) playerBySeat(seat int) *Player {
	for i := range m.Players {
		if m.Players[i].Seat == seat {
			return &m.Players[i]
		}
	}
	return nil
}

func (m *Match) playerByAgent(agentPublicID string) *Player {
	for i := range m.Players {
		if m.Players[i].AgentPublicID == agentPublicID {
			return &m.Players[i]
		}
	}
	return nil
}

// ── Collaborator ports (kept minimal; real implementations arrive in later
//    stages, no-op defaults let the match loop run end-to-end now) ─────────────

// Limits checks the seven server-enforced spending limits at join time.
// Real implementation: Stage 4 (wallet). No-op admits everyone.
type Limits interface {
	CheckJoin(ctx context.Context, agentPublicID string, bid int64) error
}

// Wallet escrows stakes and settles winnings. Real implementation: Stage 4.
type Wallet interface {
	// StakeMatch escrows BOTH seats' bids in a single atomic transaction, so a
	// partial failure can never orphan one player's coins in escrow.
	StakeMatch(ctx context.Context, matchPublicID, agentA, agentB string, bid int64) error
	// Settle pays the pool to winnerAgentPublicID minus rake; "" means a tie (split/refund).
	Settle(ctx context.Context, matchPublicID, winnerAgentPublicID string, pool int64, rakePct int) error
	// Refund returns both stakes for an aborted match (players read from state).
	Refund(ctx context.Context, matchPublicID string) error
	// RefundStakes compensates a failed activation: it returns the two just-staked
	// bids by explicit agent id (the joiner is not yet persisted). Idempotent.
	RefundStakes(ctx context.Context, matchPublicID, agentA, agentB string, bid int64) error
}

// Broadcaster fans match events out to spectators. Real implementation: Stage 6.
type Broadcaster interface {
	Broadcast(matchPublicID string, events []gs.Event)
}

// Notifier is a low-latency wake-up channel for waiting agents. When a match's
// state advances, Notify wakes any agent long-polling that match's state so it
// returns on the actual state change rather than a fixed timer. Subscribe returns
// a channel that receives once per wake-up plus a cancel to release it. Backed by
// Redis pub/sub (cross-instance); NoopNotifier degrades long-poll to timeout-only.
type Notifier interface {
	Notify(matchPublicID string)
	Subscribe(matchPublicID string) (events <-chan struct{}, cancel func())
}

// Verifier records action timing and gates eligibility (anti human-play).
// Satisfied by an adapter over verification.Service.
type Verifier interface {
	Record(ctx context.Context, agentPublicID string, matchPublicID *string, responseMs int)
	CheckEligible(ctx context.Context, agentPublicID string) error
}

// Rater updates skill ratings (ELO) when a match finalizes. Real implementation:
// Stage 7 (rating). Called once per match after settlement; the implementation is
// idempotent on the match id so a finalize retry never double-counts.
type Rater interface {
	Rate(ctx context.Context, r RatingResult) error
}

// RatingResult is the finalized outcome handed to the rater: both seats plus the
// winning seat (or gs.Tie).
type RatingResult struct {
	MatchPublicID string
	Game          string
	WinnerSeat    int
	Players       []RatingPlayer
}

// RatingPlayer is one seat's contribution to the rating update. Placement is the
// finishing rank (1 = best); 0 means "derive from WinnerSeat" (the 2-player case).
type RatingPlayer struct {
	AgentPublicID string
	Seat          int
	Placement     int
	CoinsDelta    int64
}

// Bot picks the house agent's card in a sandbox match. Real implementation:
// internal/bot. It is given the full (open-information) engine state, the seat to
// play, and the match's bot policy, and MUST return a card in that seat's hand.
type Bot interface {
	Pick(state gs.State, seat int, policy string) int
}

// FinishHook fires once a match is fully finalized (settled + rated). It MUST be
// non-blocking — the implementation enqueues async work (clips, notifications)
// and returns immediately, never delaying the match loop. Real impl: Stage 8.
type FinishHook interface {
	MatchFinished(ctx context.Context, matchPublicID string)
}

// ── No-op defaults ───────────────────────────────────────────────────────────

type NoopLimits struct{}

func (NoopLimits) CheckJoin(context.Context, string, int64) error { return nil }

type NoopWallet struct{}

func (NoopWallet) StakeMatch(context.Context, string, string, string, int64) error { return nil }
func (NoopWallet) Settle(context.Context, string, string, int64, int) error        { return nil }
func (NoopWallet) Refund(context.Context, string) error                            { return nil }
func (NoopWallet) RefundStakes(context.Context, string, string, string, int64) error {
	return nil
}

type NoopBroadcaster struct{}

func (NoopBroadcaster) Broadcast(string, []gs.Event) {}

// NoopNotifier never wakes a waiter; long-poll falls back to its timeout. Used in
// tests and whenever no real notifier is wired.
type NoopNotifier struct{}

func (NoopNotifier) Notify(string) {}
func (NoopNotifier) Subscribe(string) (<-chan struct{}, func()) {
	return make(chan struct{}), func() {}
}

type AllowAllVerifier struct{}

func (AllowAllVerifier) Record(context.Context, string, *string, int) {}
func (AllowAllVerifier) CheckEligible(context.Context, string) error  { return nil }

type NoopRater struct{}

func (NoopRater) Rate(context.Context, RatingResult) error { return nil }

// NoopBot plays the lowest legal card. It is the safe default when no real bot is
// wired; sandbox matches require a real Bot (set via SetBot), so this only ever
// runs in tests / misconfiguration, and still returns a legal move.
type NoopBot struct{}

func (NoopBot) Pick(state gs.State, seat int, _ string) int {
	hand := state.Hands[seat]
	low := hand[0]
	for _, c := range hand[1:] {
		if c < low {
			low = c
		}
	}
	return low
}

type NoopFinishHook struct{}

func (NoopFinishHook) MatchFinished(context.Context, string) {}
