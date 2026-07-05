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

// MintCoins tops up an agent wallet for dev play.
func MintCoins(ctx context.Context, mint *wallet.Service, agentPublicID string, amount int64) error {
	key := "demo:mint:" + agentPublicID
	return mint.Mint(ctx, agentPublicID, amount, key)
}

// NewOwnerID generates a fresh user public id for dev seeding.
func NewOwnerID() string { return platform.NewID(platform.PrefixUser) }
