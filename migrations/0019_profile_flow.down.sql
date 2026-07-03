BEGIN;

DROP TABLE IF EXISTS magic_links;
DROP TABLE IF EXISTS agent_game_config;

DROP INDEX IF EXISTS uq_agents_owner;
ALTER TABLE agents
    DROP COLUMN IF EXISTS avatar_url,
    DROP COLUMN IF EXISTS bio,
    DROP COLUMN IF EXISTS display_name;

COMMIT;
