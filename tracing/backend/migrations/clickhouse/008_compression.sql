-- Column compression. Every table was on ClickHouse's default LZ4, which is tuned for
-- decompression speed rather than size, and the storage bill is the point of this file.
--
-- MEASURED, not assumed. Two tables were built from the same 154,097 live events_raw rows
-- with identical schemas, one on the default codec and one on the codecs below:
--
--     default (LZ4)        27.75 MiB
--     ZSTD as below        13.70 MiB      2.03x smaller
--
-- The surprise in the per-column numbers is worth recording, because it is the opposite of
-- where you would look first. payload_json is the big column and LZ4 already got 11x on it,
-- since JSON is repetitive. The IDs are the problem: event_id, trace_id and ingestion_id are
-- high-entropy hex, LZ4 achieved a ratio of 1.0-1.1 on them, and together they occupied MORE
-- compressed space than every payload in the table. The firehose was mostly paying to store
-- identifiers.
--
-- Codec choice follows that split:
--   ZSTD(3) for payloads   — JSON has structure worth the extra effort; level 3 is well
--                            inside the range where ClickHouse's own reads stay fast.
--   ZSTD(1) for identifiers — nothing to model, so level 1 buys the entropy coding without
--                            paying for a search that random hex will not reward.
--
-- Deliberately codecs ONLY, and no type changes. LowCardinality on the enum-ish columns was
-- measured too and contributed almost nothing here (step_name already compressed 91x under
-- LZ4), so it would have been a column rewrite and a client-visible type change bought
-- nothing. A storage codec is invisible above the storage layer: no query, no driver and no
-- reader behaves differently, which is what makes this safe to apply to a live telemetry
-- store.
--
-- Applies to parts written from now on. ClickHouse recompresses existing parts during normal
-- background merges, so the saving arrives gradually; no OPTIMIZE FINAL is issued here
-- because rewriting every part at once on a live cluster costs more than waiting.

-- The firehose. Largest table and the one that grows fastest.
ALTER TABLE pyyol_lens.events_raw MODIFY COLUMN payload_json String CODEC(ZSTD(3));
ALTER TABLE pyyol_lens.events_raw MODIFY COLUMN event_id     String CODEC(ZSTD(1));
ALTER TABLE pyyol_lens.events_raw MODIFY COLUMN trace_id     String CODEC(ZSTD(1));
ALTER TABLE pyyol_lens.events_raw MODIFY COLUMN span_id      String CODEC(ZSTD(1));
ALTER TABLE pyyol_lens.events_raw MODIFY COLUMN ingestion_id String CODEC(ZSTD(1));
ALTER TABLE pyyol_lens.events_raw MODIFY COLUMN payload_ref  String CODEC(ZSTD(1));

-- Derived per-event rows. Same identifier problem, 842k rows of it.
ALTER TABLE pyyol_lens.events MODIFY COLUMN event_id    String CODEC(ZSTD(1));
ALTER TABLE pyyol_lens.events MODIFY COLUMN trace_id    String CODEC(ZSTD(1));
ALTER TABLE pyyol_lens.events MODIFY COLUMN span_id     String CODEC(ZSTD(1));
ALTER TABLE pyyol_lens.events MODIFY COLUMN payload_ref String CODEC(ZSTD(1));

ALTER TABLE pyyol_lens.traces MODIFY COLUMN trace_id String CODEC(ZSTD(1));
ALTER TABLE pyyol_lens.spans  MODIFY COLUMN trace_id String CODEC(ZSTD(1));
ALTER TABLE pyyol_lens.spans  MODIFY COLUMN span_id  String CODEC(ZSTD(1));

-- Dedupe bookkeeping: one high-entropy id per event ever accepted, and NOTHING else of
-- substance. It is the purest instance of the problem this file fixes — 14.55 MiB of
-- almost nothing but identifiers.
ALTER TABLE pyyol_lens.processed_events MODIFY COLUMN event_id String CODEC(ZSTD(1));
