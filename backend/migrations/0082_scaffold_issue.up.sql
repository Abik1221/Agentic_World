-- Why a decision carries no scaffold fingerprint.
--
-- A short CODE, not prose: the reason repeats on every decision of every non-qualifying
-- agent, so storing the sentence would duplicate it thousands of times per match. The
-- wording lives in the SDK and the UI, which means it can be improved without a migration.
--
-- The code exists so an ineligible agent is TOLD why. The commonest value is
-- 'no_system_prompt': the developer's instructions sit in the user turn mixed with the game
-- state, so the harness cannot be identified and the agent is excluded from paired model
-- comparison. That is a one-line fix if you know about it and a support ticket if you don't.
--
-- It also makes the population answerable in aggregate — "how many agents are ineligible,
-- and why" — which is the difference between knowing the model board's coverage and guessing.
ALTER TABLE agent_match_decisions
    ADD COLUMN IF NOT EXISTS scaffold_issue text NOT NULL DEFAULT '';
