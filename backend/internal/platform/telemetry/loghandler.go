package telemetry

import (
	"context"
	"log/slog"
)

// LogHandler is a slog.Handler middleware that mirrors log records into Pyyol
// Lens as `log_record` telemetry events, while still delegating to the base
// handler (stdout JSON) unchanged. This gives the "all logs, correlated with
// traces" half of observability without changing a single log call site.
//
// It is level-gated (MinLevel, default WARN) so the default footprint is errors
// + warnings; set PYYOL_LENS_LOG_LEVEL=info/debug to widen. When a log call
// carries a telemetry span in its context (slog.*Context APIs), the emitted
// record is correlated to that trace/span; otherwise it lands uncorrelated.
type LogHandler struct {
	base     slog.Handler
	em       *Client
	minLevel slog.Level
	attrs    []slog.Attr
	groups   []string
}

// NewLogHandler wraps base so records at >= minLevel are also emitted to Lens.
// If em is nil/disabled, it returns base unchanged (no overhead).
func NewLogHandler(base slog.Handler, em *Client, minLevel slog.Level) slog.Handler {
	if em == nil || !em.Enabled() {
		return base
	}
	return &LogHandler{base: base, em: em, minLevel: minLevel}
}

func (h *LogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *LogHandler) Handle(ctx context.Context, r slog.Record) error {
	// Always deliver to the base handler first (never lose a stdout log).
	err := h.base.Handle(ctx, r)
	if r.Level < h.minLevel {
		return err
	}
	h.emit(ctx, r)
	return err
}

func (h *LogHandler) emit(ctx context.Context, r slog.Record) {
	payload := map[string]any{"level": r.Level.String()}
	for _, a := range h.attrs {
		payload[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool {
		payload[a.Key] = a.Value.Any()
		return true
	})

	ev := Event{
		EventType:    EventLogRecord,
		EventTime:    r.Time,
		StepName:     r.Message,
		SpanType:     "log",
		Status:       levelStatus(r.Level),
		ErrorMessage: errText(r),
		PayloadJSON:  payload,
	}
	// Correlate to the active span/trace when the caller used a *Context log API.
	if st, ok := spanFromContext(ctx); ok {
		ev.TraceID = st.traceID
		ev.RequestID = st.requestID
		ev.ParentSpanID = st.spanID
		ev.RunID = st.matchID
		ev.ActorID = st.agentID
		ev.SessionID = st.arena
	}
	h.em.EmitEvent(ev)
}

func (h *LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LogHandler{
		base:     h.base.WithAttrs(attrs),
		em:       h.em,
		minLevel: h.minLevel,
		attrs:    append(append([]slog.Attr(nil), h.attrs...), attrs...),
		groups:   h.groups,
	}
}

func (h *LogHandler) WithGroup(name string) slog.Handler {
	return &LogHandler{
		base:     h.base.WithGroup(name),
		em:       h.em,
		minLevel: h.minLevel,
		attrs:    h.attrs,
		groups:   append(append([]string(nil), h.groups...), name),
	}
}

func levelStatus(l slog.Level) string {
	if l >= slog.LevelError {
		return "error"
	}
	return "ok"
}

func errText(r slog.Record) string {
	if r.Level < slog.LevelError {
		return ""
	}
	return r.Message
}
