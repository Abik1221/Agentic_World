package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GameMoveSig is one persisted per-move authorship proof for the non-card games
// (Mafia/Monopoly). Mirror of match.MoveSignature but keyed on (seq, seat, action).
type GameMoveSig struct {
	Seq       int
	Seat      int
	Action    string
	Signature string
	Pubkey    string
}

// agentSigningKey returns the agent's registered Ed25519 public key (base64) or ""
// if none. Shared by every game (the key lives on the agents row).
func agentSigningKey(ctx context.Context, db *pgxpool.Pool, agentPublicID string) (string, error) {
	var key string
	err := db.QueryRow(ctx,
		`SELECT COALESCE(signing_pubkey, '') FROM agents WHERE public_id = $1`, agentPublicID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return key, err
}

// recordGameMoveSig persists a verified proof (best-effort append; a re-submit of
// the same (match, seq, seat, action) is deduped by the UNIQUE constraint).
func recordGameMoveSig(ctx context.Context, db *pgxpool.Pool, game, matchPublicID string, seq, seat int, action, signature, pubkey string) error {
	_, err := db.Exec(ctx,
		`INSERT INTO game_move_signatures (game, match_public_id, seq, seat, action, signature, pubkey)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (match_public_id, seq, seat, action) DO NOTHING`,
		game, matchPublicID, seq, seat, action, signature, pubkey)
	return err
}

// loadGameMoveSigs returns every proof for a match (replay re-verification).
func loadGameMoveSigs(ctx context.Context, db *pgxpool.Pool, matchPublicID string) ([]GameMoveSig, error) {
	rows, err := db.Query(ctx,
		`SELECT seq, seat, action, signature, pubkey FROM game_move_signatures
		 WHERE match_public_id = $1 ORDER BY seq, seat`, matchPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GameMoveSig
	for rows.Next() {
		var s GameMoveSig
		if err := rows.Scan(&s.Seq, &s.Seat, &s.Action, &s.Signature, &s.Pubkey); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
