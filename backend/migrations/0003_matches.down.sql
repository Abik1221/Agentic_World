ALTER TABLE agent_timing_samples DROP CONSTRAINT IF EXISTS fk_timing_match;
DROP TABLE IF EXISTS match_events;
DROP TABLE IF EXISTS match_players;
DROP TABLE IF EXISTS matches;
