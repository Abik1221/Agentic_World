BEGIN;

ALTER TABLE agent_manifests DROP COLUMN IF EXISTS endpoint_auth_token_enc;

COMMIT;
