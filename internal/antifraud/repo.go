package antifraud

import (
	"context"
	"time"
)

// Repo is the anti-fraud persistence port.
type Repo interface {
	// ── payout gate ──
	MatchAgents(ctx context.Context, matchPublicID string) ([]AgentRef, error)
	AnyFlagged(ctx context.Context, agentPublicIDs []string) (bool, error)
	RecordFlag(ctx context.Context, agentPublicID, matchPublicID, typ, detail string) error
	RecordHold(ctx context.Context, matchPublicID, reason string) (newlyHeld bool, err error)
	ResolveHold(ctx context.Context, matchPublicID, status string) error

	// ── disputes ──
	OpenDispute(ctx context.Context, in DisputeInput) (publicID string, err error)
	// ResolveDispute transitions an open/reviewing dispute to status; changed is
	// false (idempotent) if it was already terminal. Returns the linked match.
	ResolveDispute(ctx context.Context, disputePublicID, status, resolution string) (matchPublicID string, changed bool, err error)

	// ── detection ──
	RecentPairs(ctx context.Context, since time.Time, minGames int) ([]Pair, error)
	AgentsWithSamples(ctx context.Context, min int) ([]string, error)
	AgentTiming(ctx context.Context, agentPublicID string) (TimingStat, error)

	// ── audit (append-only) ──
	Audit(ctx context.Context, actor, action, target string, detail []byte) error
}

// AgentRef pairs an agent with its owner (for same-owner detection).
type AgentRef struct {
	AgentPublicID string
	OwnerPublicID string
}

// DisputeInput is a filed dispute.
type DisputeInput struct {
	PublicID       string
	MatchPublicID  string // optional
	AgentPublicID  string // optional
	ReporterUserID string
	Kind           string
	Detail         string
}

// Settler releases or reverses a held payout. Satisfied by wallet.Service.
type Settler interface {
	SettleHeld(ctx context.Context, matchPublicID string) error
	Refund(ctx context.Context, matchPublicID string) error
}
