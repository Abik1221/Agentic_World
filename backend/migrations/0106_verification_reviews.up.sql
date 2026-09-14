-- A review for "flagged for review".
--
-- CheckEligibility can refuse an agent with reason 'high_human_likelihood' — the timing
-- detector deciding a human is playing by hand. The message says "flagged for review"
-- and, until this table, there was no reviewer and no review: no admin endpoint cleared
-- it, nothing anywhere deleted from agent_timing_samples, and the verdict is computed
-- from an agent's OWN recent samples.
--
-- That made the refusal terminal. Samples are only produced by playing; a flagged agent
-- may not play — not ranked, not a private room, and not sandbox either (CreateSandbox
-- checks the same gate) — so it can never produce the faster samples that would clear
-- it. The other documented escape, a completion-binding proof outranking the timing
-- guess, needs 90% of the agent's WHOLE history bound, which an agent with unbound
-- history cannot reach. Both doors are closed at once.
--
-- A false positive is cheap to cause: a provider outage, a rate limit, or an unset API
-- key makes an honest agent answer slowly and erratically for twenty turns.
--
-- WHY A CLEAN SLATE RATHER THAN AN EXEMPTION. A flag that reads "reviewed, ignore this
-- agent" would be an exemption cut into a fraud control, and this codebase has a rule
-- against exactly that. So a review does not exempt anything: it marks an instant, and
-- the detector afterwards considers only samples NEWER than it. The agent is judged on
-- what it does from here. An agent that really is a human at a keyboard is flagged again
-- twenty moves later, by the same rule, with no special case in the detector.
--
-- The samples are NOT deleted. Erasing the evidence would destroy the record of why the
-- agent was flagged; the review is appended beside it.
CREATE TABLE IF NOT EXISTS agent_verification_reviews (
    id           BIGSERIAL PRIMARY KEY,
    agent_id     BIGINT      NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    -- Who decided. Free text rather than a users FK: a review may be made by an operator
    -- acting through the platform token, who has no row in users.
    reviewed_by  TEXT        NOT NULL,
    -- Why. Required by the handler, because a clean slate with no stated reason is
    -- indistinguishable from an accident six months later.
    reason       TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The only read: the newest review for one agent, on every eligibility check.
CREATE INDEX IF NOT EXISTS idx_agent_verification_reviews_agent_created
    ON agent_verification_reviews (agent_id, created_at DESC);
