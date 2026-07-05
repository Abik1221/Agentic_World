-- 0026_seasons — record each completed season's finalisation (the "roll").
--
-- Seasons themselves are fixed date-derived windows (see internal/rating), and
-- per-season standings already persist in `ratings` (keyed by agent+season), so
-- history is queryable without a snapshot. This table records the one-time moment
-- a completed season is finalised, so the roll (and its season.rolled event +
-- champion badge) happens exactly once per season, idempotently.
BEGIN;

CREATE TABLE season_rolls (
    season                    INT PRIMARY KEY,
    champion_agent_public_id  TEXT,               -- NULL if the season had no matches
    rolled_at                 TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMIT;
