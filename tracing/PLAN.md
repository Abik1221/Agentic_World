# Pyyol Lens Full Engine Implementation Plan

## Summary
Implement `pyyol-lens` as the full standalone internal AI observability engine described in `/Users/m1pro/Desktop/Alet/pyyol-lens-perfect-plan.md`, not as a narrow trace viewer. The finished system includes:

- Trace engine
- Token metering engine
- Replay engine
- Evaluation engine
- Prompt and model registry
- Policy, redaction, and retention engine
- Analytics and dashboard engine
- Node.js SDK
- Go ingestion/query/control services
- ClickHouse analytics store
- PostgreSQL control plane
- S3-compatible blob storage
- Durable event stream

Keep the existing repo structure:
- `agent-arena/pyyol-lens/backend`
- `agent-arena/pyyol-lens/sdk/nodejs`
- `agent-arena/pyyol-lens/web`

Evolve the current prototype into a production-ready system instead of replacing it wholesale.

## Architecture
### Runtime topology
- `ingest-api` in Go receives SDK/service telemetry.
- `processor` in Go consumes the durable stream and builds normalized analytics data.
- `query-api` in Go serves traces, trees, analytics, search, replay state, and dashboard reads.
- `control-api` in Go serves projects, SDK keys, prompts, prompt versions, pricing, datasets, evaluations, budgets, alerts, policies, and audit logs.
- `web` in Next.js serves the dashboard.
- `sdk/nodejs` is the canonical instrumentation library for Pyyol services.
- ClickHouse stores raw and derived analytical data.
- PostgreSQL stores control-plane state.
- MinIO locally, S3-compatible storage in production, stores large payload blobs, archives, and replay snapshots.
- NATS JetStream is the durable queue/stream for v1.
- Pyyol Lens reuses `pyyol-api` identity/org/user context as the source of truth for authentication and org scoping.

### Local infrastructure
Extend `pyyol-lens/docker-compose.yml` to include:
- ClickHouse
- PostgreSQL
- NATS JetStream
- MinIO
- optional admin UIs only if they do not become product dependencies

### Environment model
Support `development`, `staging`, and `production` from day one.
Each environment has:
- separate SDK keys
- separate budgets
- separate retention rules
- separate dashboards/filters
- separate policy enforcement

## Core Domain Model
### Canonical entities
Implement these first-class entities:

- Trace
- Span
- Event
- Token usage record
- Tool call
- Retrieval record
- Replay run
- Evaluation dataset
- Evaluation example
- Evaluation run
- Prompt
- Prompt version
- Model config
- Pricing model
- Budget
- Policy
- Alert rule
- Audit log
- API key
- Project

### Trace shape
Store:
- `trace_id`
- `request_id`
- `org_id`
- `project_id`
- `environment`
- `user_id`
- `actor_id`
- `session_id`
- `started_at`
- `ended_at`
- `status`
- `root_input_ref`
- `root_output_ref`
- `total_tokens`
- `total_cost`
- `latency_ms`
- `error_type`
- `error_message`
- `workflow_version`
- `prompt_version_ids`
- `model_config_versions`
- `sampling_reason`
- `redaction_summary`

### Span shape
Store:
- `span_id`
- `trace_id`
- `parent_span_id`
- `span_type`
- `step_name`
- `status`
- `started_at`
- `ended_at`
- `latency_ms`
- `sequence_index`
- `input_ref`
- `output_ref`
- `error_type`
- `error_message`
- `provider`
- `model`
- `model_version`
- `tool_name`
- `tool_version`
- retrieval metadata
- token usage
- cost

### Event shape
Store:
- `event_id`
- `trace_id`
- `span_id`
- `parent_span_id`
- `event_type`
- `timestamp`
- `sequence_number`
- `source_service`
- `schema_version`
- `status`
- inline structured fields
- `payload_ref`
- error details
- redaction metadata

## Event Taxonomy
Adopt the canonical event taxonomy from the spec. Support a temporary compatibility mapper for current names emitted by `pyyol-api`.

### Lifecycle
- `trace_started`
- `trace_completed`
- `trace_failed`
- `span_started`
- `span_completed`
- `span_failed`

### Model
- `model_call_started`
- `model_call_completed`
- `model_call_failed`
- `model_call_retried`

### Retrieval
- `retrieval_started`
- `retrieval_completed`
- `rerank_started`
- `rerank_completed`

