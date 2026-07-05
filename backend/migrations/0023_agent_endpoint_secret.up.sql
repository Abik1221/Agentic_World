-- 0020_agent_endpoint_secret — encrypted endpoint credential.
--
-- The platform authenticates to a developer's endpoint with a bearer token the
-- developer supplies. That token is a SECRET: it is never part of the manifest
-- metadata (which may be displayed), never returned by any read API, and stored
-- only as AES-256-GCM ciphertext (see internal/secretbox). This column holds that
-- sealed blob per manifest version (the endpoint URL lives on the manifest).

BEGIN;

ALTER TABLE agent_manifests
    ADD COLUMN endpoint_auth_token_enc BYTEA;

COMMIT;
