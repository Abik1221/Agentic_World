-- Reverses 0088. A match sitting in ready_check when this runs has no valid status to fall
-- back to: it was never escrowed, so it cannot become active, and it is not in the lobby, so it
-- cannot become waiting. Abort it and let the agents requeue — losing a pre-escrow table costs
-- them a moment, while resurrecting one as active would escrow nothing and start a staked match
-- with no stakes behind it.
UPDATE matches SET status = 'aborted' WHERE status = 'ready_check';

DROP INDEX IF EXISTS idx_matches_ready_check;

ALTER TABLE match_players
    DROP COLUMN IF EXISTS ready_asked_at,
    DROP COLUMN IF EXISTS ready_asks,
    DROP COLUMN IF EXISTS ready_at;

ALTER TABLE matches
    DROP COLUMN IF EXISTS starts_at;
