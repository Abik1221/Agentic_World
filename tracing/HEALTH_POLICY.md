# Health Policy

This document defines the readiness contract for `pyyol-lens` query API health endpoints.

## Endpoints

- `GET /health`
  - Liveness-style check for query-api process availability.
  - Returns HTTP `200` when the server is running.
  - Does not evaluate projection pipeline health.

- `GET /health/ready`
  - Readiness check for serving reliable projected/aggregated data.
  - Evaluates ingest-vs-processor behavior using the same rules as projection status endpoints.
  - Supports optional tuning query params:
    - `window_minutes`
    - `degraded_backlog`
    - `stalled_backlog`
    - `degraded_lag_seconds`
    - `stalled_lag_seconds`

## Status semantics

- `ok`
  - Pipeline is healthy and keeping up.
  - Readiness: `ready=true`
  - HTTP status: `200`

- `degraded`
  - Pipeline is functioning but lagging behind target thresholds.
  - Readiness: `ready=true`
  - HTTP status: `200`

- `stalled`
  - Pipeline is not progressing acceptably (for example, ingest active with no recent processing, or critical backlog/lag thresholds exceeded).
  - Readiness: `ready=false`
  - HTTP status: `503`

- `error`
  - Readiness check itself cannot query required data sources/tables.
  - Readiness: `ready=false`
  - HTTP status: `503`

## Operational guidance

- Use `GET /health` for basic process liveness.
- Use `GET /health/ready` for deployment gating and autoscaler/service readiness decisions.
- Use `GET /v1/projections/metrics/prometheus` (or `/v1/projections/metrics`) for alerting and trend dashboards.

