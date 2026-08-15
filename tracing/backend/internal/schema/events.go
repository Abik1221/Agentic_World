package schema

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

const CurrentSchemaVersion = "2026-04-17"

// MaxTelemetryFreeText is the cap for analytics-safe but sensitive free-text in payloads (NLP, quotes).
const MaxTelemetryFreeText = 240

type TelemetryEvent struct {
	EventID             string         `json:"event_id"`
	TraceID             string         `json:"trace_id"`
	RequestID           string         `json:"request_id"`
	SpanID              string         `json:"span_id"`
	ParentSpanID        string         `json:"parent_span_id"`
	EventType           string         `json:"event_type"`
	EventTime           time.Time      `json:"event_time"`
	SequenceNumber      int64          `json:"sequence_number"`
	SourceService       string         `json:"source_service"`
	SchemaVersion       string         `json:"schema_version"`
	Status              string         `json:"status"`
	OrganizationID      string         `json:"organization_id"`
	ProjectID           string         `json:"project_id"`
	Environment         string         `json:"environment"`
	UserID              string         `json:"user_id"`
	ActorID             string         `json:"actor_id"`
	SessionID           string         `json:"session_id"`
	RunID               string         `json:"run_id"`
	ConversationID      string         `json:"conversation_id"`
	AppID               string         `json:"app_id"`
	QueueJobID          string         `json:"queue_job_id"`
	Component           string         `json:"component"`
	Operation           string         `json:"operation"`
	SpanType            string         `json:"span_type"`
	StepName            string         `json:"step_name"`
	Provider            string         `json:"provider"`
	Model               string         `json:"model"`
	ModelVersion        string         `json:"model_version"`
	ToolName            string         `json:"tool_name"`
	ToolVersion         string         `json:"tool_version"`
	RootInputRef        string         `json:"root_input_ref"`
	RootOutputRef       string         `json:"root_output_ref"`
	PayloadRef          string         `json:"payload_ref"`
	PayloadJSON         map[string]any `json:"payload_json"`
	PromptVersionIDs    []string       `json:"prompt_version_ids"`
	ModelConfigVersions []string       `json:"model_config_versions"`
	// Phase 10: task lineage & cost/quality dimensions (additive; empty/zero if omitted).
	TaskKind             string   `json:"task_kind"`
	Archetype            string   `json:"archetype"`
	Scope                string   `json:"scope"`
	SubagentID           string   `json:"subagent_id"`
	ParentSubagentID     string   `json:"parent_subagent_id"`
	ArtifactIDsIn        []string `json:"artifact_ids_in"`
	ArtifactIDsOut       []string `json:"artifact_ids_out"`
	EvidenceIDsOut       []string `json:"evidence_ids_out"`
	CitationIDsOut       []string `json:"citation_ids_out"`
	ReducerName          string   `json:"reducer_name"`
	ReductionRatio       float64  `json:"reduction_ratio"`
	ToolTokenSavings     int64    `json:"tool_token_savings"`
	BudgetIterationsUsed int64    `json:"budget_iterations_used"`
	BudgetIterationsCap  int64    `json:"budget_iterations_cap"`
	BudgetTokensUsed     int64    `json:"budget_tokens_used"`
	BudgetTokensCap      int64    `json:"budget_tokens_cap"`

	LatencyMS        int64          `json:"latency_ms"`
	InputBytes       int64          `json:"input_bytes"`
	OutputBytes      int64          `json:"output_bytes"`
	PromptTokens     int64          `json:"prompt_tokens"`
	CompletionTokens int64          `json:"completion_tokens"`
	CachedTokens     int64          `json:"cached_tokens"`
	ReasoningTokens  int64          `json:"reasoning_tokens"`
	TotalTokens      int64          `json:"total_tokens"`
	EstimatedCost    float64        `json:"estimated_cost"`
	ReconciledCost   float64        `json:"reconciled_cost"`
	Currency         string         `json:"currency"`
	PricingVersion   string         `json:"pricing_version"`
	MeterSource      string         `json:"meter_source"`
	// AgentKind is whose traffic this span is: "external" (a developer's agent) or
	// "harness" (a Pyyol platform benchmark seat). Empty means UNKNOWN — including every
	// row written before ClickHouse migration 007 — and must never be read as "external",
	// which would fold platform benchmark calls back into a developer's telemetry.
	AgentKind string `json:"agent_kind"`
	ErrorType        string         `json:"error_type"`
	ErrorCode        string         `json:"error_code"`
	ErrorMessage     string         `json:"error_message"`
	SamplingReason   string         `json:"sampling_reason"`
	RedactionSummary map[string]any `json:"redaction_summary"`
	IngestedAt       time.Time      `json:"ingested_at"`
}

