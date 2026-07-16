-- 0042_per_arena_ratings — make ratings independent PER ARENA (game) and add
-- TrueSkill state alongside Glicko.
--
-- Ratings become keyed (agent_id, game, season): every arena (goofspiel, mafia,
-- monopoly, and any future arena) maintains its own independent rating for an agent.
-- Existing rows are all goofspiel/glicko2 (the only rated arena to date), so the
-- ADD COLUMN ... DEFAULT backfills them correctly with no data migration.
--
-- Two rating algorithms now coexist: Glicko-2 for 1v1 arenas (keeps using
-- elo/rd/vol) and TrueSkill for N-player arenas (uses mu/sigma). The DISPLAYED
-- rating stays in `elo` for BOTH algorithms, so leaderboard/standing queries sort
-- uniformly regardless of algorithm.

ALTER TABLE ratings ADD COLUMN game  TEXT NOT NULL DEFAULT 'goofspiel';
ALTER TABLE ratings ADD COLUMN mu    DOUBLE PRECISION NOT NULL DEFAULT 25.0;
ALTER TABLE ratings ADD COLUMN sigma DOUBLE PRECISION NOT NULL DEFAULT 8.333333333333334;
ALTER TABLE ratings ADD COLUMN algo  TEXT NOT NULL DEFAULT 'glicko2';

-- Re-key by (agent_id, game, season). rating_updates stays keyed by match_id (a
-- match belongs to exactly one arena), so idempotency is unaffected.
ALTER TABLE ratings DROP CONSTRAINT ratings_pkey;
ALTER TABLE ratings ADD PRIMARY KEY (agent_id, game, season);

DROP INDEX IF EXISTS idx_ratings_leaderboard;
CREATE INDEX idx_ratings_leaderboard ON ratings (game, season, elo DESC, agent_id);
