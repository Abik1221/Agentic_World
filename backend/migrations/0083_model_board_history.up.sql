-- Per-day history of the model board, so a model's rating can be shown as a series rather than
-- only as today's number.
--
-- WHY THIS EXISTS NOW, BEFORE ANYTHING READS IT. The board computes a CURRENT snapshot and keeps
-- nothing. History is the one thing that cannot be backfilled: a rating is a fit over the matches
-- that existed at a moment, and that moment does not come again. Every day without this table is a
-- day of the series permanently missing, so it is created before the chart that will read it.
--
-- ONE ROW PER (day, model), not per refresh. The worker refits every ten minutes; storing each
-- would be ~144 rows per model per day describing the same day, and a chart would then have to
-- decide which one the day "was". Upserting on the day means the row holds that day's latest fit,
-- which is the most complete one — it saw the most matches.
--
-- The interval bounds are stored alongside the point estimate because they are the honest content
-- of the series. A rating line without its uncertainty invites reading a 4-point move as a change
-- when the interval is 40 points wide, and that misreading is the whole reason arena boards publish
-- a ± at all.
CREATE TABLE IF NOT EXISTS model_board_history (
    -- The DAY the fit describes, in UTC. Date rather than timestamp: the grain is the chart's
    -- grain, and storing a timestamp would invite two rows for one day at a boundary.
    day              date        NOT NULL,
    model            text        NOT NULL,

    -- The published scale, so a reader and the series agree without re-deriving anything.
    elo              double precision NOT NULL,
    elo_low          double precision NOT NULL,
    elo_high         double precision NOT NULL,
    -- theta is kept too: elo is an affine presentation of it, and a future rescale must not
    -- silently rewrite history that was published on the old scale.
    theta            double precision NOT NULL,

    rank             integer     NOT NULL DEFAULT 0,
    -- Share of bootstrap replicates in which the model held that rank. A rank held 60% of the time
    -- and one held 99% of the time are different claims, and a series that plots only the rank
    -- hides the difference.
    rank_stability   double precision NOT NULL DEFAULT 0,

    comparisons      integer     NOT NULL DEFAULT 0,
    wins             integer     NOT NULL DEFAULT 0,
    losses           integer     NOT NULL DEFAULT 0,
    draws            integer     NOT NULL DEFAULT 0,
    harnesses        integer     NOT NULL DEFAULT 0,
    -- Share of evidence that came from a harness which also ran another model. Carried into the
    -- series because a rating that rose while separability fell is a statement about one developer,
    -- not about the model.
    separability     double precision NOT NULL DEFAULT 0,
    provisional      boolean     NOT NULL DEFAULT false,

    -- Provenance for the row itself.
    window_days      integer     NOT NULL DEFAULT 0,
    computed_at      timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (day, model)
);

-- The chart's query: one model's series over a date range.
CREATE INDEX IF NOT EXISTS idx_model_board_history_model_day
    ON model_board_history (model, day DESC);
