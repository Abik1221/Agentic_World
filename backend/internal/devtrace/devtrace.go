package devtrace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/platform/telemetry"
)

// Package devtrace serves a developer the trace of their OWN agent.
//
// Every other read path into the Lens is operator-facing: org-scoped, behind a shared
// secret, and free to return anything. This one is tenant-facing, so it is built the
// other way round — it starts from "return nothing" and adds back only what the
// caller is entitled to.
//
// There are two independent gates, and either one alone would be sufficient:
//
//  1. OWNERSHIP. The agent must belong to the calling user, checked against Postgres
//     before any query is issued.
//  2. VISIBILITY. Only event types on telemetry's allowlist are requested, and the
//     rows that come back are filtered through the same allowlist again.
//
// The duplication is the point. A mistake in the query, a stale filter, or a
// compromised Lens is not on its own enough to leak — the second gate still holds.
// Both are cheap; a leak of another developer's agent reasoning is not.

// Repo is the ownership oracle. It is the only component that knows which user owns
// which agent, which is why the whole enforcement lives on this side of the wire.
type Repo interface {
	// OwnedAgentIDs returns the public ids of every agent owned by this user.
	OwnedAgentIDs(ctx context.Context, userPublicID string) ([]string, error)
}

// Service reads a developer's own agent activity out of the Lens.
type Service struct {
	repo     Repo
	endpoint string // Lens query-api base URL; empty disables the feature
	apiKey   string
	org      string
	client   *http.Client
	log      *slog.Logger
}

// New builds the reader.
//
// queryAPIKey is the Lens READ secret (its QUERY_API_KEY), which is NOT the ingest
// key the telemetry emitter uses — the Lens gates the two planes on separate
// secrets. Passing the ingest key here earns a 401 on every read, and the only thing
// a developer could see for it was "the trace store is unreachable".
//
// log may be nil (falls back to the default logger). It is not optional in spirit:
// every failure below is invisible to the developer by design — they are told the
// view is degraded, not why — so the operator's only account of what went wrong is
// what gets logged here.
func New(repo Repo, queryEndpoint, queryAPIKey, org string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		repo:     repo,
		endpoint: strings.TrimRight(queryEndpoint, "/"),
		apiKey:   queryAPIKey,
		org:      org,
		// A short timeout on purpose: this is a convenience view, and a slow
		// telemetry backend must not hold an arena request open.
		client: &http.Client{Timeout: 8 * time.Second},
		log:    log,
	}
}

// Enabled reports whether the Lens read path is configured.
func (s *Service) Enabled() bool { return s.endpoint != "" }

