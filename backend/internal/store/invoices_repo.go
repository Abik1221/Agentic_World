package store

import (
	"context"
	"errors"

	"github.com/agent-arena/arena/internal/invoices"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InvoicesRepo assembles a user's receipts from the three tables where money
// actually moved: on-chain deposits, card/subscription top-ups, and withdrawals.
//
// A UNION rather than a new table. There is no invoices table by design (see the
// package doc): a stored copy of these figures is a second source of truth that
// will eventually disagree with the ledger, and the disagreement would be
// discovered by a user reading a receipt.
type InvoicesRepo struct {
	db *pgxpool.Pool
	// coinCents converts credits to currency for the rows that only store one of
	// the two. Deposits record base units and credits but no cents; withdrawals
	// record cents. Passing it in keeps the repo free of economy config.
	coinCents int64
}

func NewInvoicesRepo(db *pgxpool.Pool, coinCents int64) *InvoicesRepo {
	if coinCents <= 0 {
		coinCents = 1
	}
	return &InvoicesRepo{db: db, coinCents: coinCents}
}

var _ invoices.Repo = (*InvoicesRepo)(nil)

// invoiceSelect is the shared projection. Every branch produces the same columns
// in the same order so List and Get cannot drift apart — a receipt that renders
// differently depending on which endpoint fetched it is a bug nobody would think
// to look for.
//
// $1 = user public id, $2 = coin_cents.
const invoiceSelect = `
WITH me AS (SELECT id, public_id FROM users WHERE public_id = $1)
SELECT * FROM (
  -- On-chain deposits. Only sessions that actually credited become receipts:
  -- an expired or abandoned request is not a document anyone wants.
  --
  -- The fee comes from the LEDGER, not from the deposit tables. Both
  -- deposit_sessions.coins_credited and solana_deposits.coins record the GROSS
  -- pegged amount; the split between what the user received and what the platform
  -- kept exists only in the ledger transaction's metadata. Deriving the fee from
  -- the deposit rows would produce a receipt claiming the user got 1000 credits
  -- when 950 reached their balance — a document that contradicts the money it
  -- describes, which is the one thing a receipt must never do.
  SELECT
    ds.public_id                        AS id,
    'deposit'                           AS kind,
    ds.status                           AS status,
    COALESCE(sd.created_at, ds.created_at) AS issued_at,
    ds.asset                            AS asset,
    'solana'                            AS chain,
    COALESCE(sd.amount_base, ds.amount_expected) AS amount_base,
    6                                   AS decimals,
    -- Net credited: the ledger's figure when we have it, else the gross we recorded.
    COALESCE((lt.metadata->>'coins')::bigint, ds.coins_credited, sd.coins, ds.coins_expected) AS coins,
    COALESCE((lt.metadata->>'fee')::bigint, 0)  AS fee_coins,
    COALESCE(sd.coins, ds.coins_expected) * $2  AS gross_cents,
    COALESCE((lt.metadata->>'fee')::bigint, 0) * $2 AS fee_cents,
    COALESCE((lt.metadata->>'coins')::bigint, ds.coins_credited, ds.coins_expected) * $2 AS net_cents,
    COALESCE(sd.tx_signature, ds.tx_signature, '') AS reference,
    ''                                  AS counterparty
  FROM deposit_sessions ds
  JOIN me ON me.id = ds.user_id
  LEFT JOIN solana_deposits sd ON sd.session_id = ds.id
  LEFT JOIN ledger_transactions lt
         ON lt.idempotency_key = 'solana:' || COALESCE(sd.tx_signature, ds.tx_signature)
  WHERE ds.status = 'completed'

  UNION ALL

  -- Card / subscription credits. The provider owns the charge; we only know what
  -- was charged and what we credited.
  SELECT
    cp.payment_intent                   AS id,
    'topup'                             AS kind,
    'completed'                         AS status,
    cp.created_at                       AS issued_at,
    'USD'                               AS asset,
    'stripe'                            AS chain,
    0                                   AS amount_base,
    0                                   AS decimals,
    cp.coins                            AS coins,
    0                                   AS fee_coins,
    cp.amount_cents                     AS gross_cents,
    0                                   AS fee_cents,
    cp.amount_cents                     AS net_cents,
    cp.payment_intent                   AS reference,
    ''                                  AS counterparty
  FROM coin_purchases cp
  JOIN me ON me.public_id = cp.user_public_id

  UNION ALL

  -- Withdrawals. Included from the moment they are requested, not only when paid:
  -- the credits have already left the balance, so the user is owed a document
  -- explaining where they went while it is still in flight.
  SELECT
    w.public_id                         AS id,
    'withdrawal'                        AS kind,
    w.status                            AS status,
    COALESCE(w.resolved_at, w.requested_at) AS issued_at,
    'USDC'                              AS asset,
    w.chain                             AS chain,
    0                                   AS amount_base,
    6                                   AS decimals,
    w.coins                             AS coins,
    w.fee_coins                         AS fee_coins,
    w.gross_cents                       AS gross_cents,
    w.stripe_fee_cents                  AS fee_cents,
    w.net_cents                         AS net_cents,
    COALESCE(w.transfer_id, '')         AS reference,
    COALESCE(w.dest_wallet_address, '') AS counterparty
  FROM withdrawals w
  JOIN me ON me.id = w.user_id
) invoices`

func (r *InvoicesRepo) List(ctx context.Context, userPublicID string, limit, offset int) ([]invoices.Row, error) {
	rows, err := r.db.Query(ctx,
		invoiceSelect+` ORDER BY issued_at DESC LIMIT $3 OFFSET $4`,
		userPublicID, r.coinCents, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []invoices.Row{}
	for rows.Next() {
		row, err := scanInvoiceRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Get scopes by user IN THE QUERY, so an id belonging to someone else returns
// found=false. The handler renders that as 404 — identical to a nonexistent id,
// which is what stops the endpoint confirming that another user's invoice exists.
func (r *InvoicesRepo) Get(ctx context.Context, userPublicID, id string) (invoices.Row, bool, error) {
	row := r.db.QueryRow(ctx, invoiceSelect+` WHERE id = $3`, userPublicID, r.coinCents, id)
	out, err := scanInvoiceRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return invoices.Row{}, false, nil
	}
	if err != nil {
		return invoices.Row{}, false, err
	}
	return out, true, nil
}

// scanner is the shared shape of pgx.Row and pgx.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanInvoiceRow(s scanner) (invoices.Row, error) {
	var r invoices.Row
	err := s.Scan(&r.ID, &r.Kind, &r.Status, &r.IssuedAt, &r.Asset, &r.Chain,
		&r.AmountBase, &r.Decimals, &r.Coins, &r.FeeCoins,
		&r.GrossCents, &r.FeeCents, &r.NetCents, &r.Reference, &r.Counterpty)
	return r, err
}
