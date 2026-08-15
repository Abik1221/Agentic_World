-- events_raw gains agent_kind, so PLATFORM benchmark traffic is separable from a
-- developer's own.
--
-- WHY THIS IS NEEDED. The Pyyol harness plays real matches, with real models, through the
-- real gateway. That is deliberate: a benchmark that ran on a private code path would be
-- measuring the private code path, and its numbers would not be comparable to anything a
-- developer's agent does. The consequence is that harness spans arrive through exactly the
-- same ingest as user spans and are, in Lens, indistinguishable from them.
--
-- Two things break without a discriminator, in opposite directions:
--
--   * An operator cannot trace a benchmark run without reading user telemetry to find it.
--   * A developer-facing cost or latency view silently includes calls that were never
--     theirs — the platform's, made on the platform's own keys.
--
-- WHY THIS DISCRIMINATOR. It is the SAME one the model boards already separate on: the
-- agent's kind (`external` = a developer's agent, `harness` = a platform benchmark seat).
-- Reusing it means the trace views and the boards cannot disagree about whose result
-- something is. A Lens-local flag, or an environment split, would be a second definition of
-- the same fact and would drift from the first one.
--
-- EMPTY MEANS UNKNOWN, NOT EXTERNAL, and there is no backfill. Every row written before
-- this ran carries '' and forms its own bucket, exactly as meter_source's rows do after
-- migration 006. Backfilling them to 'external' would assert something the data cannot
-- support: at the time they were written the harness kind did not exist as a label on the
-- span, and some of those rows may well BE harness traffic. A reader wanting history should
-- treat '' as "unknown kind, pre-007" rather than as a developer's traffic.
--
-- LowCardinality because there are three values in the schema's CHECK constraint and no
-- path that creates a fourth. Not added to the sorting key: unlike meter_source in 006 this
-- is not double-counting anything in the rollups — it is a filter on a raw-event lookup, so
-- a data-skipping index is the right tool and a sorting-key change is not warranted.
ALTER TABLE pyyol_lens.events_raw
    ADD COLUMN IF NOT EXISTS agent_kind LowCardinality(String);

-- The filter an operator actually types ("show me only the harness run", "exclude the
-- platform's own traffic"). set(0) rather than a bloom filter because the column has a tiny
-- fixed domain, which is the case set indexes exist for.
ALTER TABLE pyyol_lens.events_raw
    ADD INDEX IF NOT EXISTS idx_agent_kind agent_kind TYPE set(0) GRANULARITY 4;
