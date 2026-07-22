-- Rotating refresh tokens for dashboard (user) sessions. The access token is a
-- short-lived JWT; this table backs a long-lived, single-use, rotating refresh
-- token with a sliding idle window + family-based reuse (theft) detection.
CREATE TABLE IF NOT EXISTS refresh_tokens (
    id              TEXT PRIMARY KEY,                 -- public token id (rt_...)
    user_public_id  TEXT NOT NULL,
    family_id       TEXT NOT NULL,                    -- rotation lineage (revoke-on-reuse)
    token_hash      TEXT NOT NULL,                    -- sha256(secret); the secret is never stored
    expires_at      TIMESTAMPTZ NOT NULL,             -- sliding idle expiry
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    used_at         TIMESTAMPTZ,                      -- set once when rotated (single-use)
    revoked_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_family ON refresh_tokens (family_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user ON refresh_tokens (user_public_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_expires ON refresh_tokens (expires_at);