type EventBatchRequest struct {
	Events []TelemetryEvent `json:"events"`
}

type ValidationFailure struct {
	Index   int    `json:"index"`
	EventID string `json:"event_id"`
	Error   string `json:"error"`
}

type EventBatchResponse struct {
	Accepted    int                 `json:"accepted"`
	Rejected    int                 `json:"rejected"`
	IngestionID string              `json:"ingestion_id"`
	Failures    []ValidationFailure `json:"failures,omitempty"`
}

type TraceSummary struct {
	TraceID        string    `json:"trace_id"`
	RequestID      string    `json:"request_id"`
	Status         string    `json:"status"`
	OrganizationID string    `json:"organization_id"`
	ProjectID      string    `json:"project_id"`
	Environment    string    `json:"environment"`
	UserID         string    `json:"user_id"`
	StartedAt      time.Time `json:"started_at"`
	EndedAt        time.Time `json:"ended_at"`
	LatencyMS      int64     `json:"latency_ms"`
	EventCount     int64     `json:"event_count"`
	TotalTokens    int64     `json:"total_tokens"`
	TotalCost      float64   `json:"total_cost"`
	ErrorType      string    `json:"error_type"`
	ErrorMessage   string    `json:"error_message"`
	Model          string    `json:"model"`
	ToolName       string    `json:"tool_name"`
}

type SpanSummary struct {
	SpanID         string         `json:"span_id"`
	TraceID        string         `json:"trace_id"`
	ParentSpanID   string         `json:"parent_span_id"`
	EventType      string         `json:"event_type,omitempty"`
	TaskKind       string         `json:"task_kind,omitempty"`
	Archetype      string         `json:"archetype,omitempty"`
	Scope          string         `json:"scope,omitempty"`
	SubagentID     string         `json:"subagent_id,omitempty"`
	ArtifactIDsIn  []string       `json:"artifact_ids_in,omitempty"`
	ArtifactIDsOut []string       `json:"artifact_ids_out,omitempty"`
	SpanType       string         `json:"span_type"`
	StepName       string         `json:"step_name"`
	Status         string         `json:"status"`
	StartedAt      time.Time      `json:"started_at"`
	EndedAt        time.Time      `json:"ended_at"`
	LatencyMS      int64          `json:"latency_ms"`
	Provider       string         `json:"provider"`
	Model          string         `json:"model"`
	ToolName       string         `json:"tool_name"`
	TotalTokens    int64          `json:"total_tokens"`
	EstimatedCost  float64        `json:"estimated_cost"`
	ErrorType      string         `json:"error_type"`
	ErrorMessage   string         `json:"error_message"`
	Children       []*SpanSummary `json:"children,omitempty"`
}

