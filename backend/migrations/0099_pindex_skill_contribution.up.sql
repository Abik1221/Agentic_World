-- Persist the Decision Quality contribution alongside the other five.
--
-- The dimension has been computed and returned by the engine since it shipped, but there was
-- nowhere to put its contribution: developer_pindex carried arena_c, consistency_c,
-- difficulty_c, activity_c and intelligence_c and simply dropped the sixth. So even an
-- operator who set a weight deliberately would have had the number vanish on write, and the
-- transparency surface could never show what Decision Quality contributed.
--
-- Defaults to 0, which is the value every existing row already implies: the weight is 0, so
-- the contribution is 0, and no historical P-Index changes.
ALTER TABLE developer_pindex ADD COLUMN IF NOT EXISTS skill_c double precision NOT NULL DEFAULT 0;
ALTER TABLE developer_pindex_history ADD COLUMN IF NOT EXISTS skill_c double precision NOT NULL DEFAULT 0;
