-- 0058_docs_pages — docs-as-data. The arena seeds the modular Markdown docs
-- (internal/docs/content) into this table at startup, versioned, so the frontend
-- serves structured, versioned documentation from the API. An admin can later edit
-- a row to override a page without a redeploy.
CREATE TABLE IF NOT EXISTS docs_pages (
    version    TEXT        NOT NULL,
    slug       TEXT        NOT NULL,
    title      TEXT        NOT NULL DEFAULT '',
    section    TEXT        NOT NULL DEFAULT '',
    game       TEXT        NOT NULL DEFAULT '',
    category   TEXT        NOT NULL DEFAULT '',
    ord        INT         NOT NULL DEFAULT 0,
    body_md    TEXT        NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (version, slug)
);

-- Nav/list queries read by (version, section rank, ord); index the version prefix.
CREATE INDEX IF NOT EXISTS idx_docs_pages_version ON docs_pages (version, section, ord);
