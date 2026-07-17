# Pyyol Lens

Standalone observability system for Pyyol chat/app/deep-run telemetry (ClickHouse + NATS + Go query/ingest + Next.js UI).

Read health semantics in `HEALTH_POLICY.md`.

## Architecture (quick)

| Piece | Role |
|--------|------|
| **ingest-api** (`cmd/ingest-api`) | Accepts batched telemetry (`POST /v1/events/batch`), writes **MergeTree** `events_raw`, publishes to NATS |
| **processor** (`cmd/processor`) | Consumes NATS, dedupes via `processed_events`, upserts `traces` / `spans`, inserts `events`, `token_usage`, rollups |
| **query-api** (`cmd/query-api`) | Read APIs for traces, spans, events, overview, metrics |
| **web** (`web/`) | Next.js UI; talks to query API (and optional control API) |
| **ClickHouse** | `events_raw` source of truth; derived tables rebuilt via backfill if needed |
| **NATS JetStream** | Buffer between ingest and processor |

End-to-end with **pyyol-api**: the API emits telemetry to ingest using `PYYOL_LENS_ENDPOINT` and `PYYOL_LENS_API_KEY`.

---

## Local deployment

### 1) Full stack in Docker (recommended)

```bash
cp .env.docker.example .env
docker compose up -d --build
```

This starts **infra** (ClickHouse, Postgres, NATS, MinIO) plus **ingest-api**, **query-api**, **control-api**, **processor**, and the **web panel** on port **3100**. See **[deploy/Docker.md](./deploy/Docker.md)**.

### 1b) Infrastructure only

```bash
docker compose up -d clickhouse postgres nats minio
```

Use this if you still run Go services and `npm run dev` on the host.

Ensure ClickHouse applied schema (first boot mounts `backend/migrations/clickhouse`). If upgrading an old volume:

```bash
docker exec pyyol-lens-clickhouse clickhouse-client --user default --queries-file /docker-entrypoint-initdb.d/001_init.sql
```

### 2) Backend (three processes)

Use the same env as Docker services. Minimal:

| Variable | Typical local |
|----------|----------------|
| `CH_ADDR` | `localhost:9000` |
| `CH_DB` | `pyyol_lens` |
| `CH_USER` | `default` |
| `CH_PASS` | empty or set to match ClickHouse |
| `NATS_URL` | `nats://localhost:4222` |
| `INGEST_API_KEY` | shared secret (e.g. `local-pyyol-lens-key`) |

```bash
cd backend
go run ./cmd/ingest-api
```

```bash
cd backend
go run ./cmd/processor
```

```bash
cd backend
go run ./cmd/query-api
```

Ports default: ingest **8081**, query **8082**, control **8083** (override with `PORT_INGEST`, `PORT_QUERY`, `PORT_CONTROL`).

### 3) Web UI

```bash
cd web
npm install
npm run dev
```

Optional env (see `web` and `pyyol-lens-api` client):

```bash
PYYOL_LENS_QUERY_URL=http://localhost:8082
PYYOL_LENS_CONTROL_URL=http://localhost:8083
PYYOL_LENS_DEFAULT_ORG_ID=local-dev-org
PYYOL_LENS_DEFAULT_USER_ID=local-dev-user
PYYOL_LENS_ALLOW_DIRECT_FALLBACK=true
```

### 4) Pyyol API → Pyyol Lens

In **pyyol-api** (separate repo), point telemetry at the ingest service:

| Variable | Example |
|----------|---------|
| `PYYOL_LENS_ENABLED` | `true` |
| `PYYOL_LENS_ENDPOINT` | `http://localhost:8081` |
| `PYYOL_LENS_API_KEY` | same value as `INGEST_API_KEY` |

---

## Production deployment

High-level checklist (adapt to your orchestrator: k8s, VMs, etc.):

1. **ClickHouse**  
   - Managed CH or self-hosted with TLS, backups, and retention aligned with `events_raw` TTL (see migration).  
   - Run migrations from `backend/migrations/clickhouse/` on upgrades.  
   - Store `CH_PASS` (and `CH_SECURE=true` if using TLS).

2. **NATS**  
   - JetStream enabled; durable consumer matches `NATS_STREAM_NAME` / `NATS_CONSUMER_NAME`.  
   - Size streams for peak ingest; monitor consumer lag.

