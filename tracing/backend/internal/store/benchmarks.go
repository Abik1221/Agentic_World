package store

import (
	"context"
	"time"

	"github.com/agent-arena/pyyol-lens/backend/internal/schema"
)

// BenchmarkRow is one agent's decision-quality aggregate for a match (a single
// benchmark_recorded event), keyed for the SummingMergeTree rollup. matches=1 per
// row so summing counts matches played.
type BenchmarkRow struct {
	BucketStart     time.Time
	OrganizationID  string
	ProjectID       string
	Environment     string
	Game            string
	Mode            string
	AgentID         string
	AgentVersion    string
	Provider        string
	Model           string
	Matches         int64
	Decisions       int64
	Legal           int64
	Illegal         int64
	Timeouts        int64
	TransportErrors int64
	Disconnects     int64
	Errors          int64
	Fallbacks       int64
	LatencySumMS    int64
	Wins            int64
	Losses          int64
	Draws           int64
}

// BenchmarkRowFromEvent extracts a benchmark rollup row from a benchmark_recorded
// event. Returns ok=false for any other event type. Pure (no I/O) so the mapping
// is unit-tested directly. Reads typed identity from the event and the per-seat
// counters from payload_json (emitted by the arena's benchmark.Emit).
func BenchmarkRowFromEvent(e schema.TelemetryEvent) (BenchmarkRow, bool) {
	if e.EventType != "benchmark_recorded" || e.ActorID == "" {
		return BenchmarkRow{}, false
	}
	p := e.PayloadJSON
	row := BenchmarkRow{
		BucketStart:     dayBucketUTC(e.EventTime),
		OrganizationID:  e.OrganizationID,
		ProjectID:       e.ProjectID,
		Environment:     e.Environment,
		Game:            firstNonEmpty(payloadStr(p, "game"), e.SessionID),
		Mode:            payloadStr(p, "mode"),
		AgentID:         e.ActorID,
		AgentVersion:    payloadStr(p, "agent_version"),
		Provider:        firstNonEmpty(payloadStr(p, "provider"), e.Provider),
		Model:           firstNonEmpty(payloadStr(p, "model"), e.Model),
		Matches:         1,
		Decisions:       payloadInt(p, "decisions"),
		Legal:           payloadInt(p, "legal"),
		Illegal:         payloadInt(p, "illegal"),
		Timeouts:        payloadInt(p, "timeouts"),
		TransportErrors: payloadInt(p, "transport_errors"),
		Disconnects:     payloadInt(p, "disconnects"),
		Errors:          payloadInt(p, "errors"),
		Fallbacks:       payloadInt(p, "fallbacks"),
		LatencySumMS:    payloadInt(p, "latency_sum_ms"),
		Wins:            payloadInt(p, "wins"),
		Losses:          payloadInt(p, "losses"),
		Draws:           payloadInt(p, "draws"),
	}
	return row, true
}

// InsertAgentBenchmark rolls a benchmark_recorded event into agent_benchmarks.
// No-op for other event types.
func (s *Store) InsertAgentBenchmark(ctx context.Context, e schema.TelemetryEvent) error {
	row, ok := BenchmarkRowFromEvent(e)
	if !ok {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO agent_benchmarks (
		bucket_start,organization_id,project_id,environment,game,mode,agent_id,agent_version,provider,model,
		matches,decisions,legal,illegal,timeouts,transport_errors,disconnects,errors,fallbacks,latency_sum_ms,
		wins,losses,draws
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		row.BucketStart, row.OrganizationID, row.ProjectID, row.Environment, row.Game, row.Mode, row.AgentID, row.AgentVersion, row.Provider, row.Model,
		row.Matches, row.Decisions, row.Legal, row.Illegal, row.Timeouts, row.TransportErrors,
		row.Disconnects, row.Errors, row.Fallbacks, row.LatencySumMS,
		row.Wins, row.Losses, row.Draws,
	)
	return err
}

func dayBucketUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func payloadStr(p map[string]any, key string) string {
	if p == nil {
		return ""
	}
	if s, ok := p[key].(string); ok {
		return s
	}
	return ""
}

// payloadInt coerces a JSON number (float64/json.Number/int) to int64; 0 if absent.
func payloadInt(p map[string]any, key string) int64 {
	if p == nil {
		return 0
	}
	switch v := p[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	default:
		return 0
	}
}
