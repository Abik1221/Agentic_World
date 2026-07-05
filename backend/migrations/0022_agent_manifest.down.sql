-- Reverse 0019_agent_manifest. Drop the pointer first (it references the tables),
-- then the tables in dependency order.

BEGIN;

ALTER TABLE agents DROP COLUMN IF EXISTS active_manifest_public_id;

DROP TABLE IF EXISTS agent_endpoint_verifications;
DROP TABLE IF EXISTS agent_manifest_games;
DROP TABLE IF EXISTS agent_manifests;

COMMIT;
