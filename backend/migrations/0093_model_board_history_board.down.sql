-- Collapsing the key back to (day, model) cannot be done while two boards have rows for the
-- same day and model — one of them would have to be destroyed. Drop the harness rows first,
-- deliberately, rather than letting the constraint pick a winner.
DELETE FROM model_board_history WHERE board <> 'developer';
DROP INDEX IF EXISTS idx_model_board_history_board_model;
ALTER TABLE model_board_history DROP CONSTRAINT IF EXISTS model_board_history_pkey;
ALTER TABLE model_board_history ADD PRIMARY KEY (day, model);
ALTER TABLE model_board_history DROP COLUMN IF EXISTS board;
