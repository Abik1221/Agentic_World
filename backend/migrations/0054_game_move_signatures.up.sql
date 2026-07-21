-- 0054 — per-move Ed25519 authorship proofs for the non-card games (Mafia,
-- Monopoly), mirroring Goofspiel's move_signatures. Goofspiel binds (round, seat,
-- card); the richer games bind (seq, seat, action-string), so they need their own
-- shape rather than overloading move_signatures. Persistence is an audit trail
-- (replay re-verification) — the authoritative check happens on submit.
CREATE TABLE IF NOT EXISTS game_move_signatures (
    id                BIGSERIAL PRIMARY KEY,
    game              TEXT        NOT NULL,
    match_public_id   TEXT        NOT NULL,
    seq               INTEGER     NOT NULL,
    seat              INTEGER     NOT NULL,
    action            TEXT        NOT NULL,
    signature         TEXT        NOT NULL,
    pubkey            TEXT        NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- one proof per (match, slot, seat, action); a re-submit is deduped, not duplicated.
    UNIQUE (match_public_id, seq, seat, action)
);
CREATE INDEX IF NOT EXISTS idx_game_move_signatures_match ON game_move_signatures (match_public_id);
