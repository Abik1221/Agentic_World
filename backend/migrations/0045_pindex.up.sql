-- 0045_pindex — the P-Index (Pyyol Index) developer-reputation layer.
--
-- P-Index is a per-season COMPOSITE score aggregated to the DEVELOPER (user),
-- distinct from per-agent per-arena ratings. It blends four independently-weighted
-- dimensions (Arena 50% / Consistency 20% / Difficulty 15% / Activity 15%). Weights
-- and constants live in pindex_config (VERSIONED) so any historical P-Index is
-- reproducible from its recorded config_version + inputs. The design is modular:
-- future dimensions (reasoning, planning, …) plug in as new config keys + engine
-- dimensions without altering these tables.

-- Versioned, independently-configurable scoring parameters. Exactly one row is
-- active; recompute always stamps the version it used.
CREATE TABLE pindex_config (
    version    INT PRIMARY KEY,
    params     JSONB NOT NULL,
    active     BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_pindex_config_active ON pindex_config (active) WHERE active;

-- Beta config (version 1). Weights sum to 1.0; every score is on a 0–1000 scale.
INSERT INTO pindex_config (version, params, active) VALUES (1, '{
  "scale": 1000,
  "weights": {"arena": 0.50, "consistency": 0.20, "difficulty": 0.15, "activity": 0.15},
  "norm": {"low": 1000, "high": 2500},
  "arena": {"min_matches": 5, "match_cap": 50},
  "consistency": {"rd_low": 50, "rd_high": 350, "sigma_low": 1.0, "sigma_high": 8.333333, "min_matches": 10},
  "activity": {"match_k": 20, "diversity_target": 3, "recency_days": 7}
}', true);

-- One row per (developer, season): the latest computed P-Index + per-dimension
-- contributions + rank/percentile + running peaks. Historical seasons are never
-- deleted (a new season adds a new row).
CREATE TABLE developer_pindex (
    user_id        BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    season         INT    NOT NULL,
    p_index        NUMERIC(7,2) NOT NULL DEFAULT 0,
    arena_c        NUMERIC(7,2) NOT NULL DEFAULT 0,  -- dimension sub-scores (0–1000)
    consistency_c  NUMERIC(7,2) NOT NULL DEFAULT 0,
    difficulty_c   NUMERIC(7,2) NOT NULL DEFAULT 0,
    activity_c     NUMERIC(7,2) NOT NULL DEFAULT 0,
    global_rank    INT    NOT NULL DEFAULT 0,        -- 1-based; 0 = unranked
    percentile     NUMERIC(5,2) NOT NULL DEFAULT 0,  -- top X% (lower is better)
    highest_pindex NUMERIC(7,2) NOT NULL DEFAULT 0,
    best_rank      INT    NOT NULL DEFAULT 0,
    config_version INT    NOT NULL,
    computed_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, season)
);
CREATE INDEX idx_developer_pindex_board ON developer_pindex (season, p_index DESC, user_id);

-- Append-only audit + transparency trail: one row per recompute, with the full
-- per-dimension breakdown, the delta from the previous value, the config version,
-- and a hash of the inputs (so a recompute can prove reproducibility).
CREATE TABLE developer_pindex_history (
    id             BIGSERIAL PRIMARY KEY,
    user_id        BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    season         INT    NOT NULL,
    p_index        NUMERIC(7,2) NOT NULL,
    breakdown      JSONB  NOT NULL,
    delta          NUMERIC(7,2) NOT NULL DEFAULT 0,
    config_version INT    NOT NULL,
    inputs_hash    TEXT   NOT NULL,
    computed_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_developer_pindex_history ON developer_pindex_history (user_id, computed_at DESC);

-- Dirty set: rating.updated marks the affected developers here; the recompute worker
-- drains it (FOR UPDATE SKIP LOCKED) so recompute is async + horizontally safe and
-- never blocks the match/rating hot path.
CREATE TABLE pindex_dirty (
    user_id     BIGINT NOT NULL PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    enqueued_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
