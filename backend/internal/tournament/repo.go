// Package tournament implements the funded freeroll (the hero tournament): a
// sponsor funds a prize pool, entry is free but eligibility-gated (a
// tournament_ready agent with no active fraud flag), and a single champion is
// paid through the ledger. The prize pool moves via the Bank port (the ledger),
// so reconciliation stays exact. See Stage 10.
package tournament

import "context"

// Repo persists tournaments and entries and answers eligibility.
type Repo interface {
	Create(ctx context.Context, in CreateInput) (publicID string, err error)
	Get(ctx context.Context, publicID string) (Tournament, error)
	// Enter records a free entry (idempotent); errors if the tournament is closed.
	Enter(ctx context.Context, publicID, agentPublicID string) error
	// Eligible reports whether an agent may enter/win: tournament_ready badge and
	// no active fraud flag. reason explains a false result.
	Eligible(ctx context.Context, agentPublicID string) (ok bool, reason string, err error)
	// Finalize records the champion + finished status if not already finished.
	// firstTime is false on a repeat call; the returned Tournament always carries
	// the effective winner + pool (so payout is idempotent either way).
	Finalize(ctx context.Context, publicID, winnerAgentPublicID string) (t Tournament, firstTime bool, err error)
}

// Bank moves the prize pool through the ledger. Satisfied by an adapter over
// ledger.Service in main. Both calls are idempotent on the tournament id.
type Bank interface {
	FundPool(ctx context.Context, tournamentPublicID string, coins int64) error
	PayWinner(ctx context.Context, tournamentPublicID, agentPublicID string, coins int64) error
}

// CreateInput is a new tournament.
type CreateInput struct {
	PublicID  string
	Name      string
	Sponsor   string
	PrizePool int64
}

// Tournament is the public view.
type Tournament struct {
	PublicID  string `json:"tournament_id"`
	Name      string `json:"name"`
	Sponsor   string `json:"sponsor,omitempty"`
	PrizePool int64  `json:"prize_pool"`
	Status    string `json:"status"`
	Winner    string `json:"winner,omitempty"`
	Entries   int    `json:"entries"`
}