var canonicalEventTypes = map[string]struct{}{
	"trace_started":        {},
	"trace_completed":      {},
	"trace_failed":         {},
	"span_started":         {},
	"span_completed":       {},
	"span_failed":          {},
	"model_call_started":   {},
	"model_call_completed": {},
	"model_call_failed":    {},
	"model_call_retried":   {},
	"retrieval_started":    {},
	"retrieval_completed":  {},
	"rerank_started":       {},
	"rerank_completed":     {},
	"tool_call_started":    {},
	"tool_call_completed":  {},
	"tool_call_failed":     {},
	"usage_reported":       {},
	"token_estimated":      {},
	"cost_calculated":      {},
	"budget_checked":       {},
	"schema_validated":     {},
	"output_accepted":      {},
	"output_rejected":      {},
	"human_feedback_added": {},
	"evaluation_completed": {},
	// Structured application logs mirrored from producers (logs+traces unified).
	"log_record": {},
	// Per-match, per-seat agent benchmark fact (decision quality + latency).
	"benchmark_recorded": {},
	// Arena agent events — the behaviour of a competing agent, which is what the
	// developer-facing trace view (/v1/agent-activity) reads and what the arena has
	// been emitting all along.
	//
	// These were MISSING from this list, and the consequence was total: ingest
	// answered 400 "unsupported event_type" for every one of them, the arena's
	// emitter retried three times and dropped the batch, and so not a single agent
	// decision, chat line or connect/disconnect ever reached events_raw. The read
	// path was correct, the query was correct, the ownership gates were correct —
	// and the table was empty, which surfaced to developers as a broken telemetry
	// view they could do nothing about. An allowlist that rejects the platform's own
	// primary producer is the failure mode to watch for here: adding an emitter and
	// adding its type to this map are ONE change, not two.
	"agent_decision":          {},
	"agent_said":              {},
	"agent_say_rejected":      {},
	"agent_connected":         {},
	"agent_disconnected":      {},
	"agent_registered":        {},
	"agent_endpoint_verified": {},
	"agent_endpoint_failed":   {},
	"field_redacted":          {},
	"payload_quarantined":     {},
	"access_denied":           {},
	// Phase 10 — lead loop
	"plan_emitted":        {},
	"synthesis_started":   {},
	"synthesis_completed": {},
	"citation_started":    {},
	"citation_completed":  {},
	// Phase 10 — sub-agents
	"subagent_spawned":             {},
	"subagent_started":             {},
	"subagent_iteration_started":   {},
	"subagent_iteration_completed": {},
	"subagent_report_emitted":      {},
	"subagent_completed":           {},
	// Phase 10 — reducer / code / nlp
	"reducer_started":     {},
	"reducer_completed":   {},
	"code_exec_started":   {},
	"code_exec_completed": {},
	"nlp_task_call":       {},
	// Phase 10 — artifacts
	"artifact_written": {},
	"artifact_read":    {},
	// Pipeline warnings / skips (pyyol-api emits; export must accept)
	"synthesis_skipped_no_evidence": {},
	"citation_skipped_low_evidence": {},
	"tool_warning":                  {},
	"tool_message_truncated":        {},
	"output_contract_pass":          {},
	"output_contract_fail":          {},
	"output_evaluator_started":      {},
	"output_evaluator_completed":    {},
}

var compatibilityEventTypes = map[string]string{
	"http.request.start":  "trace_started",
	"http.request.end":    "trace_completed",
	"http.request.error":  "trace_failed",
	"run.start":           "trace_started",
	"run.complete":        "trace_completed",
	"run.fail":            "trace_failed",
	"run.iteration.start": "span_started",
	"run.iteration.end":   "span_completed",
	"tool.start":          "tool_call_started",
	"tool.result":         "tool_call_completed",
	"tool.error":          "tool_call_failed",
	"llm.call.start":      "model_call_started",
	"llm.call.end":        "model_call_completed",
	"llm.call.error":      "model_call_failed",
	"queue.enqueue":       "span_started",
	"queue.dequeue":       "span_started",
	"queue.complete":      "span_completed",
	"queue.fail":          "span_failed",
	"db.write.start":      "span_started",
	"db.write.end":        "span_completed",
	"db.write.error":      "span_failed",
}

