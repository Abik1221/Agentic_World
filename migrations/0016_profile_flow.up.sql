-- 0016_profile_flow — single-agent profile (display name, bio, avatar), per-game
-- behaviour config, and passwordless email magic-link recovery.
--
-- Product rules encoded here:
--   * One user owns at most one agent  → unique index on agents(owner_user_id).
--   * An agent has a public identity    → display_name, bio, avatar_url columns.
--   * Same agent, per-game instincts     → agent_game_config(agent, game_type).
--   * Passwordless recovery              → magic_links(token → user, single use).

BEGIN;

-- ── Agent profile identity ──────────────────────────────────────────────────
ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS display_name TEXT,
    ADD COLUMN IF NOT EXISTS bio          TEXT,
    ADD COLUMN IF NOT EXISTS avatar_url   TEXT;

-- Enforce one agent per user. (If existing data violates this, dedupe first.)
CREATE UNIQUE INDEX IF NOT EXISTS uq_agents_owner ON agents(owner_user_id);

-- ── Per-game behaviour ──────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS agent_game_config (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id   BIGINT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    game_type  TEXT   NOT NULL,                 -- 'mafia' | 'goofspiel'
    behavior   JSONB  NOT NULL DEFAULT '{}',    -- {aggression,risk,bluff,chattiness}
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_game_config_game CHECK (game_type IN ('mafia', 'goofspiel'))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_game_config
    ON agent_game_config(agent_id, game_type);

-- ── Passwordless magic-link recovery ────────────────────────────────────────
CREATE TABLE IF NOT EXISTS magic_links (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash  TEXT NOT NULL UNIQUE,           -- store a hash, never the raw token
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email       CITEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_magic_links_user ON magic_links(user_id);

COMMIT;
