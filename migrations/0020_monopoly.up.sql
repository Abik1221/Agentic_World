-- 0017_monopoly — AI-vs-AI Monopoly-inspired game: matches, teams, agents,
-- property ownership, and an append-only event log for replay + SSE backlog.
--
-- Design doc: 40-tile board, teams accumulate net worth via property ownership,
-- building, rent and trades. Winner = last solvent team, or highest net worth
-- when the match timer expires. Engine is deterministic (board_seed) and
-- server-validated; agents act via REST/WebSocket.

BEGIN;

CREATE TABLE IF NOT EXISTS monopoly_matches (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id      TEXT NOT NULL UNIQUE,
    status         TEXT NOT NULL DEFAULT 'lobby',   -- lobby|live|settled|cancelled
    mode           TEXT NOT NULL DEFAULT '2v2',     -- 1v1|2v2|3v3|4v4
    board_seed     BIGINT NOT NULL,                 -- deterministic engine seed
    round          INT  NOT NULL DEFAULT 0,
    phase          TEXT NOT NULL DEFAULT 'pregame',
    entry_fee      BIGINT NOT NULL DEFAULT 0,       -- coins locked per agent
    timer_ends_at  TIMESTAMPTZ,
    winner_team_id BIGINT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    settled_at     TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS monopoly_teams (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    match_id   BIGINT NOT NULL REFERENCES monopoly_matches(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    color      TEXT NOT NULL,
    cash       BIGINT NOT NULL DEFAULT 1500,
    position   INT    NOT NULL DEFAULT 0,
    in_jail    BOOLEAN NOT NULL DEFAULT false,
    net_worth  BIGINT NOT NULL DEFAULT 1500,
    bankrupt   BOOLEAN NOT NULL DEFAULT false,
    seat       INT    NOT NULL                       -- turn order 0..n
);
CREATE INDEX IF NOT EXISTS idx_monopoly_teams_match ON monopoly_teams(match_id);

CREATE TABLE IF NOT EXISTS monopoly_team_agents (
    id        BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id   BIGINT NOT NULL REFERENCES monopoly_teams(id) ON DELETE CASCADE,
    agent_id  BIGINT NOT NULL REFERENCES agents(id),
    UNIQUE (team_id, agent_id)
);

CREATE TABLE IF NOT EXISTS monopoly_properties (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    match_id      BIGINT NOT NULL REFERENCES monopoly_matches(id) ON DELETE CASCADE,
    tile_id       INT    NOT NULL,                    -- 0..39
    owner_team_id BIGINT REFERENCES monopoly_teams(id),
    houses        INT    NOT NULL DEFAULT 0,
    mortgaged     BOOLEAN NOT NULL DEFAULT false,
    UNIQUE (match_id, tile_id)
);

CREATE TABLE IF NOT EXISTS monopoly_events (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    match_id   BIGINT NOT NULL REFERENCES monopoly_matches(id) ON DELETE CASCADE,
    seq        BIGINT NOT NULL,                       -- monotonic per match
    team_id    BIGINT REFERENCES monopoly_teams(id),
    action     TEXT NOT NULL,                         -- ROLL_DICE|BUY_PROPERTY|...
    detail     JSONB NOT NULL DEFAULT '{}',
    kind       TEXT NOT NULL,                         -- Buy|Rent|Build|Trade|Tax|Move|Card|Jail
    round      INT  NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (match_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_monopoly_events_match ON monopoly_events(match_id, seq);

COMMIT;