// coalescePhase10FromPayload copies Phase-10 analytics dimensions from
// payload_json into top-level struct fields when those fields are empty but
// the payload carries them (common when pyyol-api nested telemetry for
// reducer/artifact events). Idempotent.
func coalescePhase10FromPayload(e *TelemetryEvent) {
	if e == nil || len(e.PayloadJSON) == 0 {
		return
	}
	p := e.PayloadJSON
	setStr := func(dst *string, keys ...string) {
		if *dst != "" {
			return
		}
		for _, k := range keys {
			if v, ok := p[k]; ok {
				if s, ok2 := v.(string); ok2 && strings.TrimSpace(s) != "" {
					*dst = s
					return
				}
			}
		}
	}
	setStr(&e.TaskKind, "task_kind")
	setStr(&e.ReducerName, "reducer_name")
	setStr(&e.Archetype, "archetype")
	setStr(&e.Scope, "scope")
	setStr(&e.SubagentID, "subagent_id")
	setStr(&e.ParentSubagentID, "parent_subagent_id")
	if len(e.ArtifactIDsIn) == 0 {
		e.ArtifactIDsIn = append([]string(nil), stringSliceFromPayload(p, "artifact_ids_in")...)
	}
	if len(e.ArtifactIDsOut) == 0 {
		e.ArtifactIDsOut = append([]string(nil), stringSliceFromPayload(p, "artifact_ids_out")...)
	}
	if e.OutputBytes == 0 {
		if n, ok := numberFromPayload(p, "byte_size"); ok {
			e.OutputBytes = n
		}
	}
	if e.ReductionRatio == 0 {
		if v, ok := p["reduction_ratio"]; ok {
			switch t := v.(type) {
			case float64:
				e.ReductionRatio = t
			case json.Number:
				if f, err := t.Float64(); err == nil {
					e.ReductionRatio = f
				}
			}
		}
	}
}

func stringSliceFromPayload(p map[string]any, key string) []string {
	v, ok := p[key]
	if !ok || v == nil {
		return nil
	}
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok2 := x.(string); ok2 && s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		var arr []string
		if json.Unmarshal([]byte(t), &arr) == nil {
			return arr
		}
		return nil
	default:
		return nil
	}
}

func numberFromPayload(p map[string]any, key string) (int64, bool) {
	v, ok := p[key]
	if !ok || v == nil {
		return 0, false
	}
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		return int64(t), true
	case json.Number:
		i, err := t.Int64()
		return i, err == nil
	default:
		return 0, false
	}
}

// EnrichTelemetryExportFromPayload mirrors coalescePhase10FromPayload for
// read paths (TraceEvents) that hydrate TelemetryEvent from a partial SQL
// projection. Safe to call on already-normalized rows.
func EnrichTelemetryExportFromPayload(e *TelemetryEvent) {
	coalescePhase10FromPayload(e)
}

