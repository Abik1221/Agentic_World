-- 0076_match_rated — mark a match as excluded from ranked statistics.
--
-- Until now "ranked" was implied by `matches.bid > 0`: a staked table was rated, a
-- free one was not. Group matchmaking breaks that equivalence. When the queue is thin
-- (which is every queue at launch), a Mafia table can now start with fewer real agents
-- than its 12-seat roster, the remaining seats filled by kind='house' bots. Those
-- tables are REAL for money — the human seats stake and are paid — but they must not
-- count as ranked evidence:
--
--   * the bots are engine-driven, so beating them is not the same achievement as
--     beating eleven other developers' agents, and
--   * letting them count would let anyone farm P-Index and the model board by queueing
--     at an hour when nobody else is playing.
--
-- Rating exclusion itself is enforced in code (the finalize path skips rating entirely
-- when any seat is a house bot, so no match_rating_changes rows are written, which is
-- what P-Index reads). This column exists for the OTHER consumer: the model-benchmark
-- board reads agent_match_benchmark, which is written per human seat and so would
-- otherwise show a bot-filled table's decisions as ranked play.
--
-- Default TRUE so every historical row keeps its current meaning: before this change no
-- table could be bot-filled, so every past match was as-rated-as-its-stake.
BEGIN;

ALTER TABLE matches
    ADD COLUMN IF NOT EXISTS rated BOOLEAN NOT NULL DEFAULT TRUE;

COMMENT ON COLUMN matches.rated IS
    'FALSE when the table was filled with house bots to reach its roster, so it is excluded from ranked statistics (P-Index, model board). Independent of bid: a bot-filled table can still be staked and settled.';

-- The model board and P-Index scan finished matches inside a season window and now also
-- filter on rated. Partial index on the exclusion (the rare case) keeps that filter cheap
-- without duplicating the much larger rated=TRUE set.
CREATE INDEX IF NOT EXISTS matches_unrated_idx ON matches (finished_at) WHERE rated = FALSE;

COMMIT;
