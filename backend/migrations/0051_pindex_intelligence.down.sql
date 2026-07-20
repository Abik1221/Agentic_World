-- 0051_pindex_intelligence (down)
DELETE FROM pindex_config WHERE version = 2;
ALTER TABLE developer_pindex DROP COLUMN IF EXISTS intelligence_c;
DROP TABLE IF EXISTS agent_match_benchmark;
