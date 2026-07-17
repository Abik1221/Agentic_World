package processor

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/agent-arena/pyyol-lens/backend/internal/config"
	"github.com/agent-arena/pyyol-lens/backend/internal/schema"
	"github.com/agent-arena/pyyol-lens/backend/internal/store"
	"github.com/agent-arena/pyyol-lens/backend/internal/stream"
	"github.com/nats-io/nats.go"
)

type Runner struct {
	Config config.Config
	Store  *store.Store
	Stream *stream.JetStream

	traceState map[string]store.TraceProjection
	spanState  map[string]store.SpanProjection
}

func (r Runner) Run(ctx context.Context) error {
	if r.Stream == nil || r.Store == nil {
		return nil
	}
	if err := r.Stream.EnsureDurableConsumer(); err != nil {
		return err
	}
	sub, err := r.Stream.PullSubscribe()
	if err != nil {
		return err
	}
	r.traceState = map[string]store.TraceProjection{}
	r.spanState = map[string]store.SpanProjection{}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			msgs, err := sub.Fetch(64, nats.MaxWait(2*time.Second))
			if err != nil && err != nats.ErrTimeout {
				log.Printf("processor fetch error: %v", err)
				time.Sleep(500 * time.Millisecond)
				continue
			}
			for _, msg := range msgs {
				if err := r.processMessage(ctx, msg); err != nil {
					log.Printf("processor event failed: %v", err)
					_ = msg.Nak()
					continue
				}
				_ = msg.Ack()
			}
		}
	}
}

type envelope struct {
	IngestionID string                `json:"ingestion_id"`
	Event       schema.TelemetryEvent `json:"event"`
}

func (r *Runner) processMessage(ctx context.Context, msg *nats.Msg) error {
	var env envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		return err
	}
	e := env.Event
	if e.EventID == "" {
		return nil
	}
	seen, err := r.Store.HasProcessedEvent(ctx, e.EventID)
	if err != nil {
		return err
	}
	if seen {
		return nil
	}
	if e.TraceID == "" {
		return nil
	}

	trace := r.traceState[e.TraceID]
	trace = foldTrace(trace, e)
	r.traceState[e.TraceID] = trace
	if err := r.Store.UpsertTraceProjection(ctx, trace); err != nil {
		return err
	}

	if e.SpanID != "" {
		key := e.TraceID + ":" + e.SpanID
		span := r.spanState[key]
		span = foldSpan(span, e)
		r.spanState[key] = span
		if err := r.Store.UpsertSpanProjection(ctx, span); err != nil {
			return err
		}
	}

	if err := r.Store.InsertProjectedEvent(ctx, e); err != nil {
		return err
	}
	if err := r.Store.InsertTokenUsage(ctx, e); err != nil {
		return err
	}
	if err := r.Store.InsertToolCall(ctx, e); err != nil {
		return err
	}
	if err := r.Store.InsertRetrieval(ctx, e); err != nil {
		return err
	}
	if err := r.Store.InsertMatchEvent(ctx, e); err != nil {
		return err
	}
	if err := r.Store.InsertAgentBenchmark(ctx, e); err != nil {
		return err
	}
	if err := r.Store.InsertRollupDelta(ctx, buildRollupDelta(e)); err != nil {
		return err
	}
	if err := r.Store.MarkProcessedEvent(ctx, e.EventID, e.TraceID, env.IngestionID); err != nil {
		return err
	}
	return nil
}

func foldTrace(current store.TraceProjection, e schema.TelemetryEvent) store.TraceProjection {
	now := e.EventTime
	if current.TraceID == "" {
		current = store.TraceProjection{
			TraceID:        e.TraceID,
			StartedAt:      now,
			EndedAt:        now,
			Status:         e.Status,
			OrganizationID: e.OrganizationID,
			ProjectID:      e.ProjectID,
			Environment:    e.Environment,
		}
	}
	if current.RequestID == "" {
		current.RequestID = e.RequestID
	}
	if current.UserID == "" {
		current.UserID = e.UserID
	}
	if current.ActorID == "" {
		current.ActorID = e.ActorID
	}
	if current.SessionID == "" {
		current.SessionID = e.SessionID
	}
	if current.RootInputRef == "" {
		current.RootInputRef = e.RootInputRef
	}
	if current.RootOutputRef == "" {
		current.RootOutputRef = e.RootOutputRef
	}
	if e.EventTime.Before(current.StartedAt) {
		current.StartedAt = e.EventTime
	}
	isTerminal := e.EventType == "trace_completed" || e.EventType == "trace_failed"
	if isTerminal {
		current.Complete = true
		current.EndedAt = e.EventTime
	} else if !current.Complete && e.EventTime.After(current.EndedAt) {
		current.EndedAt = e.EventTime
	}
	if e.Status != "" {
		current.Status = e.Status
	}
	if e.ErrorType != "" {
		current.ErrorType = e.ErrorType
	}
	if e.ErrorMessage != "" {
		current.ErrorMessage = e.ErrorMessage
	}
	current.TotalTokens += e.TotalTokens
	cost := e.ReconciledCost
	if cost == 0 {
		cost = e.EstimatedCost
	}
	current.TotalCost += cost
	if isTerminal {
		if e.LatencyMS > 0 {
			current.LatencyMS = e.LatencyMS
		} else {
			current.LatencyMS = int64(current.EndedAt.Sub(current.StartedAt) / time.Millisecond)
		}
	} else if !current.Complete {
		current.LatencyMS = int64(current.EndedAt.Sub(current.StartedAt) / time.Millisecond)
	}
	current.PromptVersionIDsJSON = schema.JSONString(e.PromptVersionIDs)
	current.ModelConfigJSON = schema.JSONString(e.ModelConfigVersions)
	current.SamplingReason = e.SamplingReason
	current.RedactionJSON = schema.JSONString(e.RedactionSummary)
	return current
}

