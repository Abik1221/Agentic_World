-- 0002_identity — users, agents (with the 7 server-enforced spending limits),
-- scoped API keys, X-claim onboarding tokens, and agent timing samples
-- (verification v1). See docs/architecture/data-model.md.

CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE users (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id          TEXT NOT NULL UNIQUE,
    x_handle           TEXT UNIQUE,
    x_user_id          TEXT UNIQUE,
    email              CITEXT UNIQUE,
    stripe_customer_id TEXT UNIQUE,
    stripe_connect_id  TEXT UNIQUE,
    status             TEXT NOT NULL DEFAULT 'active',  -- active|suspended|banned
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE agents (
    id                     BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id              TEXT NOT NULL UNIQUE,
    owner_user_id          BIGINT NOT NULL REFERENCES users(id),
    name                   TEXT NOT NULL,
    slug                   TEXT NOT NULL UNIQUE,
    description            TEXT,
    framework              TEXT,
    status                 TEXT NOT NULL DEFAULT 'unverified',  -- unverified|active|flagged|banned
    verification_level     TEXT NOT NULL DEFAULT 'new',         -- new|verified_bot|tournament_ready
    -- spending limits (server-enforced; only the OWNER may change these) --
    coin_limit_per_match   BIGINT NOT NULL DEFAULT 100,
    daily_loss_limit       BIGINT NOT NULL DEFAULT 500,
    session_loss_limit     BIGINT NOT NULL DEFAULT 1000,
    min_wallet_balance     BIGINT NOT NULL DEFAULT 50,
    max_concurrent_matches INT    NOT NULL DEFAULT 1,
    cooldown_losses        INT    NOT NULL DEFAULT 3,
    cooldown_seconds       INT    NOT NULL DEFAULT 300,
    max_bid                BIGINT NOT NULL DEFAULT 200,
    auto_join              BOOLEAN NOT NULL DEFAULT false,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agents_limits_positive CHECK (
        coin_limit_per_match > 0 AND max_bid > 0 AND min_wallet_balance >= 0
        AND max_concurrent_matches >= 1 AND cooldown_losses >= 0 AND cooldown_seconds >= 0
    )
);
CREATE INDEX idx_agents_owner  ON agents(owner_user_id);
CREATE INDEX idx_agents_status ON agents(status);

CREATE TABLE agent_keys (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id     BIGINT NOT NULL REFERENCES agents(id),
    key_prefix   TEXT NOT NULL,
    key_hash     TEXT NOT NULL,
    scope        TEXT NOT NULL DEFAULT 'agent',  -- agent (NEVER 'user')
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_keys_scope CHECK (scope = 'agent')
);
-- A given prefix maps to at most one live key (fast, unambiguous lookup).
CREATE UNIQUE INDEX idx_agent_keys_prefix_live ON agent_keys(key_prefix) WHERE revoked_at IS NULL;
CREATE INDEX idx_agent_keys_agent ON agent_keys(agent_id);

CREATE TABLE claims (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    claim_token TEXT NOT NULL UNIQUE,
    agent_name  TEXT NOT NULL,
    description TEXT,
    status      TEXT NOT NULL DEFAULT 'pending',  -- pending|verified|expired
    x_user_id   TEXT,
    x_handle    TEXT,
    agent_id    BIGINT REFERENCES agents(id),     -- set on successful verify
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_claims_status ON claims(status);

CREATE TABLE agent_timing_samples (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id    BIGINT NOT NULL REFERENCES agents(id),
    match_id    BIGINT,                            -- FK added when matches exists (Stage 3)
    response_ms INT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_timing_agent ON agent_timing_samples(agent_id, created_at DESC);
