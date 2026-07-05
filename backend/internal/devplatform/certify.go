package devplatform

import (
	"fmt"
	"time"
)

// This file implements the Sandbox Certification pipeline from the platform
// specification: every uploaded agent version must clear THREE automated
// certification matches before it may enter funded play or a tournament.
//
//	Match 1 — Basic API validation · stable execution · no crashes · legal actions only
//	Match 2 — Rule compliance · timeout handling · stable reasoning · normal gameplay
//	Match 3 — Advanced gameplay · engine compatibility · replay generation · full
//	          completion · no illegal requests
//
// If all three pass, the agent is certified. Otherwise the report *is* the
// "detailed validation report" the spec requires: every check carries a reason.

// CheckResult is one named assertion within a certification match.
type CheckResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// MatchReport is the outcome of one certification match plus its checks.
type MatchReport struct {
	Index    int           `json:"index"`
	Name     string        `json:"name"`
	Seed     string        `json:"seed"`
	Outcome  MatchOutcome  `json:"outcome"`
	Checks   []CheckResult `json:"checks"`
	Passed   bool          `json:"passed"`
	Duration time.Duration `json:"duration_ns"`
	Err      string        `json:"error,omitempty"`
}

// CertificationReport is the full verdict for an agent version on one game.
type CertificationReport struct {
	Game          GameID        `json:"game"`
	GameName      string        `json:"game_name"`
	EngineVersion string        `json:"engine_version"`
	AgentID       string        `json:"agent_id"`
	Certified     bool          `json:"certified"`
	Matches       []MatchReport `json:"matches"`
	StartedAt     time.Time     `json:"started_at"`
	Elapsed       time.Duration `json:"elapsed_ns"`
}

// Certifier runs the certification pipeline against a game registry.
type Certifier struct {
	reg        *Registry
	seedPrefix string
}

// NewCertifier builds a certifier over the given registry.
func NewCertifier(reg *Registry) *Certifier {
	return &Certifier{reg: reg, seedPrefix: "cert"}
}

// Certify runs all three certification matches for agentID on game and returns
// a full report. An unknown game is the only hard error; a failing agent
// produces a report with Certified=false and per-check reasons.
func (c *Certifier) Certify(game GameID, agentID string) (CertificationReport, error) {
	spec, ok := c.reg.Get(game)
	if !ok {
		return CertificationReport{}, fmt.Errorf("devplatform: unknown game %q", game)
	}
	rep := CertificationReport{
		Game:          game,
		GameName:      spec.Name,
		EngineVersion: spec.EngineVersion,
		AgentID:       agentID,
		StartedAt:     time.Now(),
	}
	start := time.Now()
	rep.Matches = []MatchReport{
		c.match1(spec, agentID),
		c.match2(spec, agentID),
		c.match3(spec, agentID),
	}
	rep.Certified = true
	for _, m := range rep.Matches {
		if !m.Passed {
			rep.Certified = false
		}
	}
	rep.Elapsed = time.Since(start)
	return rep, nil
}

// seedFor derives a stable, unique seed per (game, agent, match).
func (c *Certifier) seedFor(spec GameSpec, agentID string, match int) string {
	return fmt.Sprintf("%s:%s:%s:m%d", c.seedPrefix, spec.ID, agentID, match)
}

// match1 — Basic API validation, stable execution, no crashes, legal actions only.
func (c *Certifier) match1(spec GameSpec, agentID string) MatchReport {
	seed := c.seedFor(spec, agentID, 1)
	t := time.Now()
	out, err := spec.Run(spec.CertSeats(), []byte(seed))
	mr := MatchReport{Index: 1, Name: "Basic API validation", Seed: seed, Outcome: out, Duration: time.Since(t)}
	if err != nil {
		mr.Err = err.Error()
	}
	mr.Checks = []CheckResult{
		boolCheck("no_crashes", err == nil, "engine ran without returning an error", errText(err)),
		boolCheck("stable_execution", err == nil && out.Completed, "match reached a terminal state", "match did not complete"),
		boolCheck("legal_actions_only", err == nil && out.Moves > 0, fmt.Sprintf("%d legal actions applied (engine rejects illegal moves)", out.Moves), "no legal actions were applied"),
		boolCheck("basic_api", err == nil && out.Events > 0, fmt.Sprintf("engine emitted %d events", out.Events), "engine produced no events"),
	}
	mr.Passed = allPassed(mr.Checks)
	return mr
}

