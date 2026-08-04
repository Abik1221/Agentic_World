-- 0071_agent_key_labels — name each agent key after the machine holding it, so a
-- second machine stops silently killing the first.
--
-- THE BUG. There was exactly one live key per agent, by construction: minting went
-- through InsertKeyRotating, which revoked EVERY live key for the agent inside the
-- same transaction. Every credential-issuing path shares that one slot, and all of
-- them mint unconditionally:
--
--   * `pyyol login` mints on every login (sdk/python/pyyol/cli.py, _login_and_save)
--   * /cli-login mints on every device authorization (Pyyol_client/app/cli-login)
--   * the dashboard "Rotate key" button (Pyyol_client/components/console/ApiKeysCard)
--
-- So the documented always-on deployment flow — rotate in the dashboard, copy the
-- key, inject it as PYYOL_TOKEN (internal/docs/content/sdk/deployment.md) — revoked
-- the laptop's key as a side effect. And the next `pyyol login` on the laptop revoked
-- the container's. The loser found out at the next reconnect, as a bare
-- "register token rejected" from the gateway, with nothing anywhere naming rotation
-- as the cause. Two devices could never both be live, and nothing in the product
-- said so.
--
-- WHY LABELS AND NOT JUST "ALLOW MANY". Unlimited unnamed keys is the other failure:
-- `pyyol login` runs many times on one machine, so the list becomes a pile of
-- indistinguishable sk_arena_… prefixes and revoking a leaked key means guessing.
-- A label is what makes the list actionable — you revoke "ci-runner", not a prefix —
-- and it is what lets re-login on the SAME machine replace its own key instead of
-- adding one. That replacement is the reason for the partial unique index below
-- rather than a service-level check: two logins racing on one machine would both
-- pass a SELECT-then-INSERT.
--
-- EXISTING KEYS KEEP label = '' (rendered as unnamed, still revocable). They are
-- live credentials on machines we cannot identify from here; inventing a name for
-- them would be a guess displayed as fact, and the empty string is the honest
-- version. Only '' is exempt from the uniqueness rule, so the legacy rows never
-- collide with each other.
--
-- No key is revoked, created, or re-hashed by this migration: it is additive, and a
-- deploy of it must not sign anyone out.

BEGIN;

ALTER TABLE agent_keys
    ADD COLUMN IF NOT EXISTS label TEXT NOT NULL DEFAULT '';

-- One live key per (agent, label): re-issuing for the same device replaces that
-- device's key and leaves every other device alone. '' is excluded so pre-0071 keys
-- (and any future deliberately-unnamed key) are not forced into one slot.
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_keys_agent_label_live
    ON agent_keys(agent_id, label)
    WHERE revoked_at IS NULL AND label <> '';

COMMIT;
