-- Reverse 0015_sandbox. Destructive: removes all sandbox practice matches and the
-- seeded house agents. Gated/reviewed per coding-standards §3 — production rolls
-- forward. Order respects the FKs (events → players → matches → wallets → agents → user).

DELETE FROM match_events
    WHERE match_id IN (SELECT id FROM matches WHERE mode = 'sandbox');
DELETE FROM match_players
    WHERE match_id IN (SELECT id FROM matches WHERE mode = 'sandbox');
DELETE FROM matches WHERE mode = 'sandbox';

DELETE FROM wallets
    WHERE agent_id IN (SELECT id FROM agents WHERE kind = 'house');
DELETE FROM agents WHERE kind = 'house';
DELETE FROM users WHERE public_id = 'usr_system';

DROP INDEX IF EXISTS idx_agents_kind;
ALTER TABLE agents  DROP COLUMN IF EXISTS kind;
ALTER TABLE matches DROP COLUMN IF EXISTS bot_policy;
ALTER TABLE matches DROP COLUMN IF EXISTS mode;