3. **Go services**  
   - Build static binaries: `go build -o ingest-api ./cmd/ingest-api` (and processor, query-api).  
   - Set all config via env (see `internal/config/config.go`).  
   - Place **ingest** behind TLS termination; require `X-Pyyol-Key` / `INGEST_API_KEY`.  
   - **Query** API: restrict by network and/or API keys as you deploy (org scoping is in handlers).

4. **Web**  
   - `npm run build && npm start` or static export per Next.js docs.  
   - Set **public** env for browser: query URL must be reachable by users (or use a BFF).  
   - Do **not** expose ingest keys to the browser.

5. **pyyol-api**  
   - `PYYOL_LENS_ENDPOINT` = internal URL to ingest (HTTPS in prod).  
   - Rotate `PYYOL_LENS_API_KEY` with ingest’s `INGEST_API_KEY`.

6. **Health**  
   - `GET /health` on ingest and query; readiness `GET /health/ready` on query (see `HEALTH_POLICY.md`).

---

## Backfill: projections from `events_raw`

Use when **`events_raw` already has data** but normalized tables are empty or stale (e.g. after schema changes, or you fixed costs — see below).

```bash
cd backend
BACKFILL_RESET=true go run ./cmd/backfill-projections
```

- `BACKFILL_RESET=true` **truncates** projection tables (`traces`, `spans`, `events`, `token_usage`, rollups, `processed_events`, etc.) then **re-inserts** from `events_raw`.  
- Omit `BACKFILL_RESET` to append-only insert (usually **not** what you want if duplicates matter — prefer reset after fixing raw data).

---

## Backfill: `estimated_cost` on historical `events_raw` + re-project

Older telemetry often had **token counts** but **`estimated_cost = 0`**. The heuristic matches **`pyyol-api`** `estimate-llm-cost.ts` (ported to **`internal/pricing/llm_cost.go`**).

**1) Preview how many rows qualify**

```bash
cd backend
go run ./cmd/backfill-estimated-cost -dry-run
```

**2) Apply updates** (optional `-limit N` for a test slice, `-batch 200` for mutation batch size)

```bash
go run ./cmd/backfill-estimated-cost
```

**3) Rebuild projections** so `traces.total_cost`, `token_usage`, rollups pick up the new costs (same as reset + backfill):

```bash
go run ./cmd/backfill-estimated-cost -reproject
```

Or run step (2) then separately:

```bash
BACKFILL_RESET=true go run ./cmd/backfill-projections
```

ClickHouse applies **mutations** asynchronously; `-reproject` uses `SETTINGS mutations_sync = 1` per batch so each batch completes before the next. For very large tables, monitor `system.mutations`.

---

## Verify pipeline status

Check health:

```bash
curl -s http://localhost:8081/health
curl -s http://localhost:8082/health
curl -i -s http://localhost:8082/health/ready
```

Check projection coverage:

```bash
curl -s http://localhost:8082/v1/projections/status | jq
curl -s http://localhost:8082/v1/projections/status/summary | jq
curl -s http://localhost:8082/v1/projections/metrics | jq
curl -s http://localhost:8082/v1/projections/metrics/prometheus
```

Expected in steady state:

- `events_raw` count grows from ingest.  
- `processed_events` grows from processor consumption.  
- Normalized tables (`traces`, `spans`, `events`, `token_usage`) match expectations after processing or backfill.  
- Rollups non-zero after backfill or sufficient traffic.  
- `pipeline.status` is `ok` when lag/backlog are within thresholds (see `HEALTH_POLICY.md`).

---

## Troubleshooting: `nats: no servers available for connection`

```bash
docker compose up -d nats
docker compose ps nats
curl -s http://localhost:8222/healthz
```

If NATS uses a different host/port, set `NATS_URL`.

---

## Troubleshooting: runs finish in Pyyol API but Pyyol Lens stays empty

1. All three backend processes running (ingest, processor, query).  
2. `events_raw` exists — re-apply migration if needed (see above).  
3. Ingest key matches between pyyol-api and ingest-api.  
4. Verify counts:

```bash
curl -s -u "default:${CH_PASS}" "http://localhost:8123/?query=SELECT%20count()%20FROM%20pyyol_lens.events_raw"
curl -s -u "default:${CH_PASS}" "http://localhost:8123/?query=SELECT%20count()%20FROM%20pyyol_lens.processed_events"
```
