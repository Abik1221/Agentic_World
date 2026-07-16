-- 0043_match_rating_changes — a permanent, per-match rating snapshot.
--
-- Written in the SAME transaction as the rating update (store.RatingRepo.ApplyMatch),
-- one row per participating agent, recording the rating before/after/delta and the
-- agent's finishing rank in that match. This powers two things the P-Index needs:
--   1. match history that shows exactly how each match moved a rating ("why it
--      changed"), and
--   2. the Difficulty dimension, which reads the OPPONENT rating faced AT match time
--      (rating_before) — impossible to reconstruct once the live rating moves on.
--
-- Append-only; keyed (match_id, agent_id) so a finalize retry is a harmless no-op
-- (ApplyMatch is already idempotent via rating_updates, but the PK is belt-and-braces).

CREATE TABLE match_rating_changes (
    match_id      BIGINT NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    agent_id      BIGINT NOT NULL REFERENCES agents(id),
    game          TEXT   NOT NULL,
    season        INT    NOT NULL,
    rating_before INT    NOT NULL,
    rating_after  INT    NOT NULL,
    rating_delta  INT    NOT NULL,
    rank_in_match INT    NOT NULL,  -- 1 = best; equal ranks = a tie between agents
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (match_id, agent_id)
);

-- Read paths: an agent's recent rating changes (match history + difficulty inputs).
CREATE INDEX idx_mrc_agent ON match_rating_changes (agent_id, created_at DESC);