func foldSpan(current store.SpanProjection, e schema.TelemetryEvent) store.SpanProjection {
	now := e.EventTime
	if current.SpanID == "" {
		current = store.SpanProjection{
			TraceID:      e.TraceID,
			SpanID:       e.SpanID,
			ParentSpanID: e.ParentSpanID,
			StartedAt:    now,
			EndedAt:      now,
			Status:       e.Status,
		}
	}
	if e.ParentSpanID != "" {
		current.ParentSpanID = e.ParentSpanID
	}
	if current.SpanType == "" {
		current.SpanType = e.SpanType
	}
	if current.StepName == "" {
		current.StepName = e.StepName
	}
	if e.EventTime.Before(current.StartedAt) {
		current.StartedAt = e.EventTime
	}
	if e.EventTime.After(current.EndedAt) {
		current.EndedAt = e.EventTime
	}
	if e.Status != "" {
		current.Status = e.Status
	}
	if current.InputRef == "" {
		current.InputRef = e.RootInputRef
	}
	if current.OutputRef == "" {
		current.OutputRef = e.RootOutputRef
	}
	if e.ErrorType != "" {
		current.ErrorType = e.ErrorType
	}
	if e.ErrorMessage != "" {
		current.ErrorMessage = e.ErrorMessage
	}
	if e.Provider != "" {
		current.Provider = e.Provider
	}
	if e.Model != "" {
		current.Model = e.Model
	}
	if e.ModelVersion != "" {
		current.ModelVersion = e.ModelVersion
	}
	if e.ToolName != "" {
		current.ToolName = e.ToolName
	}
	if e.ToolVersion != "" {
		current.ToolVersion = e.ToolVersion
	}
	if e.TaskKind != "" {
		current.TaskKind = e.TaskKind
	}
	if e.Archetype != "" {
		current.Archetype = e.Archetype
	}
	if e.Scope != "" {
		current.Scope = e.Scope
	}
	if e.SubagentID != "" {
		current.SubagentID = e.SubagentID
	}
	if e.ParentSubagentID != "" {
		current.ParentSubagentID = e.ParentSubagentID
	}
	if j := schema.StringSliceJSON(e.ArtifactIDsIn); j != "[]" {
		current.ArtifactIDsInJSON = j
	}
	if j := schema.StringSliceJSON(e.ArtifactIDsOut); j != "[]" {
		current.ArtifactIDsOutJSON = j
	}
	if j := schema.StringSliceJSON(e.EvidenceIDsOut); j != "[]" {
		current.EvidenceIDsOutJSON = j
	}
	if j := schema.StringSliceJSON(e.CitationIDsOut); j != "[]" {
		current.CitationIDsOutJSON = j
	}
	if e.ReducerName != "" {
		current.ReducerName = e.ReducerName
	}
	if e.ReductionRatio > 0 || e.EventType == "reducer_completed" {
		current.ReductionRatio = e.ReductionRatio
	}
	if e.ToolTokenSavings != 0 {
		current.ToolTokenSavings = e.ToolTokenSavings
	}
	if e.BudgetIterationsUsed != 0 {
		current.BudgetIterationsUsed = e.BudgetIterationsUsed
	}
	if e.BudgetIterationsCap != 0 {
		current.BudgetIterationsCap = e.BudgetIterationsCap
	}
	if e.BudgetTokensUsed != 0 {
		current.BudgetTokensUsed = e.BudgetTokensUsed
	}
	if e.BudgetTokensCap != 0 {
		current.BudgetTokensCap = e.BudgetTokensCap
	}
	if e.EventType != "" {
		current.LastEventType = e.EventType
	}
	current.TotalTokens += e.TotalTokens
	cost := e.ReconciledCost
	if cost == 0 {
		cost = e.EstimatedCost
	}
	current.TotalCost += cost
	current.LatencyMS = int64(current.EndedAt.Sub(current.StartedAt) / time.Millisecond)
	return current
}

func buildRollupDelta(e schema.TelemetryEvent) store.RollupDelta {
	cost := e.ReconciledCost
	if cost == 0 {
		cost = e.EstimatedCost
	}
	var tracesInc int64
	if e.EventType == "trace_started" {
		tracesInc = 1
	}
	var errorsInc int64
	if e.EventType == "trace_failed" || e.Status == "error" {
		errorsInc = 1
	}
	return store.RollupDelta{
		OrganizationID: e.OrganizationID,
		ProjectID:      e.ProjectID,
		Environment:    e.Environment,
		EventTime:      e.EventTime,
		TracesTotal:    tracesInc,
		ErrorsTotal:    errorsInc,
		TokensTotal:    e.TotalTokens,
		EstimatedCost:  cost,
	}
}
