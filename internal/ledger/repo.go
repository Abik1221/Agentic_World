package ledger

import "context"

// Repo is the persistence port for the ledger. The store implementation performs
// the atomic, FOR UPDATE-locked, idempotent write and owns the SQL; the service
// (ledger.go) owns the balance invariant (Σ == 0), checked in code before any DB
// work. The store is the only package that imports a DB driver.
type Repo interface {
	// Apply persists a balanced transaction atomically:
	//   - if a txn already exists for in.Key, it is a no-op and Applied is false
	//     (the pre-existing public id is returned);
	//   - otherwise it resolves wallet refs, locks the affected wallets FOR UPDATE
	//     in a deadlock-free order, rejects any protected wallet going negative
	//     (agent → ErrInsufficient, escrow → ErrInvariant), writes the txn + entries,
	//     updates balances, and returns Applied true.
	Apply(ctx context.Context, in ApplyInput) (ApplyResult, error)

	// Balance returns an agent wallet's current balance (ErrWalletNotFound if none).
	Balance(ctx context.Context, agentPublicID string) (int64, error)

	// History returns an agent's ledger lines, newest first, capped at limit.
	History(ctx context.Context, agentPublicID string, limit int) ([]Line, error)

	// Reconcile returns every wallet whose cached balance != Σ of its entries.
	// An empty slice means the books balance.
	Reconcile(ctx context.Context) ([]Drift, error)
}

// ApplyInput is the resolved, validated transaction handed to the store.
type ApplyInput struct {
	PublicID string // pre-generated txn_xxx
	Kind     string
	Key      string
	Metadata map[string]any
	Postings []Posting
}

// ApplyResult reports the txn that now owns the key and whether it was newly
// applied (false ⇒ idempotent replay, no new effect).
type ApplyResult struct {
	PublicID string
	Applied  bool
}
