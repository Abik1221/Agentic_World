-- Reversing this restores the SHAPE, not the data.
--
-- The Monopoly tables are recreated from 0020/0047 so the schema round-trips and a
-- down-then-up cannot fail on a missing table. What was in them is gone: a drop is not
-- recoverable from a migration, and pretending otherwise by leaving this empty would be
-- worse — a down that silently does nothing looks like it worked.
--
-- The aborted matches are NOT reopened. Setting them back to 'active' would put dead
-- tables back in front of the sweeper and re-block their agents' match slots, which is
-- the bug this migration exists to end. They stay closed.
--
-- Keep in step with 0020_monopoly.up.sql / 0047_monopoly_lobby.up.sql if those are ever
-- amended.

CREATE TABLE IF NOT EXISTS monopoly_matches (
    id           BIGSERIAL PRIMARY KEY,
    public_id    TEXT UNIQUE NOT NULL,
    status       TEXT NOT NULL,
    state        JSONB,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS monopoly_teams (
    id         BIGSERIAL PRIMARY KEY,
    match_id   BIGINT NOT NULL REFERENCES monopoly_matches(id) ON DELETE CASCADE,
    seat       INT NOT NULL,
    name       TEXT
);

CREATE TABLE IF NOT EXISTS monopoly_team_agents (
    id        BIGSERIAL PRIMARY KEY,
    team_id   BIGINT NOT NULL REFERENCES monopoly_teams(id) ON DELETE CASCADE,
    agent_id  BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS monopoly_properties (
    id         BIGSERIAL PRIMARY KEY,
    match_id   BIGINT NOT NULL REFERENCES monopoly_matches(id) ON DELETE CASCADE,
    position   INT NOT NULL,
    owner_seat INT,
    houses     INT NOT NULL DEFAULT 0,
    mortgaged  BOOLEAN NOT NULL DEFAULT false
);

CREATE TABLE IF NOT EXISTS monopoly_events (
    id         BIGSERIAL PRIMARY KEY,
    match_id   BIGINT NOT NULL REFERENCES monopoly_matches(id) ON DELETE CASCADE,
    seq        BIGINT NOT NULL,
    type       TEXT NOT NULL,
    payload    JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The stake tiers 0038 seeded. Restoring these makes the row set match the schema again.
INSERT INTO game_stakes (game, tier_key, label, coins, ordering)
VALUES ('monopoly', 'low', 'Low', 100, 0),
       ('monopoly', 'mid', 'Mid', 500, 1),
       ('monopoly', 'high', 'High', 2000, 2)
ON CONFLICT DO NOTHING;
