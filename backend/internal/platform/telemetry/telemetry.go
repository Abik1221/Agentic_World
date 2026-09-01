// Package telemetry is the arena's Pyyol Lens emitter: a non-blocking, batched,
// retrying client that ships trace/span/log events to the Pyyol Lens ingest API
// (POST /v1/events/batch, authenticated with X-Pyyol-Key).
//
// Design rules (mirrors tracing/PLAN.md "never block the app critical path"):
//   - Emit is fire-and-forget and NEVER blocks the caller. On a full buffer the
//     event is dropped and counted (drops are a metric, not an error).
//   - Telemetry failures are swallowed; the match/agent path is unaffected.
//   - When Enabled is false, everything is a cheap no-op — New still returns a
//     usable *Client so call sites need no nil checks.
//
// It lives under platform/ (leaf, imports nothing from internal/*), so any module
// may take a *Client without creating an import cycle.
package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SchemaVersion is the Pyyol Lens event schema this client emits against.
const SchemaVersion = "2026-04-17"

// Priority controls sampling. High-priority events bypass trace sampling and are
// always kept (subject only to a full buffer): failures, trace lifecycle, and
// benchmark facts must survive even when the boring middle is thinned.
type Priority int

const (
	// PriorityAuto (zero value) lets the client classify by event type/status.
	PriorityAuto Priority = iota
	// PriorityHigh forces keep regardless of sampling.
	PriorityHigh
)

// Meter sources — the provenance of a model-call's token/cost numbers, recorded
// structurally on Event.MeterSource so the backend can compute verified-only economics.
const (
	MeterSourceSDK     = "sdk"     // agent self-reported (sandbox / unverified tier)
	MeterSourceGateway = "gateway" // server-observed via the Pyyol Gateway (verified tier)
)

// CurrencyUSD is the cost currency the pricing table produces.
const CurrencyUSD = "USD"

