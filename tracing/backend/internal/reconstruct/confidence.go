package reconstruct

import (
	"math"

	"github.com/agent-arena/pyyol-lens/backend/internal/schema"
)

type Breakdown struct {
	STree float64 `json:"S_tree"`
	SMile float64 `json:"S_mile"`
	STime float64 `json:"S_time"`
	STerm float64 `json:"S_term"`
	SIng  float64 `json:"S_ing"`
}

type Violations struct {
	MissingParents int  `json:"missing_parents"`
	ExtraRoots     int  `json:"extra_roots"`
	TimeViolations int  `json:"time_violations"`
	DuplicateTerms bool `json:"duplicate_terminals"`
}

type Result struct {
	Confidence float64    `json:"confidence"`
	Mode       string     `json:"mode"`
	Breakdown  Breakdown  `json:"breakdown"`
	Violations Violations `json:"violations"`
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return math.Round(v*1000) / 1000
}

func Compute(events []schema.TelemetryEvent) Result {
	n := float64(len(events))
	if n == 0 {
		return Result{Confidence: 0, Mode: "weighted"}
	}
	spans := map[string]bool{}
	roots, missingParent := 0, 0
	runStart, runEnd, runFail := 0, 0, 0
	for _, e := range events {
		if e.SpanID != "" {
			spans[e.SpanID] = true
		}
		if e.ParentSpanID == "" && e.SpanID != "" {
			roots++
		}
		if e.EventType == "run.start" {
			runStart++
		}
		if e.EventType == "run.complete" {
			runEnd++
		}
		if e.EventType == "run.fail" {
			runFail++
		}
	}
	for _, e := range events {
		if e.ParentSpanID != "" && !spans[e.ParentSpanID] {
			missingParent++
		}
	}
	extraRoots := 0
	if roots > 1 {
		extraRoots = roots - 1
	}
	sTree := clamp(1 - float64(missingParent+extraRoots)/math.Max(10, n))

	kHit := 0.0
	if runStart > 0 {
		kHit++
	}
	if runEnd+runFail > 0 {
		kHit++
	}
	sMile := clamp(kHit / 2.0)

	sTime := 1.0
	dupTerm := runEnd > 1 || runFail > 1
	sTerm := 0.0
	if runStart > 0 && (runEnd+runFail) == 1 {
		sTerm = 1
	}
	if dupTerm {
		sTerm *= 0.5
	}

	sIng := 1.0
	conf := clamp(0.25*sTree + 0.30*sMile + 0.15*sTime + 0.20*sTerm + 0.10*sIng)
	return Result{
		Confidence: conf,
		Mode:       "weighted",
		Breakdown:  Breakdown{STree: sTree, SMile: sMile, STime: sTime, STerm: sTerm, SIng: sIng},
		Violations: Violations{MissingParents: missingParent, ExtraRoots: extraRoots, TimeViolations: 0, DuplicateTerms: dupTerm},
	}
}
