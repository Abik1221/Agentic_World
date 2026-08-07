package demo

import (
	"context"
	"fmt"
	"log/slog"
	"time"

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

// TopUpSource lists house bots and their balances. Satisfied by *store.IdentityRepo.
type TopUpSource interface {
	HouseBotBalances(ctx context.Context) (map[string]int64, error)
}

// TopUpWorker keeps house bots solvent on an interval.
//
// # Why this cannot be a boot-time job
//
// Seeding funded them once, with a fixed idempotency key that could never fire again, and the
// population drains continuously: house bots play EACH OTHER at staked tables and the platform
// rakes every pot, so the pool shrinks by arithmetic rather than by bad play. A top-up at boot
// buys a few minutes and then the same wall returns — which is exactly what happened after the
// limit fixes landed: tables climbed from 1 seat to 12, then failed again on "Balance 496 is
// below the required 550".
//
// # Why a shrinking pool is not a symptom to work around
//
// It is the correct behaviour of the rake meeting a closed population. Nothing here is leaking
// coins — the ledger stays balanced and every drained coin is in platform_revenue. Topping up is
// how infrastructure is maintained, not a patch over a bug.
type TopUpWorker struct {
	src      TopUpSource
	mint     *wallet.Service
	interval time.Duration
	log      *slog.Logger
}

func NewTopUpWorker(src TopUpSource, mint *wallet.Service, interval time.Duration, log *slog.Logger) *TopUpWorker {
	if log == nil {
		log = slog.Default()
	}
	return &TopUpWorker{src: src, mint: mint, interval: interval, log: log}
}

// Run tops up until ctx is cancelled, once immediately on start.
func (w *TopUpWorker) Run(ctx context.Context) {
	w.log.Info("house bot top-up worker started", "interval", w.interval.String(), "float", HouseFloat)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		balances, err := w.src.HouseBotBalances(ctx)
		if err != nil {
			// Logged, not fatal. A failed read is not a reason to stop maintaining the pool, and
			// the next tick retries.
			w.log.Error("house bot balances could not be read", "error", err)
		}
		topped := 0
		for agentID, bal := range balances {
			// Round key on the interval so a restart inside the same window does not double-mint,
			// while a genuinely later window can.
			round := time.Now().Unix() / int64(w.interval.Seconds())
			ok, err := TopUp(ctx, w.mint, agentID, bal, round)
			if err != nil {
				w.log.Warn("house bot top-up failed", "agent", agentID, "balance", bal, "error", err)
				continue
			}
			if ok {
				topped++
			}
		}
		if topped > 0 {
			w.log.Info("house bots topped up", "count", topped, "of", len(balances), "to", HouseFloat)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
