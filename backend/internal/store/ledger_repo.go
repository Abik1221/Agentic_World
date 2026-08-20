package store

import (
	"context"
	"errors"
	"fmt"
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

func (r *LedgerRepo) History(ctx context.Context, agentPublicID string, limit, offset int) ([]ledger.Line, error) {
	rows, err := r.db.Query(ctx,
		`SELECT t.public_id, t.kind, e.amount, t.created_at
		 FROM ledger_entries e
		 JOIN ledger_transactions t ON t.id = e.txn_id
		 JOIN wallets w ON w.id = e.wallet_id
		 JOIN agents  a ON a.id = w.agent_id
		 WHERE a.public_id = $1
		 ORDER BY e.id DESC
		 LIMIT $2 OFFSET $3`, agentPublicID, limit, offset)
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

func (r *LedgerRepo) UserHistory(ctx context.Context, userPublicID string, limit, offset int) ([]ledger.Line, error) {
	rows, err := r.db.Query(ctx,
		`SELECT t.public_id, t.kind, e.amount, t.created_at
		 FROM ledger_entries e
		 JOIN ledger_transactions t ON t.id = e.txn_id
		 JOIN wallets w ON w.id = e.wallet_id
		 JOIN users  u ON u.id = w.user_id
		 WHERE u.public_id = $1
		 ORDER BY e.id DESC
		 LIMIT $2 OFFSET $3`, userPublicID, limit, offset)
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

// AuditLedger recomputes the double-entry invariants straight from the rows.
//
// Deliberately recomputes rather than trusting any counter the writer maintains: a bug in the
// posting path must not be able to silence the check that would catch it. Read-only, no locks,
// safe to run against production — and it repairs nothing, because an audit that also fixed
// things would destroy the evidence of how the imbalance arose.
//
// Each check is capped at 20 identifiers. An alert that dumps ten thousand ids is one nobody
// reads, and the count is reported in full regardless.
func (r *LedgerRepo) AuditLedger(ctx context.Context) (ledger.AuditReport, error) {
	rep := ledger.AuditReport{Healthy: true}

	if err := r.db.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM ledger_transactions),
		(SELECT count(*) FROM ledger_entries),
		(SELECT count(*) FROM wallets)`).
		Scan(&rep.Transactions, &rep.Entries, &rep.Wallets); err != nil {
		return rep, fmt.Errorf("ledger audit: counting rows: %w", err)
	}

	checks := []struct{ name, severity, query string }{
		{
			// Every posting must sum to zero across its entries. A non-zero sum is coins minted
			// or burned by that single transaction — the gravest finding here, because a payout
			// could be funded from nowhere.
			"unbalanced_transactions", ledger.SeverityCritical,
			`SELECT count(*), coalesce(string_agg(txn_id::text, ',' ORDER BY txn_id), '')
			   FROM (SELECT txn_id FROM ledger_entries
			          GROUP BY txn_id HAVING sum(amount) <> 0 LIMIT 20) x`,
		},
		{
			// The stored balance must equal the sum of that wallet's entries. Entries are the
			// record of truth; balance is a cache of them, so drift means the number a user SEES
			// is wrong even when the history behind it is right.
			"wallet_balance_drift", ledger.SeverityHigh,
			`SELECT count(*), coalesce(string_agg(id::text, ',' ORDER BY id), '')
			   FROM (SELECT w.id FROM wallets w
			         LEFT JOIN ledger_entries e ON e.wallet_id = w.id
			         GROUP BY w.id, w.balance
			         HAVING w.balance <> coalesce(sum(e.amount), 0) LIMIT 20) y`,
		},
		{
			// The schema forbids this. A row here means the CHECK was bypassed by a direct write,
			// or dropped by a migration and never restored.
			"negative_agent_or_escrow_balance", ledger.SeverityCritical,
			`SELECT count(*), coalesce(string_agg(id::text, ',' ORDER BY id), '')
			   FROM (SELECT id FROM wallets
			         WHERE kind IN ('agent','escrow') AND balance < 0 LIMIT 20) z`,
		},
		{
			// An entry whose transaction is gone is a coin move with no recorded reason.
			"orphan_entries", ledger.SeverityCritical,
			`SELECT count(*), coalesce(string_agg(eid::text, ',' ORDER BY eid), '')
			   FROM (SELECT e.id AS eid FROM ledger_entries e
			         LEFT JOIN ledger_transactions t ON t.id = e.txn_id
			         WHERE t.id IS NULL LIMIT 20) o`,
		},
	}

	// Escrow reconciliation, computed before the row checks so the figures are reported even when
	// a later check errors. Every coin in escrow must be explained by a match that has not settled
	// — either still open, or finished with its retention recorded somewhere.
	//
	// # payout_holds is the ONLY hold state, and this check is deliberately strict about it
	//
	// held_settlements is not a second hold record. payout_holds says a match's payout IS held;
	// held_settlements carries the multi-winner SPLIT to replay when it is released. Every deny
	// branch of the real gate (antifraud.Service.Allow) calls RecordHold, so a genuinely held
	// match always has the payout_holds row — including the Mafia and Monopoly paths, which
	// write the split in addition to, never instead of, the hold.
	//
	// So a match with a split and no hold state is NOT explained escrow, and must keep firing.
	// It means a deny path wrote the payout map and forgot the hold, which would leave coins
	// retained with nothing marking them retained and nothing for an admin release to claim.
	//
	// I briefly widened this to accept held_settlements alone, after a critical fired on three
	// such matches. That was wrong: the three came from TestMoneyFlowE2E_TenAgents, whose
	// denyGate stub refuses without recording a hold — a state the production gate cannot
	// produce. Widening the check would have masked exactly the defect it exists to catch. The
	// fixture was fixed instead.
	const escrowSQL = `
		WITH staked AS (
		  SELECT metadata->>'match' AS mid FROM ledger_transactions WHERE kind = 'stake'),
		closed AS (
		  SELECT DISTINCT metadata->>'match' AS mid FROM ledger_transactions
		   WHERE kind IN ('settle','refund')),
		unsettled AS (
		  SELECT m.id, m.status,
		         m.bid * (SELECT count(*) FROM match_players mp WHERE mp.match_id = m.id) AS stake
		    FROM staked s JOIN matches m ON m.public_id = s.mid
		   WHERE NOT EXISTS (SELECT 1 FROM closed c WHERE c.mid = s.mid))
		SELECT
		  (SELECT coalesce(sum(balance),0) FROM wallets WHERE kind = 'escrow'),
		  coalesce(sum(stake) FILTER (
		    WHERE EXISTS (SELECT 1 FROM payout_holds h
		                   WHERE h.match_id = unsettled.id AND h.status = 'held')), 0),
		  coalesce(sum(stake) FILTER (
		    WHERE status NOT IN ('finished','aborted','cancelled')), 0),
		  coalesce(sum(stake) FILTER (
		    WHERE status IN ('finished','aborted','cancelled')
		      AND NOT EXISTS (SELECT 1 FROM payout_holds h
		                       WHERE h.match_id = unsettled.id AND h.status = 'held')), 0)
		FROM unsettled`
	var unexplained int64
	if err := r.db.QueryRow(ctx, escrowSQL).
		Scan(&rep.EscrowBalance, &rep.EscrowHeld, &rep.EscrowOpen, &unexplained); err != nil {
		return rep, fmt.Errorf("ledger audit: escrow reconciliation: %w", err)
	}
	if unexplained != 0 {
		rep.Healthy = false
		rep.Findings = append(rep.Findings, ledger.AuditFinding{
			Check: "escrow_unexplained", Count: unexplained, Severity: ledger.SeverityCritical,
			Detail: "coins in escrow for a terminal match with no payout hold recorded — " +
				"the stake was taken and there is no story for where it went",
		})
	}

	// ESCROW THAT BELONGS TO NO MATCH ROW AT ALL.
	//
	// Everything above reconciles through `unsettled`, which INNER JOINs stakes to
	// `matches`. That join is the blind spot: a stake whose match row is absent —
	// never persisted, or removed later — is dropped by the join and then counted
	// nowhere. Not open, not held, not unexplained. The audit would report those
	// coins as "clean" while nothing on earth could release them.
	//
	// This is not hypothetical. On the lab database the escrow wallet held
	// 1,521,400 coins against 7,900 explained, and the audit logged "ledger audit
	// clean" on every run, because the 1,513,500 difference sat entirely outside
	// the join.
	//
	// The check is a subtraction the report already had the operands for: the REAL
	// wallet balance minus everything the reconciliation managed to attribute. By
	// this file's own definition — "money the platform has taken and has no story
	// for" — whatever is left over is exactly the defect.
	//
	// Signed deliberately. A NEGATIVE residual is also wrong: it means more was
	// attributed than the wallet actually holds, i.e. the stake arithmetic
	// over-counts, and silently clamping that to zero would hide it.
	if residual := rep.EscrowBalance - (rep.EscrowHeld + rep.EscrowOpen + unexplained); residual != 0 {
		rep.Healthy = false
		rep.Findings = append(rep.Findings, ledger.AuditFinding{
			Check: "escrow_unattributed", Count: residual, Severity: ledger.SeverityCritical,
			Detail: fmt.Sprintf("escrow wallet holds %d but only %d is attributable to a match "+
				"(held %d + open %d + unexplained %d); the %d difference belongs to no match row "+
				"and nothing can release it",
				rep.EscrowBalance, rep.EscrowHeld+rep.EscrowOpen+unexplained,
				rep.EscrowHeld, rep.EscrowOpen, unexplained, residual),
		})
	}

	for _, c := range checks {
		var n int64
		var detail string
		if err := r.db.QueryRow(ctx, c.query).Scan(&n, &detail); err != nil {
			return rep, fmt.Errorf("ledger audit: %s: %w", c.name, err)
		}
		if n == 0 {
			continue
		}
		rep.Healthy = false
		rep.Findings = append(rep.Findings, ledger.AuditFinding{
			Check: c.name, Count: n, Detail: detail, Severity: c.severity,
		})
	}
	return rep, nil
}
