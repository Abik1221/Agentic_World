package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/agent-arena/pyyol-lens/backend/internal/config"
	"github.com/agent-arena/pyyol-lens/backend/internal/schema"
)

type Store struct {
	DB *sql.DB
}

type TraceProjection struct {
	TraceID              string
	RequestID            string
	OrganizationID       string
	ProjectID            string
	Environment          string
	UserID               string
	ActorID              string
	SessionID            string
	StartedAt            time.Time
	EndedAt              time.Time
	Status               string
	RootInputRef         string
	RootOutputRef        string
	TotalTokens          int64
	TotalCost            float64
	LatencyMS            int64
	ErrorType            string
	ErrorMessage         string
	WorkflowVersion      string
	PromptVersionIDsJSON string
	ModelConfigJSON      string
	SamplingReason       string
	RedactionJSON        string
	// Complete is in-processor fold state only (not persisted): after trace_completed/trace_failed,
	// do not extend EndedAt from delayed stray events (e.g. sampled artifact_read).
	Complete bool
}

type SpanProjection struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	SpanType     string
	StepName     string
	Status       string
	StartedAt    time.Time
	EndedAt      time.Time
	LatencyMS    int64
	InputRef     string
	OutputRef    string
	ErrorType    string
	ErrorMessage string
	Provider     string
	Model        string
	ModelVersion string
	ToolName     string
	ToolVersion  string
	TotalTokens  int64
	TotalCost    float64
	// Phase 10
	TaskKind             string
	Archetype            string
	Scope                string
	SubagentID           string
	ParentSubagentID     string
	ArtifactIDsInJSON    string
	ArtifactIDsOutJSON   string
	EvidenceIDsOutJSON   string
	CitationIDsOutJSON   string
	ReducerName          string
	ReductionRatio       float64
	ToolTokenSavings     int64
	BudgetIterationsUsed int64
	BudgetIterationsCap  int64
	BudgetTokensUsed     int64
	BudgetTokensCap      int64
	LastEventType        string
}

type RollupDelta struct {
	OrganizationID string
	ProjectID      string
	Environment    string
	EventTime      time.Time
	TracesTotal    int64
	ErrorsTotal    int64
	TokensTotal    int64
	EstimatedCost  float64
}

