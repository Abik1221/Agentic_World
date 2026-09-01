-- 0093_model_board_history_board — let two boards keep their own series.
--
-- model_board_history was keyed (day, model), which is correct for one board and silently
-- wrong for two. The platform harness benchmark runs the SAME fit as the developer board —
-- same Build, same Bradley-Terry estimator, same intervals — over a different set of
-- matches. Both write a daily row per model.
--
-- With the old key, on any day both boards measured `google/gemma-4-26b-a4b-it:free`, the
-- second writer's upsert would overwrite the first. No error, no conflict, no way to notice
-- afterwards: the developer board's published history would silently become the harness
-- board's numbers, or the reverse depending on refresh order. Two independent measurements
-- would quietly become one.
--
-- The discriminator makes them independent, which is the whole point of running the harness
-- as a parallel path rather than as extra rows in the existing one.
ALTER TABLE model_board_history
    ADD COLUMN IF NOT EXISTS board TEXT NOT NULL DEFAULT 'developer';

COMMENT ON COLUMN model_board_history.board IS
    'Which board produced this row: developer (fitted from developers'' own matches) or '
    'harness (fitted from the platform''s own benchmark runs). Same algorithm, separate data; '
    'neither may overwrite the other.';

-- Repoint the key. Existing rows default to 'developer', which is what every row in this
-- table is today — the harness board does not exist yet, so the backfill is exact rather
-- than an assumption.
ALTER TABLE model_board_history DROP CONSTRAINT IF EXISTS model_board_history_pkey;
ALTER TABLE model_board_history ADD PRIMARY KEY (board, day, model);

-- Series reads are always scoped to one board and one model, walking days.
CREATE INDEX IF NOT EXISTS idx_model_board_history_board_model
    ON model_board_history (board, model, day DESC);