### Tool
- `tool_call_started`
- `tool_call_completed`
- `tool_call_failed`

### Metering
- `usage_reported`
- `token_estimated`
- `cost_calculated`
- `budget_checked`

### Quality
- `schema_validated`
- `output_accepted`
- `output_rejected`
- `human_feedback_added`
- `evaluation_completed`

### Safety/privacy
- `field_redacted`
- `payload_quarantined`
- `access_denied`

## Storage Plan
### ClickHouse
Keep the current raw ingestion journal, but rename or wrap it behind the canonical schema and expand it.

Create:
- `events_raw`
- `traces`
- `spans`
- `events`
- `token_usage`
- `tool_calls`
- `retrievals`
- `evaluations`
- `replays`
- `rollup_hourly`
- `rollup_daily`
- `rollup_monthly`

Design rules:
- partition by date
- order by org/project/environment/time/trace
- keep hot filter fields as real columns
- avoid large unbounded JSON in hot tables
- use TTL for retention
- use materialized views or processor-driven aggregation for hot summaries

### PostgreSQL
Create control-plane tables for:
- organizations reference mapping
- users reference mapping
- projects
- service API keys
- prompts
- prompt_versions
- model_configs
- pricing_models
- datasets
- dataset_examples
- evaluation_runs
- budgets
- alerts
- policies
- audit_logs
- replay_configs
- saved_views

### Object storage
Store:
- large prompts
- large model inputs/outputs
- retrieval payloads
- tool raw outputs
- replay snapshots
- trace archives
- export bundles
- quarantined payloads

## Backend Services
### Ingest API
Replace the current direct-write pattern with a real ingestion gateway.

Implement:
- `POST /v1/events/batch`
- API key authentication
- schema-version validation
- event normalization
- redaction policy application
- deduplication by `event_id`
- ingestion ID creation
- raw journal write
- publish to JetStream
- partial acceptance/rejection reporting
- fast acknowledgment target under 500 ms

Response shape:
- `accepted`
- `rejected`
- `ingestion_id`
- per-item validation failures when needed

### Processor
Create a dedicated stream consumer service.

Responsibilities:
- consume from JetStream
- assemble traces/spans from raw events
- compute durations and statuses
- compute token usage rollups
- calculate estimated cost
- later reconcile cost if provider truth changes
- extract tool/retrieval/model views
- maintain replay snapshots
- build hourly/daily/monthly rollups
- enforce retention and sampling decisions
- write DLQ entries for poison messages

### Query API
Expand from the current run endpoints to the full trace/query surface.

Required endpoints:
- `GET /v1/traces`
- `GET /v1/traces/{trace_id}`
- `GET /v1/traces/{trace_id}/tree`
- `GET /v1/traces/{trace_id}/events`
- `GET /v1/usage/summary`
- `GET /v1/costs/summary`
- `GET /v1/retrievals`
- `GET /v1/tool-calls`
- `GET /v1/evaluations`
- `GET /v1/evaluations/{run_id}`
- `GET /v1/prompts`
- `GET /v1/prompts/{id}/versions`
- `GET /v1/replays`
- `GET /v1/replays/{id}`
- `GET /v1/search/traces`
- `GET /v1/search/events`

Support:
- filtering
- sorting
- pagination
- saved views
- comparisons between versions/time windows

### Control API
Add a separate service or route group for control-plane mutation.

Required domains:
- projects
- SDK keys
- prompts and versions
- model configs
- pricing tables
- datasets and examples
- evaluation runs
- budgets
- alerts
- policies
- retention settings
- audit log reads
- export jobs
- replay launch requests

### Replay engine
Implement:
- exact replay
- best-effort replay
- simulation replay

Each replay stores:
- original trace reference
- snapshot references
- prompt version
- model config version
- retrieval config
- tool config
- environment reference
- replay outcome
- diff vs original

## Token Metering and Cost
### Required per model call
Capture:
- provider
- model
- model_version if known
- trace_id
- span_id
- request_id
- prompt_tokens
- completion_tokens
- cached_tokens
- reasoning_tokens
- total_tokens
- estimated_cost
- reconciled_cost
- currency
- pricing_version
- meter_source
- latency_ms
- input_bytes
- output_bytes

### Metering modes
Implement all three:
- provider-reported
- local-estimate
- reconciled