func New(cfg config.Config) (*Store, error) {
	dsn := fmt.Sprintf("clickhouse://%s:%s@%s/%s", cfg.CHUser, cfg.CHPass, cfg.CHAddr, cfg.CHDB)
	db, err := sql.Open("clickhouse", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return &Store{DB: db}, nil
}

func (s *Store) InsertRawEvents(ctx context.Context, ingestionID string, events []schema.TelemetryEvent) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO events_raw (
		ingestion_id,event_id,trace_id,request_id,span_id,parent_span_id,event_type,event_time,ingested_at,
		sequence_number,source_service,schema_version,status,organization_id,project_id,environment,user_id,actor_id,
		session_id,run_id,conversation_id,app_id,queue_job_id,component,operation,span_type,
		step_name,provider,model,model_version,tool_name,tool_version,root_input_ref,root_output_ref,payload_ref,
		payload_json,prompt_version_ids_json,model_config_versions_json,latency_ms,input_bytes,output_bytes,prompt_tokens,
		completion_tokens,cached_tokens,reasoning_tokens,total_tokens,estimated_cost,reconciled_cost,currency,pricing_version,
		meter_source,error_type,error_code,error_message,sampling_reason,redaction_summary_json,
		task_kind,archetype,scope,subagent_id,parent_subagent_id,artifact_ids_in_json,artifact_ids_out_json,
		evidence_ids_out_json,citation_ids_out_json,reducer_name,reduction_ratio,tool_token_savings,
		budget_iterations_used,budget_iterations_cap,budget_tokens_used,budget_tokens_cap
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,
		?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, e := range events {
		if _, err := stmt.ExecContext(
			ctx,
			ingestionID, e.EventID, e.TraceID, e.RequestID, e.SpanID, e.ParentSpanID, e.EventType, e.EventTime, e.IngestedAt,
			e.SequenceNumber, e.SourceService, e.SchemaVersion, e.Status, e.OrganizationID, e.ProjectID, e.Environment,
			e.UserID, e.ActorID, e.SessionID, e.RunID, e.ConversationID, e.AppID, e.QueueJobID,
			e.Component, e.Operation, e.SpanType, e.StepName, e.Provider, e.Model, e.ModelVersion, e.ToolName, e.ToolVersion,
			e.RootInputRef, e.RootOutputRef, e.PayloadRef, schema.JSONString(e.PayloadJSON), schema.JSONString(e.PromptVersionIDs),
			schema.JSONString(e.ModelConfigVersions), e.LatencyMS, e.InputBytes, e.OutputBytes, e.PromptTokens, e.CompletionTokens,
			e.CachedTokens, e.ReasoningTokens, e.TotalTokens, e.EstimatedCost, e.ReconciledCost, e.Currency, e.PricingVersion,
			e.MeterSource, e.ErrorType, e.ErrorCode, e.ErrorMessage, e.SamplingReason, schema.JSONString(e.RedactionSummary),
			e.TaskKind, e.Archetype, e.Scope, e.SubagentID, e.ParentSubagentID,
			schema.StringSliceJSON(e.ArtifactIDsIn), schema.StringSliceJSON(e.ArtifactIDsOut), schema.StringSliceJSON(e.EvidenceIDsOut), schema.StringSliceJSON(e.CitationIDsOut),
			e.ReducerName, e.ReductionRatio, e.ToolTokenSavings,
			e.BudgetIterationsUsed, e.BudgetIterationsCap, e.BudgetTokensUsed, e.BudgetTokensCap,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpsertTraceProjection(ctx context.Context, p TraceProjection) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO traces (
		trace_id,request_id,organization_id,project_id,environment,user_id,actor_id,session_id,started_at,ended_at,status,
		root_input_ref,root_output_ref,total_tokens,total_cost,latency_ms,error_type,error_message,workflow_version,
		prompt_version_ids_json,model_config_versions_json,sampling_reason,redaction_summary_json,updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.TraceID, p.RequestID, p.OrganizationID, p.ProjectID, p.Environment, p.UserID, p.ActorID, p.SessionID,
		p.StartedAt, p.EndedAt, p.Status, p.RootInputRef, p.RootOutputRef, p.TotalTokens, p.TotalCost, p.LatencyMS,
		p.ErrorType, p.ErrorMessage, p.WorkflowVersion, p.PromptVersionIDsJSON, p.ModelConfigJSON, p.SamplingReason,
		p.RedactionJSON, time.Now().UTC(),
	)
	return err
}

func (s *Store) UpsertSpanProjection(ctx context.Context, p SpanProjection) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO spans (
		trace_id,span_id,parent_span_id,span_type,step_name,status,started_at,ended_at,latency_ms,input_ref,output_ref,error_type,error_message,
		provider,model,model_version,tool_name,tool_version,total_tokens,total_cost,updated_at,
		task_kind,archetype,scope,subagent_id,parent_subagent_id,artifact_ids_in_json,artifact_ids_out_json,
		evidence_ids_out_json,citation_ids_out_json,reducer_name,reduction_ratio,tool_token_savings,
		budget_iterations_used,budget_iterations_cap,budget_tokens_used,budget_tokens_cap,last_event_type
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,
		?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.TraceID, p.SpanID, p.ParentSpanID, p.SpanType, p.StepName, p.Status, p.StartedAt, p.EndedAt, p.LatencyMS,
		p.InputRef, p.OutputRef, p.ErrorType, p.ErrorMessage, p.Provider, p.Model, p.ModelVersion, p.ToolName, p.ToolVersion,
		p.TotalTokens, p.TotalCost, time.Now().UTC(),
		p.TaskKind, p.Archetype, p.Scope, p.SubagentID, p.ParentSubagentID, p.ArtifactIDsInJSON, p.ArtifactIDsOutJSON,
		p.EvidenceIDsOutJSON, p.CitationIDsOutJSON, p.ReducerName, p.ReductionRatio, p.ToolTokenSavings,
		p.BudgetIterationsUsed, p.BudgetIterationsCap, p.BudgetTokensUsed, p.BudgetTokensCap, p.LastEventType,
	)
	return err
}

// ReplayRequest is metadata for a queued replay (collector-side record only; execution is producer-side).
type ReplayRequest struct {
	ReplayID       string
	TraceID        string
	RunID          string
	Mode           string
	Environment    string
	OrganizationID string
	ReplayOf       string
	DerivedFrom    []string
	ReducerName    string
	RequestJSON    string
}

// InsertReplay records a Phase 10 replay request in ClickHouse.
func (s *Store) InsertReplay(ctx context.Context, r ReplayRequest) error {
	derived := schema.StringSliceJSON(r.DerivedFrom)
	env := r.Environment
	if env == "" {
		env = "development"
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO replays (
		replay_id,trace_id,mode,prompt_version,model_config_version,retrieval_config,tool_config,environment,status,
		outcome_ref,diff_ref,created_at,run_id,replay_of,derived_from_json,reducer_name,request_json,organization_id
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ReplayID, r.TraceID, r.Mode, "", "", "", "", env, "queued", "", "", time.Now().UTC(),
		r.RunID, r.ReplayOf, derived, r.ReducerName, r.RequestJSON, r.OrganizationID,
	)
	return err
}

func (s *Store) InsertProjectedEvent(ctx context.Context, e schema.TelemetryEvent) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO events (
		trace_id,span_id,event_id,event_type,event_time,status,source_service,payload_ref,error_type,error_message
	) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		e.TraceID, e.SpanID, e.EventID, e.EventType, e.EventTime, e.Status, e.SourceService, e.PayloadRef, e.ErrorType, e.ErrorMessage,
	)
	return err
}

func (s *Store) InsertTokenUsage(ctx context.Context, e schema.TelemetryEvent) error {
	if e.TotalTokens == 0 && e.EstimatedCost == 0 && e.ReconciledCost == 0 {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO token_usage (
		trace_id,span_id,project_id,environment,provider,model,model_version,prompt_tokens,completion_tokens,cached_tokens,reasoning_tokens,total_tokens,
		estimated_cost,reconciled_cost,currency,pricing_version,meter_source,event_time
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.TraceID, e.SpanID, e.ProjectID, e.Environment, e.Provider, e.Model, e.ModelVersion, e.PromptTokens, e.CompletionTokens, e.CachedTokens,
		e.ReasoningTokens, e.TotalTokens, e.EstimatedCost, e.ReconciledCost, e.Currency, e.PricingVersion, e.MeterSource, e.EventTime,
	)
	return err
}

func (s *Store) InsertToolCall(ctx context.Context, e schema.TelemetryEvent) error {
	if e.ToolName == "" {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO tool_calls (
		trace_id,span_id,tool_name,tool_version,status,event_time,error_message,payload_ref,total_tokens,estimated_cost
	) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		e.TraceID, e.SpanID, e.ToolName, e.ToolVersion, e.Status, e.EventTime, e.ErrorMessage, e.PayloadRef, e.TotalTokens, e.EstimatedCost,
	)
	return err
}

func (s *Store) InsertRetrieval(ctx context.Context, e schema.TelemetryEvent) error {
	if e.EventType != "retrieval_started" && e.EventType != "retrieval_completed" && e.EventType != "rerank_started" && e.EventType != "rerank_completed" {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO retrievals (
		trace_id,span_id,step_name,status,event_time,payload_ref,error_message
	) VALUES (?,?,?,?,?,?,?)`,
		e.TraceID, e.SpanID, e.StepName, e.Status, e.EventTime, e.PayloadRef, e.ErrorMessage,
	)
	return err
}

// InsertMatchEvent appends to the per-match event stream (keyed by run_id = match
// id). No-op for events with no run_id (not match-scoped).
func (s *Store) InsertMatchEvent(ctx context.Context, e schema.TelemetryEvent) error {
	if e.RunID == "" {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO match_events (
		run_id,trace_id,event_type,event_time,status,error_message
	) VALUES (?,?,?,?,?,?)`,
		e.RunID, e.TraceID, e.EventType, e.EventTime, e.Status, e.ErrorMessage,
	)
	return err
}

func (s *Store) HasProcessedEvent(ctx context.Context, eventID string) (bool, error) {
	var one int
	err := s.DB.QueryRowContext(
		ctx,
		`SELECT 1 FROM processed_events WHERE event_id = ? LIMIT 1`,
		eventID,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) MarkProcessedEvent(ctx context.Context, eventID string, traceID string, ingestionID string) error {
	_, err := s.DB.ExecContext(
		ctx,
		`INSERT INTO processed_events (event_id,trace_id,ingestion_id,processed_at) VALUES (?,?,?,?)`,
		eventID, traceID, ingestionID, time.Now().UTC(),
	)
	return err
}

func (s *Store) InsertRollupDelta(ctx context.Context, d RollupDelta) error {
	if d.OrganizationID == "" || d.ProjectID == "" || d.Environment == "" {
		return nil
	}
	hourBucket := d.EventTime.UTC().Truncate(time.Hour)
	dayBucket := time.Date(
		d.EventTime.Year(), d.EventTime.Month(), d.EventTime.Day(),
		0, 0, 0, 0, time.UTC,
	)
	monthBucket := time.Date(
		d.EventTime.Year(), d.EventTime.Month(), 1,
		0, 0, 0, 0, time.UTC,
	)

	if _, err := s.DB.ExecContext(
		ctx,
		`INSERT INTO rollup_hourly (bucket_start,organization_id,project_id,environment,traces_total,errors_total,tokens_total,estimated_cost)
		 VALUES (?,?,?,?,?,?,?,?)`,
		hourBucket, d.OrganizationID, d.ProjectID, d.Environment, d.TracesTotal, d.ErrorsTotal, d.TokensTotal, d.EstimatedCost,
	); err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(
		ctx,
		`INSERT INTO rollup_daily (bucket_start,organization_id,project_id,environment,traces_total,errors_total,tokens_total,estimated_cost)
		 VALUES (?,?,?,?,?,?,?,?)`,
		dayBucket, d.OrganizationID, d.ProjectID, d.Environment, d.TracesTotal, d.ErrorsTotal, d.TokensTotal, d.EstimatedCost,
	); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(
		ctx,
		`INSERT INTO rollup_monthly (bucket_start,organization_id,project_id,environment,traces_total,errors_total,tokens_total,estimated_cost)
		 VALUES (?,?,?,?,?,?,?,?)`,
		monthBucket, d.OrganizationID, d.ProjectID, d.Environment, d.TracesTotal, d.ErrorsTotal, d.TokensTotal, d.EstimatedCost,
	)
	return err
}
