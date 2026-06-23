// Package wallet is the money-safety layer over the ledger. It implements the
// match collaborator ports stubbed in Stage 3 — Wallet (stake/settle/refund) and
// Limits (the seven server-enforced spending checks at join) — and serves the
// read-only `/v1/wallet` surface. It never touches the DB directly: coin moves go
// through ledger.Service; stats come from its Repo. See Stage 4 docs.
package wallet

import (
	"context"
	"time"

	"github.com/agent-arena/arena/internal/ledger"
)

// ledgerPort is the slice of ledger.Service this package needs (kept narrow so
// the service is trivially fakeable in tests). *ledger.Service satisfies it.
type ledgerPort interface {
	Post(ctx context.Context, t ledger.Txn) (ledger.ApplyResult, error)
	Balance(ctx context.Context, agentPublicID string) (int64, error)
	History(ctx context.Context, agentPublicID string, limit int) ([]ledger.Line, error)
}

// Repo supplies the read-side facts the wallet/limit logic needs. All queries
// are over already-persisted match/agent state; this port performs no writes.
type Repo interface {
	// Settlement returns the staked agents and the per-seat bid for a match,
	// used to build settle/refund transactions and tie splits.
	Settlement(ctx context.Context, matchPublicID string) (Settlement, error)
	// AgentLimits returns an agent's eight configured spending limits.
	AgentLimits(ctx context.Context, agentPublicID string) (AgentLimits, error)
	// LossSince returns the total coins lost (a non-negative number) in finished
	// matches that finished at or after `since`.
	LossSince(ctx context.Context, agentPublicID string, since time.Time) (int64, error)
	// LossCountSince returns the number of matches lost at or after `since`.
	LossCountSince(ctx context.Context, agentPublicID string, since time.Time) (int, error)
	// ActiveMatchCount returns how many active matches the agent is currently in.
	ActiveMatchCount(ctx context.Context, agentPublicID string) (int, error)
	// OwnerOf returns the owner user's public id for an agent (read authorization).
	OwnerOf(ctx context.Context, agentPublicID string) (string, error)

	// ── chargeback debt (industry-standard negative-balance handling) ──
	// OutstandingDebt is what the agent still owes from un-recovered chargebacks.
	OutstandingDebt(ctx context.Context, agentPublicID string) (int64, error)
	// RecordDebt adds to the agent's outstanding debt (a chargeback shortfall).
	RecordDebt(ctx context.Context, agentPublicID string, coins int64) error
	// RepayDebt reduces outstanding debt by up to coins.
	RepayDebt(ctx context.Context, agentPublicID string, coins int64) error
}

// Settlement is the staking shape of a match (used for tie splits, refunds, and
// releasing a held payout — hence the persisted winner + rake).
type Settlement struct {
	Bid     int64
	Agents  []string // both players' agent public ids
	Winner  string   // winner agent public id ("" for a tie); set once finished
	RakePct int
}

// AgentLimits mirrors the eight server-enforced limit columns on an agent.
type AgentLimits struct {
	CoinLimitPerMatch    int64
	DailyLossLimit       int64
	SessionLossLimit     int64
	MinWalletBalance     int64
	MaxConcurrentMatches int
	CooldownLosses       int
	CooldownSeconds      int
	MaxBid               int64
}

// Config tunes the limit engine.
type Config struct {
	// SessionWindow is the trailing window that defines a "session" for the
	// session-loss limit (the MVP interpretation; no login state required).
	SessionWindow time.Duration
}
