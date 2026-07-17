package telemetry

import (
	"context"
	"time"
)

// This file adds ergonomic, context-propagating span helpers on top of the raw
// EmitEvent path so instrumentation reads as `ctx, done := em.StartSpan(...); ...
// done(err)`. Trace/parent linkage flows through context.Context (Go has no
// async-local storage), producing the nested waterfall in the Pyyol Lens UI.

// MatchTraceID derives the stable trace id for a match so every producer of that
// match's telemetry — the domain-event bridge (match.started/finished) and the
// per-decision gateway spans — lands in ONE trace without threading ids around.
func MatchTraceID(matchPublicID string) string {
	if matchPublicID == "" {
		return ""
	}
	return "match_" + matchPublicID
}

// Attrs are the optional dimensions a span carries. Everything is optional; set
// what the call site knows. Arena nouns are mapped onto the generic Pyyol Lens
// slots (see Event) plus payload_json for the rest.
type Attrs struct {
	SpanType  string // e.g. "agent_call", "match", "round"
	Operation string
	Provider  string
	Model     string
	ToolName  string

	// Identity (arena → generic Lens slots).
	Developer string // user_id
	AgentID   string // actor_id
	Arena     string // session_id
	MatchID   string // run_id
	Game      string
	Mode      string // ranked / sandbox / cert

	Payload map[string]any
}

type spanCtxKey struct{}

// spanState is the active span carried in context so nested StartSpan calls link
// to their parent automatically.
type spanState struct {
	traceID   string
	spanID    string
	requestID string
	// inherited identity so children need not repeat it
	developer string
	agentID   string
	arena     string
	matchID   string
	game      string
	mode      string
}

func spanFromContext(ctx context.Context) (spanState, bool) {
	if ctx == nil {
		return spanState{}, false
	}
	s, ok := ctx.Value(spanCtxKey{}).(spanState)
	return s, ok
}

// WithTrace seeds a context with an explicit trace id (e.g. MatchTraceID) so
// spans started under it join that trace. Useful at a match/request boundary.
func WithTrace(ctx context.Context, traceID string, a Attrs) context.Context {
	st := spanState{
		traceID:   traceID,
		requestID: traceID,
		developer: a.Developer,
		agentID:   a.AgentID,
		arena:     a.Arena,
		matchID:   a.MatchID,
		game:      a.Game,
		mode:      a.Mode,
	}
	return context.WithValue(ctx, spanCtxKey{}, st)
}

// EndFunc finishes a span, emitting span_completed (err==nil) or span_failed.
type EndFunc func(err error)

// StartSpan emits span_started and returns a child context + finisher. On a nil
// or disabled client it is a cheap no-op that still returns a usable ctx. Trace
// id resolves from (in order): Attrs.MatchID→MatchTraceID, parent context, a new
// id. The returned ctx carries the new span so nested StartSpan calls nest.
func (c *Client) StartSpan(ctx context.Context, name string, a Attrs) (context.Context, EndFunc) {
	if c == nil || !c.enabled {
		return ctx, func(error) {}
	}
	parent, hasParent := spanFromContext(ctx)

	traceID := ""
	switch {
	case hasParent && parent.traceID != "":
		traceID = parent.traceID
	case a.MatchID != "":
		traceID = MatchTraceID(a.MatchID)
	default:
		traceID = newID()
	}
	requestID := traceID
	if hasParent && parent.requestID != "" {
		requestID = parent.requestID
	}
	spanID := newID()
	parentSpanID := ""
	if hasParent {
		parentSpanID = parent.spanID
	}

	// Inherit identity from parent when the call site didn't set it.
	inherit := func(v, p string) string {
		if v != "" {
			return v
		}
		return p
	}
	dev := inherit(a.Developer, parent.developer)
	agent := inherit(a.AgentID, parent.agentID)
	arena := inherit(a.Arena, parent.arena)
	matchID := inherit(a.MatchID, parent.matchID)
	game := inherit(a.Game, parent.game)
	mode := inherit(a.Mode, parent.mode)

	spanType := a.SpanType
	if spanType == "" {
		spanType = "operation"
	}

	base := Event{
		TraceID:      traceID,
		RequestID:    requestID,
		SpanID:       spanID,
		ParentSpanID: parentSpanID,
		SpanType:     spanType,
		StepName:     name,
		Operation:    a.Operation,
		Provider:     a.Provider,
		Model:        a.Model,
		ToolName:     a.ToolName,
		UserID:       dev,
		ActorID:      agent,
		SessionID:    arena,
		RunID:        matchID,
		PayloadJSON:  mergeGameMode(a.Payload, game, mode),
	}

	started := base
	started.EventType = EventSpanStarted
	c.EmitEvent(started)

	start := time.Now()
	child := context.WithValue(ctx, spanCtxKey{}, spanState{
		traceID: traceID, spanID: spanID, requestID: requestID,
		developer: dev, agentID: agent, arena: arena, matchID: matchID, game: game, mode: mode,
	})

	done := func(err error) {
		end := base
		end.LatencyMS = time.Since(start).Milliseconds()
		if err != nil {
			end.EventType = EventSpanFailed
			end.Status = "error"
			end.ErrorMessage = err.Error()
			end.ErrorType = "error"
		} else {
			end.EventType = EventSpanCompleted
			end.Status = "ok"
		}
		c.EmitEvent(end)
	}
	return child, done
}

func mergeGameMode(p map[string]any, game, mode string) map[string]any {
	if game == "" && mode == "" {
		return p
	}
	out := map[string]any{}
	for k, v := range p {
		out[k] = v
	}
	if game != "" {
		out["game"] = game
	}
	if mode != "" {
		out["mode"] = mode
	}
	return out
}

// EventType constants for canonical Pyyol Lens events emitted by the engine.
const (
	EventTraceStarted   = "trace_started"
	EventTraceCompleted = "trace_completed"
	EventTraceFailed    = "trace_failed"
	EventSpanStarted    = "span_started"
	EventSpanCompleted  = "span_completed"
	EventSpanFailed     = "span_failed"
	EventLogRecord      = "log_record"
)
