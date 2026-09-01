package store

// Discovering same-beneficiary account groups.
//
// # What this looks for
//
// Matchmaking used to refuse a pairing only when two agents shared an OwnerPublicID, so
// registering twice defeated it — and every downstream detector was then measuring a ring it
// was never allowed to seat together. See internal/antifraud/linkage.go for why the fix links
// by WHERE THE MONEY GOES rather than by device: a ring exists to move value to one place,
// and unlike a device fingerprint that link is the thing the fraud is for.
//
// Three signals, all already in the schema:
//
//	payout_wallet   withdrawals.dest_wallet_address — strongest. Where the money actually
//	                lands, and an attacker cannot fake sharing it.
//	stripe_connect  users.stripe_connect_id — the fiat equivalent.
//	login_wallet    users.wallet_address — weakest. Custodial and embedded wallets can
//	                legitimately collide, so it is surfaced for a reviewer rather than
//	                trusted on its own.
//
// # Why every signal is normalised and empties are excluded
//
// A NULL or empty column is not a shared identity. Without the filter, every account that has
// never withdrawn would share the empty string and the whole platform would collapse into one
// beneficiary group — which reads as "the check is working" right up until nobody can be
// matched with anyone. Addresses are lower-cased because wallet formats are case-insensitive
// in practice and two spellings of one address are one beneficiary.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agent-arena/arena/internal/antifraud"
)

// LinkageRepo discovers beneficiary groups.
type LinkageRepo struct{ db *pgxpool.Pool }

func NewLinkageRepo(db *pgxpool.Pool) *LinkageRepo { return &LinkageRepo{db: db} }

// BeneficiaryLinks returns every group of two or more owners sharing a payout identity.
//
// Groups of one are excluded: an owner linked only to itself carries no information, and
// including them would make the index as large as the user table for no benefit. The
// transitive closure is computed by antifraud.NewLinkIndex, not here — union-find belongs
// with the predicate that uses it, not in SQL.
func (r *LinkageRepo) BeneficiaryLinks(ctx context.Context) ([]antifraud.LinkedGroup, error) {
	rows, err := r.db.Query(ctx, `
		WITH links AS (
		    -- Where withdrawals are actually sent. The strongest signal.
		    SELECT $1::text AS kind,
		           lower(btrim(w.dest_wallet_address)) AS value,
		           u.public_id AS owner
		      FROM withdrawals w
		      JOIN users u ON u.id = w.user_id
		     WHERE COALESCE(btrim(w.dest_wallet_address), '') <> ''

		    UNION ALL

		    -- The fiat payout account.
		    SELECT $2::text, lower(btrim(u.stripe_connect_id)), u.public_id
		      FROM users u
		     WHERE COALESCE(btrim(u.stripe_connect_id), '') <> ''

		    UNION ALL

		    -- The login wallet. Weakest: custodial and embedded wallets collide honestly.
		    SELECT $3::text, lower(btrim(u.wallet_address)), u.public_id
		      FROM users u
		     WHERE COALESCE(btrim(u.wallet_address), '') <> ''
		)
		SELECT kind, value, array_agg(DISTINCT owner ORDER BY owner) AS owners
		  FROM links
		 GROUP BY kind, value
		HAVING count(DISTINCT owner) > 1`,
		antifraud.LinkPayoutWallet, antifraud.LinkStripeConnect, antifraud.LinkLoginWallet)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []antifraud.LinkedGroup
	for rows.Next() {
		var kind, value string
		var owners []string
		if err := rows.Scan(&kind, &value, &owners); err != nil {
			return nil, err
		}
		out = append(out, antifraud.LinkedGroup{
			Owners: owners,
			Via:    antifraud.Beneficiary{Kind: kind, Value: value},
		})
	}
	return out, rows.Err()
}
