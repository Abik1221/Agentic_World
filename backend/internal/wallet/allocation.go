package wallet

import (
	"context"
	"fmt"
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/ledger"
)

// Allocate moves coins from the owner's treasury wallet to one of their agents.
// Agents in active matches cannot receive rebalancing (caller must check).
//
// clientKey is an optional caller-supplied idempotency key: when non-empty, a
// retried request carrying the same key is a no-op (the ledger's UNIQUE key
// dedupes it), so a network retry can't double-move the owner's treasury. When
// empty the key is time-based (legacy behaviour: every call is a distinct txn),
// so callers that need retry-safety MUST pass a stable key.
func (s *Service) Allocate(ctx context.Context, ownerUserPublicID, agentPublicID string, coins int64, clientKey string) error {
	if coins <= 0 {
		return httpx.NewError(http.StatusBadRequest, "invalid_amount", "Amount must be positive.")
	}
	owner, err := s.repo.OwnerOf(ctx, agentPublicID)
	if err != nil {
		return err
	}
	if owner != ownerUserPublicID {
		return httpx.ErrForbidden
	}
	active, err := s.repo.ActiveMatchCount(ctx, agentPublicID)
	if err != nil {
		return err
	}
	if active > 0 {
		return httpx.NewError(http.StatusConflict, "agent_in_match", "Cannot rebalance coins while the agent is in an active match.")
	}
	key := fmt.Sprintf("allocate:%s:%d", agentPublicID, s.clock.Now().UnixNano())
	if clientKey != "" {
		key = fmt.Sprintf("allocate:%s:%s", agentPublicID, clientKey)
	}
	res, err := s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindAllocate,
		Key:      key,
		Metadata: map[string]any{"user": ownerUserPublicID, "agent": agentPublicID, "coins": coins},
		Postings: []ledger.Posting{
			{Wallet: ledger.UserWallet(ownerUserPublicID), Amount: -coins},
			{Wallet: ledger.AgentWallet(agentPublicID), Amount: coins},
		},
	})
	if err != nil {
		return err
	}
	// Treasury just fell by `coins`. Push it so every open tab's balance moves, not
	// just the one that happened to submit the form. Only on Applied — a dedupe of a
	// retried request moved nothing.
	if res.Applied {
		s.signalUser(ownerUserPublicID, eventCoinsAllocated, key,
			map[string]any{"agent": agentPublicID, "coins": coins})
	}
	return nil
}
