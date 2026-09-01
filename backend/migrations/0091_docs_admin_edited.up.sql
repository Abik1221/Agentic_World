-- 0091_docs_admin_edited — stop the boot seeder from silently reverting admin edits.
--
-- docs_pages is seeded at every boot from the embedded Markdown, at the constant
-- DocsVersion, with `ON CONFLICT (version, slug) DO UPDATE ... body_md = EXCLUDED.body_md`.
-- That is correct for shipping doc changes with a deploy, and wrong for the thing the
-- table was also built for: 0058's own comment says "an admin can later edit a row to
-- override a page without a redeploy".
--
-- Both cannot be true at once. An admin edit to a seeded page survived exactly until the
-- next restart, and then reverted with nothing logged and no error — the failure mode is
-- silent, which is the worst kind for a page somebody corrected on purpose.
--
-- The flag records WHO last wrote a row. The seeder now skips rows an admin has taken
-- ownership of; everything else still updates on deploy as before.
ALTER TABLE docs_pages
    ADD COLUMN IF NOT EXISTS admin_edited BOOLEAN NOT NULL DEFAULT false;

COMMENT ON COLUMN docs_pages.admin_edited IS
    'true once an admin has written this row through /v1/admin/docs/pages. The boot seeder '
    'will not overwrite it, so an edit survives a deploy. Reset it to false to hand the page '
    'back to the embedded content.';