### Pricing registry
In PostgreSQL, store versioned pricing rows:
- provider
- model
- input price
- output price
- cached price
- reasoning price
- currency
- effective_from
- effective_to
- pricing_version

### Budget controls
Support:
- soft alerts
- hard caps
- project budgets
- environment budgets
- user budgets
- anomaly detection hooks
- budget enforcement records via `budget_checked`

## Replay and Evaluation
### Evaluation engine
Implement:
- rule-based validators
- schema validation
- reference matching
- human review
- LLM-as-judge
- custom scorers

### Dataset model
Store:
- `dataset_id`
- `example_id`
- input
- expected output or rubric
- tags
- references
- version
- timestamps

### Evaluation runs
Store:
- dataset version
- prompt version
- model config version
- workflow version
- score summary
- per-example failures
- comments
- comparison baseline

Dashboard must answer:
- did quality improve
- which examples regressed
- what subsystem caused the regression

## Policy, Redaction, Retention, Security
### Four-bucket logging policy
Classify fields and payloads into:
- always log
- conditionally log
- redact
- quarantine

### Redaction modes
Implement:
- regex masking
- field-based masking
- hashing
- drop field
- quarantine blob

### Access control
Use `pyyol-api` user/org identity, then apply Pyyol Lens roles and policy rules for:
- org scope
- project scope
- environment scope
- raw payload access
- export permissions
- pricing/policy admin

### Audit logs
Audit:
- raw payload views
- exports
- policy changes
- retention changes
- pricing changes
- prompt edits
- replay launches
- budget changes

### Retention
Support:
- per-project retention
- per-environment retention
- per-event-type retention
- archive tier
- purge jobs
- object-store cleanup

## SDK Plan
### Canonical SDK surface
Expose:
- `new PyyolLens(config)`
- `client.startTrace(...)`
- `trace.startSpan(...)`
- `trace.log(...)`
- `span.log(...)`
- `span.logModelCall(...)`
- `span.logToolCall(...)`
- `span.logRetrieval(...)`
- `trace.annotate(...)`
- `span.end()`
- `trace.end()`
- `client.flush()`

### Internal SDK components
Implement:
- Client
- TraceManager
- SpanManager
- EventSerializer
- BatchManager
- TransportLayer
- RetryEngine
- ContextManager
- Redactor
- LocalBuffer
- ConfigurationLoader

### Transport behavior
Support:
- in-memory batching
- size/time flush
- compression
- async delivery
- retry with backoff
- optional offline buffer
- transport health metrics
- non-blocking app path

### Compatibility
Keep `initPyyolLens` and `getPyyolLens` as compatibility wrappers over the new client during migration.

## Pyyol Integration Plan
### `pyyol-api`
Instrument first and most deeply.

Cover:
- agent request start/end/failure
- app run start/end/failure
- LLM calls in `LlmService`
- tool execution start/result/failure
- agent iteration loop
- conversation linkage
- scrape/deep-run workflows
- retries/fallbacks
- queue events where applicable

Use existing:
- `runId`
- `conversationId`
- org/user/site context
- `AgentEventBus`

Map these into canonical Pyyol Lens traces/spans/events.

### `pyyol-web`
Instrument client and server flows only where they materially support AI workflow tracing, not generic page analytics.

### `pyyol-scraper`
Instrument:
- scrape job lifecycle
- target processing
- retries/failures
- captcha/tool interactions
- long-running task timing
- provider/network failures

### `ras-solvego`
Only instrument if it is invoked as part of AI/scrape workflows; treat it as tool-call telemetry, not as a separate first-class product domain.

## Dashboard Plan
### Navigation
Implement:
- Overview
- Traces
- Trace Detail
- Token Usage
- Costs
- Retrievals
- Tool Calls
- Evaluations
- Datasets
- Prompts
- Alerts
- Settings

### Overview page
Show:
- total traces
- success/failure counts
- average latency
- p95 latency
- token usage
- total cost
- cost trends
- top failing workflows
- top expensive workflows
- quality trend

### Trace list
Support:
- search
- date range
- status
- model
- user
- prompt version
- cost threshold
- latency threshold
- saved views

### Trace detail
Build the LangSmith-style primary screen:
- span tree / graph
- timeline
- raw event feed
- model call detail
- retrieval detail
- tool detail
- token/cost detail
- errors
- redaction markers
- replay action
- annotations / feedback

