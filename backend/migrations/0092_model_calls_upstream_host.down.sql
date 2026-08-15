-- Dropping the column loses the provenance of every call recorded since 0092, and there is
-- no way to recompute it — the routing decision that produced it is not stored anywhere
-- else. After this, real and stand-in calls are indistinguishable again.
DROP INDEX IF EXISTS idx_model_calls_bound_host;
ALTER TABLE agent_model_calls DROP COLUMN IF EXISTS upstream_host;
