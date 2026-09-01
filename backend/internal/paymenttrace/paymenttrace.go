package paymenttrace

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"time"
)

// Event is one recorded stage of one payment attempt.
type Event struct {
	Flow     string         `json:"flow"`
	Ref      string         `json:"ref"`
	Stage    string         `json:"stage"`
	Status   string         `json:"status"`
	Detail   string         `json:"detail,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	FirstAt  time.Time      `json:"first_at"`
	At       time.Time      `json:"at"`
	Attempts int            `json:"attempts"`
}

// Repo is the persistence port (pgx impl in internal/store).
type Repo interface {
	// Record upserts one stage. Repeating an identical (status, detail) is a no-op
	// so a 15-second poller does not churn the row or inflate `attempts`.
	Record(ctx context.Context, userPublicID string, e Event) error
	// ByUser returns every event for a user's most recent `limit` flows.
	ByUser(ctx context.Context, userPublicID string, limit int) ([]Event, error)
	// ByRef returns one flow's events, oldest first.
	ByRef(ctx context.Context, userPublicID, flow, ref string) ([]Event, error)
	// RecentFailures returns the newest failed stages platform-wide (operator view).
	RecentFailures(ctx context.Context, limit int) ([]FailureRow, error)
}

// FailureRow is one broken payment, for the operator's triage list.
type FailureRow struct {
	UserPublicID string    `json:"user"`
	Flow         string    `json:"flow"`
	Ref          string    `json:"ref"`
	Stage        string    `json:"stage"`
	Detail       string    `json:"detail,omitempty"`
	At           time.Time `json:"at"`
}

// Service records stages and assembles them into timelines.
type Service struct {
	repo Repo
	log  *slog.Logger
}

func New(repo Repo, log *slog.Logger) *Service { return &Service{repo: repo, log: log} }

// Record writes one stage. It NEVER returns an error to the caller.
//
// Every call site is inside a money path — crediting a deposit, holding coins for
// a payout. A diagnostic write must not be able to fail one of those, and a caller
// that has to decide what to do about a logging error will eventually decide
// wrongly. So the error is logged here and swallowed, and the caller's code stays
// a single unconditional line.
func (s *Service) Record(ctx context.Context, userPublicID, flow, ref, stage, status, detail string, meta map[string]any) {
	if s == nil || s.repo == nil || userPublicID == "" || ref == "" {
		return
	}
	// An independent context: the caller's may already be unwinding (a cancelled
	// request, a shutting-down worker), and the moment a payment breaks is exactly
	// when the record matters most.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	err := s.repo.Record(rctx, userPublicID, Event{
		Flow: flow, Ref: ref, Stage: stage, Status: status, Detail: detail, Metadata: meta,
	})
	if err != nil {
		s.log.Warn("paymenttrace: record failed", "flow", flow, "ref", ref, "stage", stage, "error", err)
	}
}

// OK / Pending / Failed are the three call shapes, named so a reader of a money
// path can see the intent without decoding a status string argument.
func (s *Service) OK(ctx context.Context, user, flow, ref, stage string, meta map[string]any) {
	s.Record(ctx, user, flow, ref, stage, StatusOK, "", meta)
}

func (s *Service) Pending(ctx context.Context, user, flow, ref, stage, detail string, meta map[string]any) {
	s.Record(ctx, user, flow, ref, stage, StatusPending, detail, meta)
}

func (s *Service) Failed(ctx context.Context, user, flow, ref, stage, detail string, meta map[string]any) {
	s.Record(ctx, user, flow, ref, stage, StatusFailed, detail, meta)
}

// ── timeline assembly ────────────────────────────────────────────────────────

// TimelineStage is one node of the rendered diagram: the expected step, plus what
// actually happened to it (if anything).
type TimelineStage struct {
	StageSpec
	// Status is ok | pending | failed | "" (never reached).
	Status   string         `json:"status"`
	Detail   string         `json:"detail,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	At       *time.Time     `json:"at,omitempty"`
	FirstAt  *time.Time     `json:"first_at,omitempty"`
	Attempts int            `json:"attempts,omitempty"`
	// ElapsedMs is how long this stage took: its own completion minus the previous
	// stage's. Absent for a stage that never completed.
	ElapsedMs *int64 `json:"elapsed_ms,omitempty"`
	// FailureStage names the terminal event that replaced this step, when one did
	// (e.g. "session_expired" standing in for "payment_detected").
	FailureStage string `json:"failure_stage,omitempty"`
}