func NormalizeAndValidate(e *TelemetryEvent, defaultProject string, defaultEnvironment string, telemetryTextCapRunes int) error {
	if e.EventID == "" {
		e.EventID = uuid.NewString()
	}
	// Hoist Phase-10 dimensions from payload_json into typed fields when the
	// producer only duplicated them in the payload (legacy) or when the HTTP
	// client omitted top-level keys. Keeps events_raw / span rollups aligned.
	coalescePhase10FromPayload(e)
	if mapped, ok := compatibilityEventTypes[e.EventType]; ok {
		e.EventType = mapped
	}
	if e.EventType == "" {
		return errors.New("event_type is required")
	}
	if _, ok := canonicalEventTypes[e.EventType]; !ok {
		return errors.New("unsupported event_type")
	}
	if e.TraceID == "" {
		e.TraceID = e.RequestID
	}
	if e.TraceID == "" {
		// Standalone application logs are legitimately trace-less; give them a
		// self-identifying trace id (their own event id) instead of rejecting, so
		// the "all logs" stream lands. Spans/traces still require correlation.
		if e.EventType == "log_record" {
			e.TraceID = e.EventID
		} else {
			return errors.New("trace_id is required")
		}
	}
	if e.EventTime.IsZero() {
		e.EventTime = time.Now().UTC()
	}
	e.IngestedAt = time.Now().UTC()
	if e.SchemaVersion == "" {
		e.SchemaVersion = CurrentSchemaVersion
	}
	if e.Status == "" {
		switch e.EventType {
		case "trace_failed", "span_failed", "model_call_failed", "tool_call_failed":
			e.Status = "error"
		default:
			e.Status = "ok"
		}
	}
	if e.ProjectID == "" {
		e.ProjectID = defaultProject
	}
	if e.Environment == "" {
		e.Environment = defaultEnvironment
	}
	if e.SourceService == "" {
		if e.Component != "" {
			e.SourceService = e.Component
		} else {
			e.SourceService = "unknown"
		}
	}
	if e.TotalTokens == 0 {
		e.TotalTokens = e.PromptTokens + e.CompletionTokens + e.CachedTokens + e.ReasoningTokens
	}
	if e.LatencyMS == 0 && e.EventType != "trace_started" && e.EventType != "span_started" {
		e.LatencyMS = 0
	}
	e.ErrorMessage = redactString(e.ErrorMessage)
	e.PayloadJSON = redactMap(e.PayloadJSON)
	e.PayloadJSON = redactPayloadByEventType(e.EventType, e.PayloadJSON, telemetryTextCapRunes)
	e.RedactionSummary = redactMap(e.RedactionSummary)
	return nil
}

