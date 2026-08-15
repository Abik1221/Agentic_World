-- Any harness agents must go first: the narrowed constraint cannot admit them, and leaving
-- rows that violate it would make the rollback fail halfway.
DELETE FROM agents WHERE kind = 'harness';
ALTER TABLE agents DROP CONSTRAINT IF EXISTS agents_kind_check;
ALTER TABLE agents ADD CONSTRAINT agents_kind_check
    CHECK (kind = ANY (ARRAY['external'::text, 'house'::text]));
