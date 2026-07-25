-- 0061_sdk_analytics — admin "SDK downloads" analytics (hybrid).
--
-- sdk_install_pings: one row per SDK first-run ping. We store the resolved COUNTRY
-- (ISO-2, or 'XX' unknown) — NEVER the raw IP — so per-country analytics is possible
-- without holding PII. Near-real-time + geographic.
CREATE TABLE IF NOT EXISTS sdk_install_pings (
    id         BIGSERIAL   PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    sdk        TEXT        NOT NULL,          -- python | js
    version    TEXT        NOT NULL DEFAULT '',
    country    TEXT        NOT NULL DEFAULT 'XX'
);
CREATE INDEX IF NOT EXISTS idx_sdk_install_pings_created ON sdk_install_pings (created_at);
CREATE INDEX IF NOT EXISTS idx_sdk_install_pings_country ON sdk_install_pings (country);
CREATE INDEX IF NOT EXISTS idx_sdk_install_pings_sdk     ON sdk_install_pings (sdk);

-- sdk_registry_downloads: authoritative DAILY download totals polled from npm + PyPI.
CREATE TABLE IF NOT EXISTS sdk_registry_downloads (
    source     TEXT        NOT NULL,          -- pypi | npm
    day        DATE        NOT NULL,
    downloads  BIGINT      NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source, day)
);
