-- 0019_agent_manifest — the Agent Manifest metadata contract.
--
-- A manifest is the metadata document a developer submits during registration
-- (see docs/agent-manifest-plan.md). It carries NO source code: agent info,
-- developer/org, supported games, the developer-hosted endpoint URL + auth type,
-- runtime hints, optional (developer-declared, never verified) model info, SDK
-- version, and contact.
--
-- Manifests are VERSIONED and IMMUTABLE: one row per submitted agent_version, so
-- historical versions remain available for replay/auditing. `agents` gains a
-- pointer to the currently active (endpoint-verified) manifest.

BEGIN;

CREATE TABLE agent_manifests (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id          TEXT NOT NULL UNIQUE,
    agent_public_id    TEXT NOT NULL REFERENCES agents(public_id) ON DELETE CASCADE,

    manifest_version   TEXT NOT NULL,                       -- spec version, e.g. "1.0"
    agent_version      TEXT NOT NULL,                       -- developer semver, e.g. "1.0.0"

    name               TEXT NOT NULL,
    description        TEXT,
    visibility         TEXT NOT NULL DEFAULT 'public',      -- public|private

    developer_name     TEXT,
    organization       TEXT,

    endpoint_url       TEXT NOT NULL,
    auth_type          TEXT NOT NULL,                       -- bearer-token|...

    runtime_timeout_ms INT  NOT NULL,
    runtime_max_memory TEXT,                                -- free-form, e.g. "512MB"

    -- Model info is OPTIONAL and DEVELOPER-DECLARED: the platform cannot verify
    -- which model a remote API actually uses, so these are informational only.
    model_provider     TEXT,
    model_name         TEXT,
    model_reasoning    BOOLEAN,

    sdk_language       TEXT,
    sdk_version        TEXT,
    contact_email      TEXT,

    raw_document       TEXT  NOT NULL,                      -- exact submitted doc (audit/replay)
    normalized         JSONB NOT NULL,                      -- parsed + validated form

    status             TEXT NOT NULL DEFAULT 'validated',   -- submitted|validated|verified|rejected
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A given agent_version is submitted at most once (immutability guard).
    CONSTRAINT agent_manifests_version_unique UNIQUE (agent_public_id, agent_version)
);
CREATE INDEX idx_agent_manifests_agent ON agent_manifests(agent_public_id);

-- Supported games, one row per (manifest, game). game_id is validated against the
-- platform's known games (devplatform.GameID) before insert.
CREATE TABLE agent_manifest_games (
    manifest_public_id TEXT NOT NULL REFERENCES agent_manifests(public_id) ON DELETE CASCADE,
    game_id            TEXT NOT NULL,
    PRIMARY KEY (manifest_public_id, game_id)
);

-- One row per endpoint-verification attempt (health + handshake). Populated by
-- the M2 verification service; created now so the schema is complete.
CREATE TABLE agent_endpoint_verifications (
    id                    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    manifest_public_id    TEXT NOT NULL REFERENCES agent_manifests(public_id) ON DELETE CASCADE,
    health_ok             BOOLEAN NOT NULL,
    health_latency_ms     INT,
    handshake_ok          BOOLEAN NOT NULL,
    handshake_sdk_version TEXT,
    handshake_games       JSONB,
    error                 TEXT,
    checked_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_agent_endpoint_verifications_manifest
    ON agent_endpoint_verifications(manifest_public_id);

-- Pointer to the agent's active (endpoint-verified) manifest. NULL until the
-- first manifest is verified. Nullable FK, so ordering here is deliberate:
-- agent_manifests already exists above.
ALTER TABLE agents
    ADD COLUMN active_manifest_public_id TEXT REFERENCES agent_manifests(public_id);

COMMIT;