### Prompt registry
Show:
- prompts
- versions
- diffs
- deployments
- linked traces
- test outputs

### Evaluation views
Show:
- datasets
- evaluation runs
- score trends
- baseline comparisons
- failed examples

### Current scaffold adaptation
Refactor the current `/runs`, `/deep-runs`, `/metrics`, and `/events` pages into the canonical product IA.
Route aliases may remain temporarily, but the product language becomes trace-first, not run-first.

## Reliability and Platform Observability
### Failure handling
Implement:
- idempotent ingestion
- durable stream before async processing
- retry with backoff
- DLQ
- processor restart safety
- duplicate delivery tolerance

### Backpressure
When overloaded:
- always keep failures
- always keep root trace lifecycle
- always keep expensive traces
- sample low-value verbose success events
- never block app critical path on observability

### Observe Pyyol Lens itself
Track:
- ingestion rate
- ingestion failures
- queue lag
- processor throughput
- ClickHouse insert latency
- query latency
- dropped events
- redaction failures
- DLQ volume
- dashboard latency
- storage growth
- platform cost

## Implementation Phases
### Phase 1: Foundation
- canonical schemas
- Docker infra expansion
- Postgres + MinIO + NATS setup
- ingest API redesign
- raw ClickHouse journal
- SDK v2 skeleton
- compatibility mapper for current event names

### Phase 2: Trace engine
- processor service
- normalized ClickHouse tables
- trace/span assembly
- query API trace endpoints
- trace explorer UI
- `pyyol-api` core instrumentation

### Phase 3: Token and cost engine
- pricing registry
- token usage tables
- cost calculation
- usage/cost dashboards
- budget checks and alerts

### Phase 4: Replay engine
- snapshot storage
- replay API
- replay UI
- original vs replay diff

### Phase 5: Evaluation engine
- datasets
- examples
- evaluation runs
- scorers
- evaluation dashboards
- regression comparison views

### Phase 6: Governance and hardening
- full redaction engine
- quarantine support
- retention engine
- audit logs
- prompt registry
- advanced permissions
- operational hardening

## Acceptance Criteria
The implementation is complete only when all of these are true:

1. Every AI request in Pyyol produces a trace.
2. Every important step produces a nested span.
3. Every model call records token usage and cost.
4. Retrieval and tool calls are visible in trace detail.
5. Trace search/filtering is fast and usable.
6. Trace detail reconstructs the run clearly.
7. Failed or important traces can be replayed.
8. Prompt and model versions are tracked.
9. Datasets and evaluation runs work end to end.
10. Cost rollups are available by project, model, and environment.
11. Budgets and alerts work.
12. Redaction and retention are enforced.
13. Audit logs exist for sensitive actions.
14. The system survives partial downstream failure without losing critical trace data.
15. Instrumentation overhead does not materially degrade Pyyol application performance.

## Test Plan
### Backend
- schema validation
- idempotency
- dedupe
- redaction/quarantine behavior
- processor correctness
- rollup correctness
- pricing version correctness
- replay correctness
- retention cleanup
- access control
- audit logging

### SDK
- nested span creation
- async context propagation
- batching by size/time
- retry/backoff
- offline buffer restore
- redaction hooks
- compatibility API behavior

### Integration
- `pyyol-api` agent trace end to end
- app run trace end to end
- scraper/deep-run telemetry end to end
- replay of failed trace
- evaluation against dataset
- prompt version linked to traces

### UI
- overview loads correctly
- trace list filters
- trace detail tree/timeline/event panes stay consistent
- cost/usage summaries match backend rollups
- replay launch and diff render correctly
- admin-only data is gated correctly

### Failure-mode tests
- ClickHouse unavailable
- JetStream unavailable
- object storage unavailable
- processor restart during backlog
- duplicate event delivery
- high-volume backpressure sampling

## Assumptions and Defaults
- Use `pyyol-api` for identity and org/user context.
- Use PostgreSQL for control-plane state.
- Use NATS JetStream for the durable event stream.
- Use MinIO locally and S3-compatible storage in production.
- Keep the existing `pyyol-lens` repo split and evolve it.
- Preserve current SDK exports temporarily through compatibility wrappers.
- Accept current event names from Pyyol services through a translation layer, but emit canonical event taxonomy for all new instrumentation.
- Build everything in the markdown plan; no subsystem from the source document is intentionally omitted in this implementation plan.
