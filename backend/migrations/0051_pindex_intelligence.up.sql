-- 0051_pindex_intelligence — feed the P-Index Intelligence dimension.
--
-- Persists per-(match, agent) decision-quality raw counts projected from the
-- match.benchmark outbox fact, and adds the intelligence sub-score column + a
-- v2 config that activates the Intelligence dimension.

-- Per-(match, agent) benchmark counts. Keyed by the match PUBLIC id so it works
-- across all games; the P-Index Inputs query joins ranked matches through
-- matches/match_rating_changes for season + owner + fraud scoping (non-ranked
-- matches simply don't join and are excluded — correct for reputation).
CREATE TABLE IF NOT EXISTS agent_match_benchmark (
    match_id       TEXT   NOT NULL,
    agent_id       BIGINT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    decisions      INT    NOT NULL DEFAULT 0,
    legal          INT    NOT NULL DEFAULT 0,
    fallbacks      INT    NOT NULL DEFAULT 0,
    latency_sum_ms BIGINT NOT NULL DEFAULT 0,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (match_id, agent_id)
);

-- Intelligence dimension sub-score (0–1000), persisted alongside the others.
ALTER TABLE developer_pindex ADD COLUMN IF NOT EXISTS intelligence_c NUMERIC(7,2) NOT NULL DEFAULT 0;

-- P-Index config v2 — adds the engine-measured Intelligence dimension (legal-rate,
-- reliability, speed), reweighted to still sum to 1.0. Seeded INACTIVE on purpose:
-- activating it reweights every developer's reputation on recompute, so flip it on
-- deliberately after sanity-checking the computed intelligence sub-scores on a live
-- DB:  UPDATE pindex_config SET active=false WHERE version=1;
--      UPDATE pindex_config SET active=true  WHERE version=2;
INSERT INTO pindex_config (version, params, active) VALUES (2, '{
  "scale": 1000,
  "weights": {"arena": 0.45, "consistency": 0.20, "difficulty": 0.10, "activity": 0.10, "intelligence": 0.15},
  "norm": {"low": 1000, "high": 2500},
  "arena": {"min_matches": 5, "match_cap": 50},
  "consistency": {"rd_low": 50, "rd_high": 350, "sigma_low": 1.0, "sigma_high": 8.333333, "min_matches": 10},
  "activity": {"match_k": 20, "diversity_target": 3, "recency_days": 7},
  "intelligence": {"w_legal": 0.4, "w_reliability": 0.4, "w_speed": 0.2, "latency_fast_ms": 500, "latency_slow_ms": 8000, "min_decisions": 200}
}', false)
ON CONFLICT (version) DO NOTHING;
