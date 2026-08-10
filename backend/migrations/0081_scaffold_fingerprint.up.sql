-- Scaffold fingerprints on the decision log.
--
-- A scaffold fingerprint identifies the HARNESS a developer built around a model — system
-- prompt, tools, sampling — with the model deliberately excluded from the hash. It is what
-- makes a paired model comparison possible: within one agent's runs at one fingerprint, the
-- harness is held constant, so a model swap is cleanly attributable to the model.
--
-- Without this, "Claude beats GPT" is confounded with "this developer's harness beats that
-- one", and no amount of statistics separates them after the fact.
--
-- scaffold_unstable records the honest failure case: an agent that puts variable game state
-- in its system prompt gets a new fingerprint every turn and cannot be paired at all. That
-- is a real state with a real consequence, so it is stored rather than smoothed away — the
-- alternative is silently pooling incomparable runs and publishing the result as controlled.
--
-- Both default to "unknown" (empty / false) and are not backfilled: historical decisions
-- genuinely have no fingerprint, and inventing one would fabricate the very control the
-- column exists to provide.
ALTER TABLE agent_match_decisions
    ADD COLUMN IF NOT EXISTS scaffold text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS scaffold_unstable boolean NOT NULL DEFAULT false;

-- Grouping decisions by (agent, scaffold) is the core query of the model board: find every
-- run of one harness, then compare the models used within it. Partial, because rows with no
-- fingerprint can never take part in a pairing and indexing them would only add write cost.
CREATE INDEX IF NOT EXISTS idx_decisions_agent_scaffold
    ON agent_match_decisions (agent_id, scaffold)
    WHERE scaffold <> '';
