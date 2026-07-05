-- 0009_tournaments — the funded freeroll (hero tournament): a sponsor-funded
-- prize pool, free eligibility-gated entry, and a single-champion payout that
-- flows through the ledger like any other settlement.

CREATE TABLE tournaments (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id       TEXT NOT NULL UNIQUE,            -- trn_xxx
    name            TEXT NOT NULL,
    sponsor         TEXT,
    prize_pool      BIGINT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'open',    -- open|running|finished
    winner_agent_id BIGINT REFERENCES agents(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    CONSTRAINT tournaments_pool_positive CHECK (prize_pool > 0)
);
CREATE INDEX idx_tournaments_status ON tournaments (status);

CREATE TABLE tournament_entries (
    tournament_id BIGINT NOT NULL REFERENCES tournaments(id),
    agent_id      BIGINT NOT NULL REFERENCES agents(id),
    joined_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tournament_id, agent_id)
);