// Event is one telemetry fact on the wire. Field names match the Pyyol Lens
// ingest JSON; zero-value fields are defaulted server-side, so callers set only
// what they know. This struct is intentionally a SUBSET of the full Pyyol Lens
// schema — the dimensions the arena actually produces.
type Event struct {
	EventID        string    `json:"event_id,omitempty"`
	TraceID        string    `json:"trace_id"`
	RequestID      string    `json:"request_id,omitempty"`
	SpanID         string    `json:"span_id,omitempty"`
	ParentSpanID   string    `json:"parent_span_id,omitempty"`
	EventType      string    `json:"event_type"`
	EventTime      time.Time `json:"event_time"`
	SourceService  string    `json:"source_service,omitempty"`
	SchemaVersion  string    `json:"schema_version,omitempty"`
	Status         string    `json:"status,omitempty"`
	OrganizationID string    `json:"organization_id,omitempty"`
	ProjectID      string    `json:"project_id,omitempty"`
	Environment    string    `json:"environment,omitempty"`
	// Identity mapped onto Pyyol Lens generic slots so its UI/analytics work
	// unchanged: user_id=developer, actor_id=agent, session_id=arena, run_id=match.
	UserID    string `json:"user_id,omitempty"`
	ActorID   string `json:"actor_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	Component string `json:"component,omitempty"`
	Operation string `json:"operation,omitempty"`
	SpanType  string `json:"span_type,omitempty"`
	StepName  string `json:"step_name,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	ToolName  string `json:"tool_name,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`

	PromptTokens     int64   `json:"prompt_tokens,omitempty"`
	CompletionTokens int64   `json:"completion_tokens,omitempty"`
	CachedTokens     int64   `json:"cached_tokens,omitempty"`
	ReasoningTokens  int64   `json:"reasoning_tokens,omitempty"`
	TotalTokens      int64   `json:"total_tokens,omitempty"`
	EstimatedCost    float64 `json:"estimated_cost,omitempty"`
	// Currency/PricingVersion make a cost reproducible; MeterSource records provenance
	// — "gateway" (server-observed, unfakeable) vs "sdk" (agent self-reported) — so the
	// backend can compute verified-only economics straight from structural columns.
	// These map 1:1 onto the Pyyol Lens schema columns of the same json name.
	Currency       string `json:"currency,omitempty"`
	PricingVersion string `json:"pricing_version,omitempty"`
	MeterSource    string `json:"meter_source,omitempty"`

	// AgentKind is WHOSE traffic this span is: `external` (a developer's agent) or
	// `harness` (the platform's own benchmark). It is the same discriminator the model
	// boards use, carried onto the span so Lens can separate the two.
	//
	// It exists because the platform harness plays REAL matches through the REAL gateway.
	// That is deliberate — a benchmark on a private code path would measure the private
	// code path — but it means a benchmark run's spans land in the same traces a developer
	// debugging their own agent is looking at. Without a discriminator, an operator cannot
	// trace a benchmark run without reading user telemetry, and a developer's cost view
	// silently includes calls that were never theirs.
	//
	// EMPTY MEANS UNKNOWN, NOT EXTERNAL. Spans written before this field existed carry ''
	// and form their own bucket, exactly as meter_source's rows did in ClickHouse migration
	// 006. Treating unknown as developer traffic would be the one reading that quietly puts
	// harness calls back into the user's numbers.
	AgentKind string `json:"agent_kind,omitempty"`

	ErrorType    string         `json:"error_type,omitempty"`
	ErrorMessage string         `json:"error_message,omitempty"`
	PayloadJSON  map[string]any `json:"payload_json,omitempty"`

	// Priority is a sampling hint, not sent on the wire. PriorityAuto (default)
	// lets the client decide by type/status; PriorityHigh forces keep.
	Priority Priority `json:"-"`
}

// Config configures the emitter. Enabled gates everything.
type Config struct {
	Enabled      bool
	Endpoint     string // Pyyol Lens ingest base URL, e.g. http://localhost:8081
	APIKey       string // X-Pyyol-Key
	Project      string // maps to project_id (default "pyyol-arena")
	Organization string // maps to organization_id (Lens scoping); empty = unset
	Environment  string // dev/staging/prod
	ServiceName  string // source_service (default "arena")

	// Tuning (zero => sane defaults).
	FlushInterval time.Duration
	MaxBatch      int
	BufferSize    int
	MaxRetries    int
	RetryBase     time.Duration
	HTTPTimeout   time.Duration

	// TraceSampleRate keeps a deterministic fraction (0..1] of NORMAL-priority
	// traces by hashing trace_id — whole traces are kept or dropped together so
	// waterfalls stay intact. High-priority events (failures, trace lifecycle,
	// benchmark facts) are always kept. Default 1.0 (keep everything).
	TraceSampleRate float64
}

func (c Config) withDefaults() Config {
	if c.Project == "" {
		c.Project = "pyyol-arena"
	}
	if c.ServiceName == "" {
		c.ServiceName = "arena"
	}
	if c.Environment == "" {
		c.Environment = "development"
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = time.Second
	}
	if c.MaxBatch <= 0 {
		c.MaxBatch = 200
	}
	if c.BufferSize <= 0 {
		c.BufferSize = 4096
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = 3
	}
	if c.RetryBase <= 0 {
		c.RetryBase = 250 * time.Millisecond
	}
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = 5 * time.Second
	}
	if c.TraceSampleRate <= 0 || c.TraceSampleRate > 1 {
		c.TraceSampleRate = 1.0
	}
	return c
}

// Client is the batching emitter. Safe for concurrent use.
type Client struct {
	cfg     Config
	log     *slog.Logger
	http    *http.Client
	enabled bool

	ch     chan Event
	done   chan struct{}
	wg     sync.WaitGroup
	closed atomic.Bool

	dropped atomic.Int64 // events dropped on a full buffer
	sampled atomic.Int64 // events dropped by trace sampling (not a failure)
	sent    atomic.Int64
}

// New builds a Client and, when enabled, starts its background flush loop.
// A disabled client is a valid no-op (no goroutine, all methods cheap).
func New(cfg Config, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	cfg = cfg.withDefaults()
	c := &Client{
		cfg:     cfg,
		log:     log,
		enabled: cfg.Enabled && cfg.Endpoint != "" && cfg.APIKey != "",
		http:    &http.Client{Timeout: cfg.HTTPTimeout},
	}
	if !c.enabled {
		if cfg.Enabled {
			log.Warn("telemetry: PYYOL_LENS enabled but endpoint/key missing — running as no-op")
		}
		return c
	}
	c.ch = make(chan Event, cfg.BufferSize)
	c.done = make(chan struct{})
	c.wg.Add(1)
	go c.loop()
	log.Info("telemetry: Pyyol Lens emitter enabled", "endpoint", cfg.Endpoint, "project", cfg.Project)
	return c
}

// Enabled reports whether events are actually shipped.
func (c *Client) Enabled() bool { return c != nil && c.enabled }

// EmitEvent normalizes and enqueues one event. Non-blocking: on a full buffer the
// event is dropped and counted. Safe to call after Shutdown (drops silently).
func (c *Client) EmitEvent(e Event) {
	if c == nil || !c.enabled || c.closed.Load() {
		return
	}
	c.normalize(&e)
	if !c.keep(&e) {
		c.sampled.Add(1)
		return
	}
	select {
	case c.ch <- e:
	default:
		c.dropped.Add(1)
	}
}

// keep decides whether an event survives trace sampling. High-priority events
// (explicit or auto-classified: failures, trace lifecycle, benchmark) always
// survive; everything else is kept iff its trace hashes into the sample.
func (c *Client) keep(e *Event) bool {
	if e.Priority == PriorityHigh || autoHigh(e) {
		return true
	}
	if c.cfg.TraceSampleRate >= 1.0 {
		return true
	}
	if e.TraceID == "" {
		return true // never sample away uncorrelated events on a trace hash
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(e.TraceID))
	return float64(h.Sum32()%10000) < c.cfg.TraceSampleRate*10000
}

// autoHigh classifies must-keep events by type/status so call sites need not set
// Priority: any failure, any trace-lifecycle event, and benchmark facts.
func autoHigh(e *Event) bool {
	if e.Status == "error" || strings.HasSuffix(e.EventType, "_failed") {
		return true
	}
	switch e.EventType {
	case "trace_started", "trace_completed", "trace_failed", "benchmark_recorded":
		return true
	}
	return false
}

func (c *Client) normalize(e *Event) {
	if e.EventID == "" {
		e.EventID = newID()
	}
	// A LEAF EVENT IS ITS OWN SPAN.
	//
	// span_id was set by nothing on this side: 0 of 129,760 events carried one, so the Lens
	// `spans` projection — which filters on `span_id != ''` — produced zero rows forever and any
	// UI reading it showed an empty trace. The events were all there; they simply had no
	// identity to be grouped under.
	//
	// The events this service emits are leaves rather than containers. A gateway model call, a
	// resolved decision, a benchmark record: each one IS the operation, so the operation's span
	// is the event itself and the event's own id is the correct span id. It is already unique
	// and already the key the pipeline dedupes on, which makes the span idempotent under NATS
	// at-least-once redelivery for free.
	//
	// Only for events that name a span_type. An event with no span type is not an operation and
	// should not appear as one; internal/platform/telemetry/span.go sets its own SpanID for the
	// nested case and is left alone here.
	if e.SpanID == "" && e.SpanType != "" {
		e.SpanID = e.EventID
	}
	if e.EventTime.IsZero() {
		e.EventTime = time.Now().UTC()
	}
	if e.SchemaVersion == "" {
		e.SchemaVersion = SchemaVersion
	}
	if e.SourceService == "" {
		e.SourceService = c.cfg.ServiceName
	}
	if e.Component == "" {
		e.Component = c.cfg.ServiceName
	}
	if e.ProjectID == "" {
		e.ProjectID = c.cfg.Project
	}
	if e.OrganizationID == "" {
		e.OrganizationID = c.cfg.Organization
	}
	if e.Environment == "" {
		e.Environment = c.cfg.Environment
	}
	if e.RequestID == "" {
		e.RequestID = e.TraceID
	}
	if e.TotalTokens == 0 && (e.PromptTokens > 0 || e.CompletionTokens > 0) {
		e.TotalTokens = e.PromptTokens + e.CompletionTokens
	}
	e.ErrorMessage = maskSecret(e.ErrorMessage)
	e.PayloadJSON = maskMap(e.PayloadJSON)
}

// Stats returns cumulative sent/dropped counters (for the platform's own metrics).
func (c *Client) Stats() (sent, dropped int64) {
	if c == nil {
		return 0, 0
	}
	return c.sent.Load(), c.dropped.Load()
}

// Sampled returns the number of events dropped by trace sampling (deliberate,
// not a loss — distinct from Stats' buffer-full drops).
func (c *Client) Sampled() int64 {
	if c == nil {
		return 0
	}
	return c.sampled.Load()
}

func (c *Client) loop() {
	defer c.wg.Done()
	tick := time.NewTicker(c.cfg.FlushInterval)
	defer tick.Stop()
	batch := make([]Event, 0, c.cfg.MaxBatch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		c.send(batch)
		batch = batch[:0]
	}
	for {
		select {
		case e := <-c.ch:
			batch = append(batch, e)
			if len(batch) >= c.cfg.MaxBatch {
				flush()
			}
		case <-tick.C:
			flush()
		case <-c.done:
			// Drain whatever is buffered, then flush a final batch.
			for {
				select {
				case e := <-c.ch:
					batch = append(batch, e)
					if len(batch) >= c.cfg.MaxBatch {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

func (c *Client) send(batch []Event) {
	body, err := json.Marshal(struct {
		Events []Event `json:"events"`
	}{Events: batch})
	if err != nil {
		c.log.Warn("telemetry: marshal failed", "err", err, "count", len(batch))
		return
	}
	delay := c.cfg.RetryBase
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if c.post(body) {
			c.sent.Add(int64(len(batch)))
			return
		}
		if attempt < c.cfg.MaxRetries {
			time.Sleep(delay)
			delay *= 2
		}
	}
	c.dropped.Add(int64(len(batch)))
	c.log.Warn("telemetry: batch dropped after retries", "count", len(batch))
}

func (c *Client) post(body []byte) bool {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.HTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.Endpoint, "/")+"/v1/events/batch", bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pyyol-Key", c.cfg.APIKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// Shutdown drains and flushes, then stops the loop. Safe to call once; further
// EmitEvent calls become no-ops.
func (c *Client) Shutdown(ctx context.Context) error {
	if c == nil || !c.enabled || !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(c.done)
	finished := make(chan struct{})
	go func() { c.wg.Wait(); close(finished) }()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// --- narrow secret masking (LLM-content-safe: no blanket "token"/"bearer") ---

func maskSecret(s string) string {
	if s == "" {
		return s
	}
	low := strings.ToLower(s)
	if strings.Contains(low, "authorization:") && strings.Contains(low, "bearer") {
		return "[redacted]"
	}
	if strings.Contains(low, "-----begin") && strings.Contains(low, "private key-----") {
		return "[redacted]"
	}
	return s
}

func maskMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "authorization") || lk == "token" || lk == "secret" ||
			strings.Contains(lk, "password") || strings.Contains(lk, "api_key") ||
			strings.Contains(lk, "private_key") {
			out[k] = "[redacted]"
			continue
		}
		if nested, ok := v.(map[string]any); ok {
			out[k] = maskMap(nested)
			continue
		}
		out[k] = v
	}
	return out
}
