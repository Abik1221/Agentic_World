-- 0050_agent_autoplay
-- Per-agent "deploy once, plays anytime" availability. When enabled, the
-- autoplay reconciler keeps the agent in matches without a human re-triggering:
--   mode  = 'sandbox' (free practice) | 'ranked' (real stakes, inside guardrails)
--   bid   = ranked stake per match (ignored for sandbox)
--   games = sandbox games to rotate through (empty ⇒ default)
-- owner_public_id is captured from the token at write time so the reconciler can
-- enqueue ranked matches under the right owner without a second lookup.
CREATE TABLE IF NOT EXISTS agent_autoplay (
    agent_id        BIGINT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    owner_public_id TEXT        NOT NULL,
    enabled         BOOLEAN     NOT NULL DEFAULT false,
    mode            TEXT        NOT NULL DEFAULT 'sandbox',
    bid             BIGINT      NOT NULL DEFAULT 0,
    games           TEXT[]      NOT NULL DEFAULT '{}',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The reconciler lists only enabled agents each tick — keep that scan cheap.
CREATE INDEX IF NOT EXISTS idx_agent_autoplay_enabled ON agent_autoplay (enabled) WHERE enabled;