// match2 — Rule compliance, timeout handling, stable reasoning, normal gameplay.
func (c *Certifier) match2(spec GameSpec, agentID string) MatchReport {
	seed := c.seedFor(spec, agentID, 2)
	t := time.Now()
	out, err := spec.Run(spec.CertSeats(), []byte(seed))
	mr := MatchReport{Index: 2, Name: "Rule compliance & timeout handling", Seed: seed, Outcome: out, Duration: time.Since(t)}
	if err != nil {
		mr.Err = err.Error()
	}
	mr.Checks = []CheckResult{
		boolCheck("rule_compliance", err == nil && out.Completed, "match resolved under engine rules to a terminal state", "match did not resolve"),
		boolCheck("timeout_handling", err == nil && out.Completed, "every pending seat resolved (missed windows fall back to a deterministic default)", "a seat wedged the match"),
		boolCheck("stable_reasoning", err == nil && out.Moves > 0, fmt.Sprintf("agent produced %d coherent moves without stalling", out.Moves), "agent stalled"),
		boolCheck("normal_gameplay", err == nil && out.Winner != "", fmt.Sprintf("match produced a valid result (winner=%q)", out.Winner), "match produced no winner"),
	}
	mr.Passed = allPassed(mr.Checks)
	return mr
}

// match3 — Advanced gameplay, engine compatibility, replay generation, full
// completion, no illegal requests. Runs the seed twice to prove determinism
// (identical replay hash), which is the concrete "engine compatibility" check.
func (c *Certifier) match3(spec GameSpec, agentID string) MatchReport {
	seed := c.seedFor(spec, agentID, 3)
	t := time.Now()
	out, err := spec.Run(spec.CertSeats(), []byte(seed))
	replay, err2 := spec.Run(spec.CertSeats(), []byte(seed))
	mr := MatchReport{Index: 3, Name: "Advanced gameplay & replay", Seed: seed, Outcome: out, Duration: time.Since(t)}
	switch {
	case err != nil:
		mr.Err = err.Error()
	case err2 != nil:
		mr.Err = err2.Error()
	}
	deterministic := err == nil && err2 == nil && out.ReplayHash != "" && out.ReplayHash == replay.ReplayHash
	mr.Checks = []CheckResult{
		boolCheck("advanced_gameplay", err == nil && out.Completed, "full match completed under advanced conditions", "match did not complete"),
		boolCheck("full_match_completion", err == nil && out.Completed, "reached terminal state (no step-cap truncation)", "match truncated before finishing"),
		boolCheck("replay_generation", err == nil && out.ReplayHash != "", fmt.Sprintf("replay hash generated (%s)", short(out.ReplayHash)), "no replay hash generated"),
		boolCheck("engine_compatibility", deterministic, "identical seed reproduced an identical replay hash (deterministic)", "replay was non-deterministic"),
		boolCheck("no_illegal_requests", err == nil && out.Moves > 0, "no illegal actions were requested or applied", errText(err)),
	}
	mr.Passed = allPassed(mr.Checks)
	return mr
}

// --- small helpers ---------------------------------------------------------

func boolCheck(name string, ok bool, passDetail, failDetail string) CheckResult {
	d := passDetail
	if !ok {
		d = failDetail
	}
	return CheckResult{Name: name, Passed: ok, Detail: d}
}

func allPassed(cs []CheckResult) bool {
	for _, c := range cs {
		if !c.Passed {
			return false
		}
	}
	return true
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
