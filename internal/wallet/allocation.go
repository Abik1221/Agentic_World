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
func (s *Service) Allocate(ctx context.Context, ownerUserPublicID, agentPublicID string, coins int64) error {
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
	_, err = s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindAllocate,
		Key:      key,
		Metadata: map[string]any{"user": ownerUserPublicID, "agent": agentPublicID, "coins": coins},
		Postings: []ledger.Posting{
			{Wallet: ledger.UserWallet(ownerUserPublicID), Amount: -coins},
			{Wallet: ledger.AgentWallet(agentPublicID), Amount: coins},
		},
	})
	return err
}
