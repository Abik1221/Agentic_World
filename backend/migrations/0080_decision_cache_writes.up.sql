-- Prompt-cache WRITE tokens on the SDK-reported decision log.
--
-- agent_match_decisions already had cached_tokens, which recorded only cache READS. The
-- two are separately billed and point in opposite directions — on Anthropic a read is
-- 0.1x input and a write is 1.25x — so one column could not represent both, and the
-- write half was recorded nowhere. The observed-call table (agent_model_calls, 0079)
-- already splits them; this brings the self-reported path to the same shape so a cost
-- means the same thing whichever tier produced it.
--
-- Backfill is deliberately absent. Historical rows genuinely do not know their cache
-- writes, and defaulting them to anything other than 0 would invent a number; 0 is
-- also what they were already being priced at, so nothing changes retroactively.
ALTER TABLE agent_match_decisions
    ADD COLUMN IF NOT EXISTS cached_write_tokens integer NOT NULL DEFAULT 0;
