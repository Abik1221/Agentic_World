# SDK Download Analytics (admin)

Goal: an admin analytics view of SDK adoption — download counts over time
(day/week/month chart) and a paginated per-country table, for the Python (PyPI) and
JS (npm) SDKs.

## Reality check (why it's a hybrid)

Neither registry gives real-time or usable geography:
- **npm** (`api.npmjs.org/downloads`): daily totals, **no geography at all**.
- **PyPI** (`pypistats.org`): daily totals; **no country** (country only exists in a
  separate daily BigQuery batch, with cost).
- **Raw per-IP** is never available from either (privacy) — and both 404 until the
  package is published.

So we combine two sources:
- **Registry poller** (authoritative totals): daily npm + PyPI counts.
- **Install pings** (near-real-time + geography): the SDK pings the arena on first
  run; the arena resolves the request to a **country** (CF-IPCountry header / GeoIP)
  and stores the COUNTRY only — never the raw IP.

## Phases

### G1 — Arena backend  ← DONE
- migration `0061_sdk_analytics`: `sdk_install_pings` (id, created_at, sdk, version,
  country) + `sdk_registry_downloads` (source, day, downloads).
- `internal/sdkstats`: Service (validate + windows) + Store; public ingest
  `POST /v1/telemetry/install` (country from CF-IPCountry, never stores IP); admin
  reads `GET /v1/admin/analytics/sdk/{summary,timeseries,countries}` (gated by
  `RequirePlatformOrAdmin`); daily registry `Poller` (npm + PyPI, 404-tolerant).
- `store.SDKStatsRepo`: summary, paginated countries, day/week/month timeseries
  (FULL OUTER JOIN of pings + registry per period).
- Tests: unit/handler/poller (fakes + httptest) + real-Postgres integration test
  (PYYOL_TEST_DATABASE_URL). build/vet/gofmt green; migrations apply through 0061.
- main.go wiring (mount + `launch("sdk-download-poller", …)`) is in the working tree
  but UNCOMMITTED — main.go currently holds a parallel session's WIP; rides along.

### G2 — SDK first-run ping  ← next
- Python + JS SDK: on first run, POST `/v1/telemetry/install` {sdk, version} once
  (dedup via a local marker), opt-out via `PYYOL_NO_TELEMETRY` / `DO_NOT_TRACK`.
  Fire-and-forget, never blocks or errors the CLI.

### G3 — Admin UI page (separate Super_Admin repo)
- Line chart (day/week/month toggle) of downloads + install pings; paginated
  per-country table; summary tiles. Consumes the G1 admin APIs.

## Notes
- Data is DAILY at best (no real-time from registries); the install-ping half is the
  freshest + only geographic signal, and it counts opt-in pings (approximate), not
  raw registry downloads.