// Timeline is one payment attempt, rendered against its expected path.
type Timeline struct {
	Flow      string `json:"flow"`
	FlowLabel string `json:"flow_label"`
	Ref       string `json:"ref"`
	// State is the flow's overall verdict:
	//   completed — every expected stage reached ok
	//   in_progress — progressing, nothing wrong
	//   stalled — the next stage has not happened and nothing says it failed
	//   failed — a stage reported a terminal failure
	State string `json:"state"`
	// BrokeAt is the stage key where a failed/stalled flow stopped. Empty when the
	// flow is healthy. This is the one field the UI needs to point at the diagram.
	BrokeAt string `json:"broke_at,omitempty"`
	// Blame is the actor of the broken stage ("you" | "your wallet" | "solana" |
	// "pyyol"). Stated explicitly because the most common support cost is a user
	// blaming us for their wallet, or us blaming the chain for our own bug.
	Blame string `json:"blame,omitempty"`
	// Summary is one sentence: what happened and what to do next.
	Summary   string          `json:"summary"`
	StartedAt time.Time       `json:"started_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Stages    []TimelineStage `json:"stages"`
}

// stalledAfter is how long a stage may sit without progress before the timeline
// calls the flow stalled rather than in-progress.
//
// Generous on purpose. Solana finality is seconds, but a withdrawal legitimately
// waits on a human reviewer, and telling someone their money is stuck when it is
// merely queued is worse than saying nothing — they escalate, and the next real
// alarm is discounted. Per-flow, because "slow" means an entirely different
// duration for a chain confirmation than for a manual approval.
var stalledAfter = map[string]time.Duration{
	FlowDeposit:    20 * time.Minute,
	FlowWithdrawal: 48 * time.Hour,
	FlowTopup:      30 * time.Minute,
}

// nowUTC is the single clock read for timeline assembly. Wall-clock is correct
// here: "stalled" is a statement about elapsed real time, not about a monotonic
// interval within one process.
func nowUTC() time.Time { return time.Now().UTC() }

// Timelines assembles a user's recent payment attempts, newest first.
func (s *Service) Timelines(ctx context.Context, userPublicID string, limit int) ([]Timeline, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	events, err := s.repo.ByUser(ctx, userPublicID, limit)
	if err != nil {
		return nil, err
	}
	return Assemble(events, nowUTC()), nil
}

// Assemble groups raw events by flow+ref and renders each against its spec. Pure,
// so the interesting logic (where did it break) is testable without a database.
func Assemble(events []Event, now time.Time) []Timeline {
	type key struct{ flow, ref string }
	grouped := map[key][]Event{}
	order := []key{}
	for _, e := range events {
		k := key{e.Flow, e.Ref}
		if _, seen := grouped[k]; !seen {
			order = append(order, k)
		}
		grouped[k] = append(grouped[k], e)
	}

	out := make([]Timeline, 0, len(order))
	for _, k := range order {
		out = append(out, buildTimeline(k.flow, k.ref, grouped[k], now))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func buildTimeline(flow, ref string, events []Event, now time.Time) Timeline {
	spec := Spec(flow)
	byStage := map[string]Event{}
	var startedAt, updatedAt time.Time
	for _, e := range events {
		byStage[e.Stage] = e
		if startedAt.IsZero() || e.FirstAt.Before(startedAt) {
			startedAt = e.FirstAt
		}
		if e.At.After(updatedAt) {
			updatedAt = e.At
		}
	}

	t := Timeline{
		Flow: flow, FlowLabel: spec.Label, Ref: ref,
		StartedAt: startedAt, UpdatedAt: updatedAt,
		Stages: make([]TimelineStage, 0, len(spec.Stages)),
	}

	// Fold terminal failures onto the expected stage they stand in for, so the
	// diagram has exactly the nodes the user was promised and the break is marked
	// on one of them.
	replacement := map[string]Event{}
	for stage, e := range byStage {
		if target, ok := replacedStage(stage); ok {
			replacement[target] = e
		}
	}

	var prevAt time.Time
	failed := false
	firstIncomplete := ""

	for _, ss := range spec.Stages {
		node := TimelineStage{StageSpec: ss}

		e, reached := byStage[ss.Key]
		if fail, replaced := replacement[ss.Key]; replaced {
			// A terminal failure outranks whatever the normal stage last said: a
			// session that expired is not "still pending detection".
			e, reached = fail, true
			node.FailureStage = fail.Stage
		}

		if reached {
			node.Status = e.Status
			node.Detail = e.Detail
			node.Metadata = e.Metadata
			node.Attempts = e.Attempts
			at, first := e.At, e.FirstAt
			node.At, node.FirstAt = &at, &first
			if e.Status == StatusOK && !prevAt.IsZero() {
				ms := at.Sub(prevAt).Milliseconds()
				if ms >= 0 {
					node.ElapsedMs = &ms
				}
			}
			if e.Status == StatusOK {
				prevAt = at
			}
			if e.Status == StatusFailed && !failed {
				failed = true
				t.BrokeAt, t.Blame = ss.Key, ss.Actor
				t.Summary = stuckSummary(ss, e.Detail)
			}
		}

		if node.Status != StatusOK && firstIncomplete == "" {
			firstIncomplete = ss.Key
		}
		t.Stages = append(t.Stages, node)
	}

	switch {
	case failed:
		t.State = "failed"
	case firstIncomplete == "":
		t.State = "completed"
		t.Summary = "Completed. Every step of this payment finished."
	default:
		// Nothing has failed, but the path is not finished. Whether that is normal
		// progress or a stall is a question of TIME, not of state — and the two
		// need different words, because one asks the user to wait and the other
		// asks them to act.
		t.BrokeAt = firstIncomplete
		for _, ss := range spec.Stages {
			if ss.Key == firstIncomplete {
				t.Blame = ss.Actor
				if now.Sub(updatedAt) > stalledFor(flow) {
					t.State = "stalled"
					t.Summary = stuckSummary(ss, "")
				} else {
					t.State = "in_progress"
					t.Summary = "In progress — waiting on: " + ss.Label + "."
				}
				break
			}
		}
	}
	return t
}

func stalledFor(flow string) time.Duration {
	if d, ok := stalledAfter[flow]; ok {
		return d
	}
	return time.Hour
}

func stuckSummary(ss StageSpec, detail string) string {
	if detail != "" {
		return ss.StuckHint + " (" + detail + ")"
	}
	return ss.StuckHint
}

// MarshalMetadata is a helper for repo implementations.
func MarshalMetadata(m map[string]any) []byte {
	if len(m) == 0 {
		return []byte("{}")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}
