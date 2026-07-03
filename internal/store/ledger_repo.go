package store

import (
	"context"
	"errors"
	"sort"

	"github.com/agent-arena/arena/internal/ledger"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LedgerRepo is the pgx implementation of ledger.Repo. It is the only code that
// writes to wallets/ledger_transactions/ledger_entries. Every apply is a single
// transaction that locks the affected wallets FOR UPDATE in a deadlock-free order
// (ascending id), enforces the protected non-negative invariant, and is
// idempotent on idempotency_key (UNIQUE) so a retry can never double-spend.
type LedgerRepo struct{ db *pgxpool.Pool }

func NewLedgerRepo(db *pgxpool.Pool) *LedgerRepo { return &LedgerRepo{db: db} }

var _ ledger.Repo = (*LedgerRepo)(nil)

func (r *LedgerRepo) Apply(ctx context.Context, in ledger.ApplyInput) (ledger.ApplyResult, error) {
	// Cheap idempotency precheck; the UNIQUE index is the authoritative guard.
	if pub, found, err := r.existingTxn(ctx, in.Key); err != nil {
		return ledger.ApplyResult{}, err
	} else if found {
		return ledger.ApplyResult{PublicID: pub, Applied: false}, nil
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return ledger.ApplyResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Resolve every posting to a wallet id and aggregate the net delta per
	//    wallet (a txn may legitimately touch one wallet more than once).
	type winfo struct {
		kind  string
		delta int64
	}
	deltas := map[int64]*winfo{}
	type entry struct {
		walletID int64
		amount   int64
	}
	entries := make([]entry, 0, len(in.Postings))
	for _, p := range in.Postings {
		id, kind, err := resolveWallet(ctx, tx, p.Wallet)
		if err != nil {
			return ledger.ApplyResult{}, err
		}
		if deltas[id] == nil {
			deltas[id] = &winfo{kind: kind}
		}
		deltas[id].delta += p.Amount
		entries = append(entries, entry{walletID: id, amount: p.Amount})
	}

	// 2. Lock all affected wallets in ascending id order and read balances.
	ids := make([]int64, 0, len(deltas))
	for id := range deltas {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	balances := map[int64]int64{}
	rows, err := tx.Query(ctx, `SELECT id, balance FROM wallets WHERE id = ANY($1) ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return ledger.ApplyResult{}, err
	}
	for rows.Next() {
		var id, bal int64
		if err := rows.Scan(&id, &bal); err != nil {
			rows.Close()
			return ledger.ApplyResult{}, err
		}
		balances[id] = bal
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ledger.ApplyResult{}, err
	}

	// 3. Enforce the protected non-negative invariant before any write.
	for id, info := range deltas {
		if balances[id]+info.delta < 0 {
			switch info.kind {
			case "agent", "user":
				return ledger.ApplyResult{}, ledger.ErrInsufficient
			case ledger.SysEscrow:
				return ledger.ApplyResult{}, ledger.ErrInvariant
			}
		}
	}

	// 4. Insert the transaction. A concurrent insert of the same key surfaces as
	//    a unique violation ⇒ the other writer won the race; treat as a replay.
	md := in.Metadata
	if md == nil {
		md = map[string]any{}
	}
	var txnID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO ledger_transactions (public_id, kind, idempotency_key, metadata)
		 VALUES ($1,$2,$3,$4::jsonb) RETURNING id`,
		in.PublicID, in.Kind, in.Key, mustJSON(md)).Scan(&txnID)
	if isUniqueViolation(err) {
		_ = tx.Rollback(ctx)
		pub, _, e := r.existingTxn(ctx, in.Key)
		if e != nil {
			return ledger.ApplyResult{}, e
		}
		return ledger.ApplyResult{PublicID: pub, Applied: false}, nil
	}
	if err != nil {
		return ledger.ApplyResult{}, err
	}

	// 5. Write entries and apply balance deltas.
	for _, e := range entries {
		if _, err := tx.Exec(ctx,
			`INSERT INTO ledger_entries (txn_id, wallet_id, amount) VALUES ($1,$2,$3)`,
			txnID, e.walletID, e.amount); err != nil {
			return ledger.ApplyResult{}, err
		}
	}
	for id, info := range deltas {
		if _, err := tx.Exec(ctx,
			`UPDATE wallets SET balance = balance + $2, updated_at = now() WHERE id = $1`,
			id, info.delta); err != nil {
			return ledger.ApplyResult{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return ledger.ApplyResult{}, err
	}
	return ledger.ApplyResult{PublicID: in.PublicID, Applied: true}, nil
}

func (r *LedgerRepo) Balance(ctx context.Context, agentPublicID string) (int64, error) {
	var bal int64
	err := r.db.QueryRow(ctx,
		`SELECT w.balance FROM wallets w JOIN agents a ON a.id = w.agent_id WHERE a.public_id = $1`,
		agentPublicID).Scan(&bal)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ledger.ErrWalletNotFound
	}
	return bal, err
}

func (r *LedgerRepo) UserBalance(ctx context.Context, userPublicID string) (int64, error) {
	var bal int64
	err := r.db.QueryRow(ctx,
		`SELECT w.balance FROM wallets w JOIN users u ON u.id = w.user_id WHERE u.public_id = $1`,
		userPublicID).Scan(&bal)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ledger.ErrWalletNotFound
	}
	return bal, err
}

func (r *LedgerRepo) History(ctx context.Context, agentPublicID string, limit int) ([]ledger.Line, error) {
	rows, err := r.db.Query(ctx,
		`SELECT t.public_id, t.kind, e.amount, t.created_at
		 FROM ledger_entries e
		 JOIN ledger_transactions t ON t.id = e.txn_id
		 JOIN wallets w ON w.id = e.wallet_id
		 JOIN agents  a ON a.id = w.agent_id
		 WHERE a.public_id = $1
		 ORDER BY e.id DESC
		 LIMIT $2`, agentPublicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ledger.Line
	for rows.Next() {
		var l ledger.Line
		if err := rows.Scan(&l.TxnPublicID, &l.Kind, &l.Amount, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (r *LedgerRepo) UserHistory(ctx context.Context, userPublicID string, limit int) ([]ledger.Line, error) {
	rows, err := r.db.Query(ctx,
		`SELECT t.public_id, t.kind, e.amount, t.created_at
		 FROM ledger_entries e
		 JOIN ledger_transactions t ON t.id = e.txn_id
		 JOIN wallets w ON w.id = e.wallet_id
		 JOIN users  u ON u.id = w.user_id
		 WHERE u.public_id = $1
		 ORDER BY e.id DESC
		 LIMIT $2`, userPublicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ledger.Line
	for rows.Next() {
		var l ledger.Line
		if err := rows.Scan(&l.TxnPublicID, &l.Kind, &l.Amount, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (r *LedgerRepo) Reconcile(ctx context.Context) ([]ledger.Drift, error) {
	rows, err := r.db.Query(ctx,
		`SELECT w.id, w.kind, w.balance, COALESCE(SUM(e.amount), 0)
		 FROM wallets w
		 LEFT JOIN ledger_entries e ON e.wallet_id = w.id
		 GROUP BY w.id, w.kind, w.balance
		 HAVING w.balance <> COALESCE(SUM(e.amount), 0)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ledger.Drift
	for rows.Next() {
		var d ledger.Drift
		if err := rows.Scan(&d.WalletID, &d.Kind, &d.Balance, &d.EntrySum); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (r *LedgerRepo) existingTxn(ctx context.Context, key string) (string, bool, error) {
	var pub string
	err := r.db.QueryRow(ctx,
		`SELECT public_id FROM ledger_transactions WHERE idempotency_key = $1`, key).Scan(&pub)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return pub, true, nil
}

// resolveWallet maps a WalletRef to its wallet id and kind. System wallets match
// on (kind, agent_id IS NULL, user_id IS NULL); user wallets join through users;
// agent wallets join through agents.
func resolveWallet(ctx context.Context, tx pgx.Tx, ref ledger.WalletRef) (int64, string, error) {
	var id int64
	if ref.IsSystem() {
		err := tx.QueryRow(ctx,
			`SELECT id FROM wallets WHERE kind = $1 AND agent_id IS NULL AND user_id IS NULL`, ref.System).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, "", ledger.ErrWalletNotFound
		}
		if err != nil {
			return 0, "", err
		}
		return id, ref.System, nil
	}
	if ref.IsUser() {
		err := tx.QueryRow(ctx,
			`SELECT w.id FROM wallets w JOIN users u ON u.id = w.user_id WHERE u.public_id = $1`,
			ref.User).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, "", ledger.ErrWalletNotFound
		}
		if err != nil {
			return 0, "", err
		}
		return id, "user", nil
	}
	err := tx.QueryRow(ctx,
		`SELECT w.id FROM wallets w JOIN agents a ON a.id = w.agent_id WHERE a.public_id = $1`,
		ref.Agent).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ledger.ErrWalletNotFound
	}
	if err != nil {
		return 0, "", err
	}
	return id, "agent", nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
