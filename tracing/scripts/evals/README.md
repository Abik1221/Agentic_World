# Phase 10 eval harness

Run after a fixture agent run has been ingested into pyyol-lens.

## Setup

1. Apply ClickHouse migration [`backend/migrations/clickhouse/002_phase10_telemetry.sql`](../../backend/migrations/clickhouse/002_phase10_telemetry.sql).
2. Query API must be reachable (default `http://127.0.0.1:8082`).

## Environment

| Variable | Description |
|----------|-------------|
| `PYYOL_LENS_ORG_ID` | Organization id (sent as `x-organization-id`) |
| `RUN_ID` | Run / trace id to evaluate |
| `QUERY_BASE_URL` | Optional; default `http://127.0.0.1:8082` |
| `MAX_LEAD_CONTEXT_TOKENS` | Cap for `eval-lead-context-size` (default 200000) |
| `EVAL_SKIP_NEEDLE` | Set to `1` to skip needle eval |
| `EVAL_SUBAGENT_ID` | Sub-agent id string for strict subagent isolation check |

## Commands

```bash
cd scripts/evals
npm install
PYYOL_LENS_ORG_ID=org_123 RUN_ID=trace_abc npm run eval -- all
```

Individual evals: `npm run eval -- lead` | `citation` | `needle` | `reducer` | `subagent`

## Notes

- These checks use **collector** APIs only (`/v1/runs/:id/dataflow`, `/v1/runs/:id/telemetry-kpi`, `/v1/traces/:id/events`). Full citation coverage and reducer dimension validation may also read Mongo in `pyyol-api` in production.
