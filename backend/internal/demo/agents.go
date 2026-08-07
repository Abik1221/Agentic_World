package demo

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/agent-arena/arena/internal/identity"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/wallet"
)

// Agent is a dev-only filler bot (rule-based, no LLM). Each has a distinct owner
// so same-owner pairing rules allow full tables.
type Agent struct {
	PublicID      string
	OwnerPublicID string
	Name          string
}

const poolSize = 14 // 12 Mafia seats + 2 Goofspiel players with headroom

// EnsureAgents creates or loads the dev agent pool. Idempotent on agent slug.
func EnsureAgents(ctx context.Context, repo identity.Repo, mint *wallet.Service, log *slog.Logger) ([]Agent, error) {
	if s, ok := repo.(interface {
		EnsureDevAgents(context.Context, int, *wallet.Service, *slog.Logger) ([]Agent, error)
	}); ok {
		return s.EnsureDevAgents(ctx, poolSize, mint, log)
	}
	return nil, fmt.Errorf("demo: identity repo does not support dev seeding")
}

// DevAgentName returns the display name for pool slot i (matches UI roster style).
func DevAgentName(i int) string {
	names := []string{
		"ATLAS_PRIME", "ORACLE_v9", "VOID_STALKER", "SHIVA_ZERO",
		"AEON_FLUX", "GHOST_PIXEL", "NEO_RECORDS", "T_CHIP",
		"HEX_WARDEN", "NULL_SECTOR", "KARMA_NODE", "PRISM_ECHO",
		"SPARK_NODE", "FLUX_CORE",
	}
	if i < len(names) {
		return names[i]
	}
	return fmt.Sprintf("DEMO_%d", i+1)
}

// Unused but documents intent — agents are code-driven, not LLM-backed.
const FrameworkLabel = "rules-engine"

// MintCoins funds an agent wallet for dev play, once.
//
// The key is fixed on purpose: seeding must not re-mint on every restart. Use TopUp for the
// recurring case.
func MintCoins(ctx context.Context, mint *wallet.Service, agentPublicID string, amount int64) error {
	key := "demo:mint:" + agentPublicID
	return mint.Mint(ctx, agentPublicID, amount, key)
}

// HouseFloat is what a house bot is topped back up to. Twenty minimum-stake matches, so a bot
// can lose a run of tables without dropping below the entry it needs to seat the next one.
const HouseFloat int64 = 10_000

// TopUp restores a house bot to HouseFloat when it has fallen below one stake plus its reserve.
//
// # Why house bots need this at all
//
// They play each other at STAKED tables, and the platform takes a rake from every pot. A closed
// population paying a percentage to the house on every match is a strictly shrinking pool: the
// bots do not go broke because of bad play, they go broke by construction. Seeding minted 10,000
// once, with a fixed idempotency key that could never fire again, so the drain was one-way.
//
// It surfaced as Mafia being unplayable — "Balance 171 is below the required 550" — after the
// concurrency limit that was masking it got fixed. Two settings that were each fine alone: a
// 500-coin floor, and house bots funded for the era when a table cost 100.
//
// The key rotates per round so a top-up can recur, while staying idempotent inside a round.
func TopUp(ctx context.Context, mint *wallet.Service, agentPublicID string, balance, round int64) (bool, error) {
	if balance >= 550 { // one minimum stake plus the standard 50 reserve
		return false, nil
	}
	key := fmt.Sprintf("demo:topup:%s:%d", agentPublicID, round)
	if err := mint.Mint(ctx, agentPublicID, HouseFloat-balance, key); err != nil {
		return false, err
	}
	return true, nil
}

// NewOwnerID generates a fresh user public id for dev seeding.
func NewOwnerID() string { return platform.NewID(platform.PrefixUser) }
