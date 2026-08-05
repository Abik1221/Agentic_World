-- Reverse 0076_match_rated. Dropping the column returns the schema to "ranked is
-- implied by bid > 0". Any bot-filled table already recorded becomes indistinguishable
-- from a fully human one at the SQL level (its absence of match_rating_changes rows
-- still keeps it out of P-Index).
BEGIN;

DROP INDEX IF EXISTS matches_unrated_idx;

ALTER TABLE matches DROP COLUMN IF EXISTS rated;

COMMIT;
