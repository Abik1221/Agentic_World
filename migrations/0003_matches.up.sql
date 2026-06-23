-- 0003_matches — match lifecycle: matches, the two seats, and the append-only
-- event log (the replay). See docs/architecture/data-model.md + game-engine.md.

CREATE TABLE matches (
    id                    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id             TEXT NOT NULL UNIQUE,
    game                  TEXT NOT NULL DEFAULT 'goofspiel',
    status                TEXT NOT NULL DEFAULT 'waiting',   -- waiting|active|finished|aborted
    bid                   BIGINT NOT NULL,
    rake_pct              INT NOT NULL DEFAULT 5,
    total_rounds          INT NOT NULL DEFAULT 13,
    engine_version        TEXT NOT NULL,
    prize_seed_commit     TEXT NOT NULL,                     -- sha256(seed), public, set at creation
    prize_seed            BYTEA,                             -- secret until finished; only then exposed via replay
    fairness_mode         TEXT NOT NULL DEFAULT 'shuffled',
    state                 JSONB,                             -- engine State snapshot (derived cache; events are authoritative)
    round_deadline        TIMESTAMPTZ,                       -- current move-window deadline (drives the timeout sweeper)
    creator_owner_user_id BIGINT NOT NULL REFERENCES users(id),  -- enables same-owner pairing block
    winner_agent_id       BIGINT REFERENCES agents(id),
    result_signature      TEXT,
    replay_hash           TEXT,
    started_at            TIMESTAMPTZ,
    finished_at           TIMESTAMPTZ,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_matches_lobby ON matches (game, bid) WHERE status = 'waiting';
CREATE INDEX idx_matches_sweep ON matches (round_deadline) WHERE status = 'active';
CREATE INDEX idx_matches_created ON matches (created_at DESC);

CREATE TABLE match_players (
    match_id      BIGINT NOT NULL REFERENCES matches(id),
    agent_id      BIGINT NOT NULL REFERENCES agents(id),
    owner_user_id BIGINT NOT NULL REFERENCES users(id),
    seat          INT NOT NULL,
    final_score   INT,
    coins_delta   BIGINT,
    PRIMARY KEY (match_id, agent_id),
    UNIQUE (match_id, seat)
);
CREATE INDEX idx_match_players_agent ON match_players (agent_id);

CREATE TABLE match_events (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    match_id   BIGINT NOT NULL REFERENCES matches(id),
    seq        INT NOT NULL,
    type       TEXT NOT NULL,
    payload    JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (match_id, seq)   -- gap-free ordering + no duplicates, enforced by the DB
);

-- Now that matches exists, anchor timing samples to it (nullable: pre-match samples allowed).
ALTER TABLE agent_timing_samples
    ADD CONSTRAINT fk_timing_match FOREIGN KEY (match_id) REFERENCES matches(id);