func JSONString(v any) string {
	if v == nil {
		return ""
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}

// StringSliceJSON marshals a string slice for ClickHouse JSON columns (empty -> "[]").
func StringSliceJSON(s []string) string {
	if s == nil {
		return "[]"
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

// redactString applies a narrow secret detector to free-text fields (e.g. error_message).
// Avoid broad substrings like "token" / "bearer" that appear in normal LLM observability text.
func redactString(input string) string {
	if input == "" {
		return input
	}
	low := strings.ToLower(input)
	if strings.Contains(low, "authorization:") && strings.Contains(low, "bearer") {
		return "[redacted]"
	}
	if strings.Contains(low, "set-cookie:") {
		return "[redacted]"
	}
	if strings.Contains(low, `"password":`) || strings.Contains(low, `'password':`) {
		return "[redacted]"
	}
	if strings.Contains(low, "-----begin") && strings.Contains(low, "private key-----") {
		return "[redacted]"
	}
	return input
}

func isAllowedTelemetryKey(lowerKey string) bool {
	switch lowerKey {
	case "prompt_tokens", "completion_tokens", "total_tokens", "cached_tokens", "reasoning_tokens",
		"input_tokens", "output_tokens", "usage", "token_usage",
		"input_messages", "assistant_output_preview", "parallel_calls", "parallel_tool_results",
		// LLM I/O — full prompt + full response stored verbatim (design choice).
		// These keys bypass string-level redaction (which would blank values
		// containing words like "token" or "bearer" — common in chat content).
		// Auth-secret stripping is still performed by REDACT_KEYS upstream and
		// by per-event-type rules below; the API redactor caps PII separately.
		"prompt", "response", "messages", "completion", "output", "output_text",
		"system_prompt", "user_prompt", "assistant_response",
		"tool_args", "tool_result", "tool_response",
		"data_preview", "messagepreview",
		"final_assistant_markdown", "final_send_latency_ms",
		"artifact_data", "telemetry_truncation", "truncation",
		"success", "error",
		"request_messages", "response_text",
		"method", "path", "controller", "handler",
		// Phase 10 — analytics dimensions (no raw content)
		"task_kind", "archetype", "scope", "subagent_id", "parent_subagent_id",
		"artifact_ids_in", "artifact_ids_out", "evidence_ids_out", "citation_ids_out",
		"reducer_name", "reduction_ratio", "tool_token_savings",
		"budget_iterations_used", "budget_iterations_cap", "budget_tokens_used", "budget_tokens_cap",
		"read_bytes",
		"planned_subagents", "planned_tools", "planner", // plan_emitted
		"parallelcalls",
		"contract_version", "autofix_applied", "drift_count", "repair_mode",
		"deterministic_issues", "issues", "ok", "mode",
		"final_markdown_preview", "evidence_seen_count", "reducer_artifacts_consumed", // synthesis_completed
		"citations_count", "unbound_claims_count", // citation_completed
		"reduced_artifact_id", "raw_artifact_id", "evidence_count", "validation_issues", // reducer
		"language", "input_artifact_ids", "code_size_bytes", "code_hash", "exit_code", // code_exec
		"stdout_preview", "stderr_preview", "sandbox_duration_ms",
		"output_artifact_id", "byte_size", "has_preview", "producer_kind", "producer_name", "json_path",
		"artifact_id", "status", "duration_ms", "tokens_used",
		"retry_of", "attempt", "retry_reason":
		return true
	default:
		return false
	}
}

func redactMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		lowerKey := strings.ToLower(key)
		if isAllowedTelemetryKey(lowerKey) {
			switch typed := value.(type) {
			case map[string]any:
				out[key] = redactMap(typed)
			default:
				out[key] = typed
			}
			continue
		}
		if strings.Contains(lowerKey, "authorization") ||
			lowerKey == "token" ||
			lowerKey == "access_token" ||
			lowerKey == "refresh_token" ||
			lowerKey == "id_token" ||
			strings.Contains(lowerKey, "cookie") ||
			strings.Contains(lowerKey, "password") ||
			strings.Contains(lowerKey, "secret") ||
			strings.Contains(lowerKey, "api_key") {
			out[key] = "[redacted]"
			continue
		}
		// Do NOT blanket-redact the key "code" here — TikTok/API payloads often
		// include legitimate fields named `code` (HTTP status, product codes).
		// Source code for code_exec events is stripped in redactPayloadByEventType.
		switch typed := value.(type) {
		case string:
			out[key] = redactString(typed)
		case map[string]any:
			out[key] = redactMap(typed)
		default:
			out[key] = typed
		}
	}
	return out
}

// redactPayloadByEventType enforces event-specific rules (caps, code stripping).
// telemetryTextCapRunes caps nested "quote" and nlp_task_call free-text (0 = unlimited).
func redactPayloadByEventType(eventType string, m map[string]any, telemetryTextCapRunes int) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	switch eventType {
	case "code_exec_started", "code_exec_completed":
		if _, ok := m["code"]; ok {
			m["code"] = "[redacted]"
		}
	}
	// textCap 0 = unlimited (self-hosted); >0 caps "quote" and nlp_task_call blobs.
	capQuoteAndNLP(m, telemetryTextCapRunes)
	if eventType == "nlp_task_call" {
		capNlpTaskCallFields(m, telemetryTextCapRunes)
	}
	return m
}

func capNlpTaskCallFields(m map[string]any, textCap int) {
	if textCap <= 0 {
		return
	}
	if inp, ok := m["input"].(map[string]any); ok {
		if t, ok := inp["text"].(string); ok {
			inp["text"] = truncateRunes(t, textCap)
		}
	}
	if out, ok := m["output"].(map[string]any); ok {
		if s, ok := out["summary"].(string); ok {
			out["summary"] = truncateRunes(s, textCap)
		}
	}
}

// capQuoteAndNLP shortens quote fields in nested structures when textCap > 0.
func capQuoteAndNLP(m map[string]any, textCap int) {
	if textCap <= 0 {
		return
	}
	for k, v := range m {
		kl := strings.ToLower(k)
		switch tv := v.(type) {
		case string:
			if kl == "quote" {
				m[k] = truncateRunes(tv, textCap)
			}
		case map[string]any:
			capQuoteAndNLP(tv, textCap)
		case []any:
			for _, el := range tv {
				if em, ok := el.(map[string]any); ok {
					capQuoteAndNLP(em, textCap)
				}
			}
		}
	}
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
