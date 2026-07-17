-- Revert per-arena ratings back to a single global (agent_id, season) rating.
-- Non-goofspiel arena rows are dropped to restore uniqueness on (agent_id, season).

ALTER TABLE ratings DROP CONSTRAINT ratings_pkey;
DROP INDEX IF EXISTS idx_ratings_leaderboard;

DELETE FROM ratings WHERE game <> 'goofspiel';

ALTER TABLE ratings ADD PRIMARY KEY (agent_id, season);
CREATE INDEX idx_ratings_leaderboard ON ratings (season, elo DESC, agent_id);

ALTER TABLE ratings DROP COLUMN game;
ALTER TABLE ratings DROP COLUMN mu;
ALTER TABLE ratings DROP COLUMN sigma;
ALTER TABLE ratings DROP COLUMN algo;