// Entry is one traced moment in an agent's life, shaped for a developer rather than
// for an operator: no ingestion ids, no org/project scoping, no cost columns.
type Entry struct {
	At        time.Time      `json:"at"`
	Type      string         `json:"type"`
	Status    string         `json:"status"`
	Game      string         `json:"game,omitempty"`
	MatchID   string         `json:"match_id,omitempty"`
	AgentID   string         `json:"agent_id"`
	Operation string         `json:"operation,omitempty"`
	LatencyMS int64          `json:"latency_ms,omitempty"`
	Error     string         `json:"error,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// Activity returns the caller's own agent activity.
//
// agentPublicID may be empty, meaning "all of my agents". That case still resolves to
// an explicit list of owned ids — it is never translated into an unfiltered query.
func (s *Service) Activity(ctx context.Context, userPublicID, agentPublicID string, since time.Time, limit int) ([]Entry, error) {
	if !s.Enabled() {
		// Not a fault: this deployment has no trace store wired. Distinct from both
		// "down" and "refusing us", because the honest answer to a developer is
		// "not here", and no amount of waiting changes it.
		return nil, httpx.NewError(http.StatusServiceUnavailable, "traces_unconfigured",
			"Agent traces are not enabled in this environment.")
	}

	owned, err := s.repo.OwnedAgentIDs(ctx, userPublicID)
	if err != nil {
		return nil, err
	}
	ownedSet := make(map[string]bool, len(owned))
	for _, id := range owned {
		ownedSet[id] = true
	}

	// GATE 1 — ownership.
	actors := owned
	if agentPublicID != "" {
		if !ownedSet[agentPublicID] {
			// Deliberately 404, not 403: confirming that an agent id exists but
			// belongs to someone else is itself a disclosure. A developer probing
			// ids learns nothing either way.
			return nil, httpx.NewError(http.StatusNotFound, "agent_not_found", "Agent not found.")
		}
		actors = []string{agentPublicID}
	}
	if len(actors) == 0 {
		// No agents, so no activity. Return empty rather than querying with an empty
		// filter, which is the shape most likely to be mishandled downstream.
		return []Entry{}, nil
	}

	// GATE 2 — visibility. The allowlist is the single source of truth; this package
	// never enumerates event types itself.
	visible := telemetry.DevVisibleEventTypes()

	q := url.Values{}
	q.Set("actor_ids", strings.Join(actors, ","))
	q.Set("event_types", strings.Join(visible, ","))
	q.Set("since", since.UTC().Format(time.RFC3339))
	q.Set("limit", fmt.Sprint(limit))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint+"/v1/agent-activity?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Pyyol-Key", s.apiKey)
	req.Header.Set("x-organization-id", s.org)

	resp, err := s.client.Do(req)
	if err != nil {
		// Genuinely could not talk to it: DNS, refused, timed out. Retrying is the
		// right advice, and this is the ONLY case where it is.
		s.log.Warn("devtrace: trace store unreachable", "endpoint", s.endpoint, "error", err)
		return nil, httpx.NewError(http.StatusBadGateway, "traces_unreachable",
			"Could not reach the trace store. Try again shortly.")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// A reachable store that refuses us is a DEPLOYMENT fault, not a blip, and
		// telling a developer to "try again shortly" wastes their afternoon on
		// something no amount of retrying will fix. 401/403 means this arena is
		// holding the wrong read key (classically: the ingest key, because the Lens
		// gates ingest and query on separate secrets) or the wrong organization.
		//
		// The message stays free of internals — endpoints and key names are the
		// operator's business, and they go to the log, not to the tenant.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		s.log.Error("devtrace: trace store refused the read",
			"status", resp.StatusCode, "endpoint", s.endpoint, "org", s.org,
			"body", strings.TrimSpace(string(body)),
			"hint", "PYYOL_LENS_QUERY_API_KEY must equal the Lens QUERY_API_KEY (not INGEST_API_KEY), and PYYOL_LENS_ORG its organization id")
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, httpx.NewError(http.StatusBadGateway, "traces_misconfigured",
				"Traces are not readable in this environment — the arena is not authorized against the trace store. This is ours to fix, not yours.")
		}
		return nil, httpx.NewError(http.StatusBadGateway, "traces_unreachable",
			"Could not read traces. Try again shortly.")
	}

	var body struct {
		Events []struct {
			EventTime time.Time      `json:"event_time"`
			EventType string         `json:"event_type"`
			Status    string         `json:"status"`
			Game      string         `json:"game"`
			MatchID   string         `json:"match_id"`
			AgentID   string         `json:"agent_id"`
			Operation string         `json:"operation"`
			LatencyMS int64          `json:"latency_ms"`
			Error     string         `json:"error"`
			Payload   map[string]any `json:"payload"`
		} `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		// A 200 whose body we cannot parse is a schema disagreement between two
		// services, which is an operator problem wearing a developer-problem costume.
		s.log.Error("devtrace: trace store returned an unreadable body", "endpoint", s.endpoint, "error", err)
		return nil, httpx.NewError(http.StatusBadGateway, "traces_unreachable",
			"Could not read traces. Try again shortly.")
	}

	out := make([]Entry, 0, len(body.Events))
	for _, e := range body.Events {
		// Re-apply BOTH gates to what actually came back. If the remote filter was
		// wrong, or the endpoint changes behaviour later, nothing the caller does not
		// own and nothing outside the allowlist gets past this loop.
		if !ownedSet[e.AgentID] || !telemetry.DevVisible(e.EventType) {
			continue
		}
		out = append(out, Entry{
			At:        e.EventTime,
			Type:      e.EventType,
			Status:    e.Status,
			Game:      e.Game,
			MatchID:   e.MatchID,
			AgentID:   e.AgentID,
			Operation: e.Operation,
			LatencyMS: e.LatencyMS,
			Error:     e.Error,
			Detail:    e.Payload,
		})
	}
	return out, nil
}
