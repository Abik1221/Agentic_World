-- rollup_* gains meter_source, because without it the rollups BLEND two different kinds of number.
--
-- WHAT WAS WRONG. Every model call that routes through the Pyyol Gateway produces TWO events: the
-- gateway's server-observed span (meter_source='gateway') and the agent's own self-report
-- (meter_source='sdk'). The processor added both to the rollup, and since meter_source was not part
-- of the sorting key, SummingMergeTree collapsed them into one bucket and summed them.
--
-- Measured on a live match: rollup_hourly reported 66,367 tokens for the 16:00 bucket, which is
-- exactly gateway (33,930) + sdk (32,437). Every routed call was counted twice, and the total mixed
-- an unfakeable measurement with a figure the agent asserts about itself. For the same match the
-- two disagreed by more than 2x on input tokens and named different models.
--
-- WHY A DIMENSION RATHER THAN PICKING ONE METER. Counting only 'gateway' would zero the cost of
-- every deployment that does not run a gateway — a worse regression than the double count. Counting
-- only 'sdk' would mean the verified figure never reaches the rollup, which defeats the point of
-- observing it. Storing both under a key that distinguishes them loses nothing, ends the double
-- count, and forces a consumer to say which number it wants — which is the whole reason
-- meter_source exists as a column in the first place.
--
-- APPENDED to the end of the sorting key, which is the only shape ALTER ... MODIFY ORDER BY
-- permits. The ADD COLUMN and the MODIFY ORDER BY must share ONE statement: ClickHouse only lets a
-- sorting key be extended with columns added by the same ALTER, so splitting them fails with
-- "Existing column meter_source is used in the expression that was added to the sorting key".
--
-- And NO DEFAULT on that column: ClickHouse refuses a sorting-key column carrying a default
-- expression ("Newly added column ... has a default expression, so adding expressions that use it
-- to the sorting key is forbidden"). None is needed — LowCardinality(String) already reads as the
-- empty string for rows written before this ran, which is exactly the "unknown meter" bucket
-- described below.
--
-- Existing rows keep meter_source = '' and therefore form their own bucket. That is the
-- honest outcome rather than a backfill: those totals really were blended, and relabelling them as
-- either meter would assert something the data cannot support. A reader wanting history should
-- treat '' as "unknown meter, pre-006".

ALTER TABLE pyyol_lens.rollup_hourly
    ADD COLUMN IF NOT EXISTS meter_source LowCardinality(String),
    MODIFY ORDER BY (organization_id, project_id, environment, bucket_start, meter_source);

ALTER TABLE pyyol_lens.rollup_daily
    ADD COLUMN IF NOT EXISTS meter_source LowCardinality(String),
    MODIFY ORDER BY (organization_id, project_id, environment, bucket_start, meter_source);

ALTER TABLE pyyol_lens.rollup_monthly
    ADD COLUMN IF NOT EXISTS meter_source LowCardinality(String),
    MODIFY ORDER BY (organization_id, project_id, environment, bucket_start, meter_source);

