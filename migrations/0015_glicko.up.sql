-- 0014_glicko — move ratings from plain Elo to Glicko-2. Each agent now carries a
-- rating deviation (RD, uncertainty) and a volatility alongside the displayed
-- rating. New agents start maximally uncertain (RD 350) so their rating moves fast
-- and wide until evidence accumulates. The displayed baseline becomes 1500 (the
-- Glicko-2 centre); existing rows keep their current values.

ALTER TABLE ratings
    ADD COLUMN rd  DOUBLE PRECISION NOT NULL DEFAULT 350,
    ADD COLUMN vol DOUBLE PRECISION NOT NULL DEFAULT 0.06;

ALTER TABLE ratings ALTER COLUMN elo SET DEFAULT 1500;
