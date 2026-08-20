// Reconciliation for the embedded harness export.
//
// # Why this exists
//
// This package writes PROOF-SHAPED rows — `bound`, `bind_receipt`, `completion_hash`,
// `verified_cost` — directly into the tables the model board's verification gate reads. Those
// tables are the platform's evidence of record: migration 0079's header calls
// `agent_model_calls` "evidence rather than testimony". Seeding them from a file means a file
// can assert evidence.
//
// The embedded export as shipped does not survive its own claims:
//
//   - 642 decisions attributed to Anthropic/claude-opus-4 (390), OpenAI/gpt-5.2 (239) and
//     Google/gemini-3-flash (7), while all 574 model_calls were served by api.groq.com
//     (llama-3.1-8b-instant, llama-3.3-70b-versatile, gpt-oss-120b) and openrouter.ai
//     (gemma-4-26b, nemotron-3-nano, laguna-s-2.1). Zero frontier calls.
//   - all 574 calls marked bound = true, including 67 non-2xx (30x400, 25x429, 12x502) and
//     131 with zero tokens. Coverage is bound/decisions and is the platform's headline
//     verification metric, so this inflates it by construction.
//   - 262 of 642 decisions with an empty scaffold, which migration 0081:11-14 says cannot be
//     paired or compared.
//
// The seeder's own defences already stop this reaching a user-facing board — `kind` is the
// literal 'harness' and `rated` the literal false in the INSERTs, overriding what the file
// says, and verified_cost covers every benchmark row so the gateway-verified name wins the
// attribution COALESCE. Those defences are good and are why this is a latent hazard rather
// than a live leak. But "contained" is not "correct", and the containment is one missing
// verified_cost row away from failing.
//
// # The rule
//
// A single source of truth cannot be checked against itself. `pyyolbench/ledger.py:1-32`
// reaches the same conclusion from the other direction and is where the three-way check was
// first written; this is that check moved to the place that does the writing, because
// "controls live on the service that ESCROWS, not the handler" is the same lesson.
//
// On a fatal finding the seeder imports NOTHING and logs the census. It does not stop the
// server: Seed is best-effort by contract, and a benchmark that failed to import is a page
// with less on it while a server that will not start is an outage.
package harnessseed

import (
	"fmt"
	"sort"
	"strings"
)

// maxExamples bounds how many offending ids a finding carries. Enough to start debugging,
// few enough that a log line stays readable.
const maxExamples = 5

// Severity separates "this data is lying" from "this data is thin".
type Severity int

const (
	// Warn: the export is usable but something downstream must exclude these rows.
	Warn Severity = iota
	// Fatal: the export asserts something its own evidence contradicts. Do not import.
	Fatal
)

func (s Severity) String() string {
	if s == Fatal {
		return "FATAL"
	}
	return "WARN"
}

// Finding is one violated rule.
type Finding struct {
	Rule     string
	Severity Severity
	Detail   string
	Count    int
	Examples []string
}

func (f Finding) String() string {
	s := fmt.Sprintf("[%s] %s: %s (%d)", f.Severity, f.Rule, f.Detail, f.Count)
	if len(f.Examples) > 0 {
		s += " e.g. " + strings.Join(f.Examples, ", ")
	}
	return s
}

// Report is the outcome of reconciling one export.
type Report struct {
	Findings []Finding
	// Fatal is true when at least one Fatal finding fired. The seeder imports nothing.
	Fatal bool
}

// Summary renders the report for a log line, most severe first.
func (r Report) Summary() string {
	if len(r.Findings) == 0 {
		return "reconciliation clean"
	}
	out := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		out = append(out, f.String())
	}
	return strings.Join(out, "; ")
}

// normModel reduces a provider/model pair to a comparable key.
//
// Vendor prefixes are stripped because the same weights are routed under different names —
// groq serves "openai/gpt-oss-120b" and openrouter serves "google/gemma-4-26b-a4b-it:free".
// Comparing the raw strings would flag honest routing as fraud. Comparing only the tail
// catches the case that matters: a claim of one MODEL answered by a different MODEL.
func normModel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	return s
}

func addExample(ex []string, s string) []string {
	if len(ex) >= maxExamples {
		return ex
	}
	return append(ex, s)
}

// reconcile cross-checks the three tables that must agree before any of them is imported.
//
// The checks are deliberately about AGREEMENT rather than plausibility. We are not trying to
// judge whether a model is good, fast or real — only whether the export's own three accounts
// of the same match tell the same story.
func reconcile(r *results) Report {
	var rep Report

	// ── Rule 1: a bound call must be a call that succeeded and produced tokens ──────────
	//
	// `bound` means the gateway extracted a move from the model's own structured output.
	// A 4xx/5xx returned no completion to extract from, and a zero-token response returned
	// no content. Either marked bound is a claim the transport contradicts. This is the
	// exact failure ledger.py was written to catch.
	badStatus, zeroTok := 0, 0
	var exStatus, exTok []string
	for _, c := range r.ModelCalls {
		if !c.Bound {
			continue
		}
		if c.Status < 200 || c.Status > 299 {
			badStatus++
			exStatus = addExample(exStatus, fmt.Sprintf("%s/%s:%d", c.MatchID, c.Agent, c.Status))
		}
		if c.PromptTokens+c.CompletionTokens+c.ReasoningTokens == 0 {
			zeroTok++
			exTok = addExample(exTok, fmt.Sprintf("%s/%s", c.MatchID, c.Agent))
		}
	}
	if badStatus > 0 {
		rep.Findings = append(rep.Findings, Finding{
			Rule: "bound_call_must_be_2xx", Severity: Fatal, Count: badStatus,
			Detail:   "calls marked bound whose HTTP status returned no completion to bind",
			Examples: exStatus,
		})
	}
	if zeroTok > 0 {
		rep.Findings = append(rep.Findings, Finding{
			Rule: "bound_call_must_have_tokens", Severity: Fatal, Count: zeroTok,
			Detail:   "calls marked bound that emitted zero tokens",
			Examples: exTok,
		})
	}

	// ── Rule 2: a decision's declared model must have actually been called ──────────────
	//
	// agent_match_decisions.provider/model is SELF-REPORTED by the SDK; agent_model_calls is
	// what the gateway observed. When they disagree, the self-report is the one that must
	// give way — and an export where they disagree wholesale is describing a different run
	// than the one it carries evidence for.
	calledBy := map[string]map[string]bool{} // (match|agent) -> set of observed models
	for _, c := range r.ModelCalls {
		k := c.MatchID + "|" + c.Agent
		if calledBy[k] == nil {
			calledBy[k] = map[string]bool{}
		}
		calledBy[k][normModel(c.Model)] = true
	}
	mismatch := 0
	var exMismatch []string
	for _, d := range r.Decisions {
		if d.Model == nil || *d.Model == "" {
			continue
		}
		k := d.MatchID + "|" + d.Agent
		seen, ok := calledBy[k]
		if !ok {
			continue // no gateway record at all for this seat: covered by rule 4
		}
		if !seen[normModel(*d.Model)] {
			mismatch++
			observed := make([]string, 0, len(seen))
			for m := range seen {
				observed = append(observed, m)
			}
			sort.Strings(observed)
			exMismatch = addExample(exMismatch, fmt.Sprintf("%s claims %q, gateway saw %v",
				k, normModel(*d.Model), observed))
		}
	}
	if mismatch > 0 {
		rep.Findings = append(rep.Findings, Finding{
			Rule: "decision_model_must_match_gateway", Severity: Fatal, Count: mismatch,
			Detail:   "decisions whose self-reported model was never the model the gateway called",
			Examples: exMismatch,
		})
	}

	// ── Rule 3: benchmark attribution must match the verified cost row ─────────────────
	//
	// benchmark.observed_model is tier 2 of the attribution ladder and surfaces whenever a
	// verified_cost row is missing. If the two disagree, then the day coverage slips the
	// board silently starts publishing the wrong name.
	verified := map[string]string{}
	for _, v := range r.VerifiedCost {
		verified[v.MatchID+"|"+v.Agent] = normModel(v.Model)
	}
	attrConflict := 0
	var exAttr []string
	for _, b := range r.Benchmark {
		if b.ObservedModel == nil || *b.ObservedModel == "" {
			continue
		}
		k := b.MatchID + "|" + b.Agent
		v, ok := verified[k]
		if !ok || v == "" {
			continue
		}
		if normModel(*b.ObservedModel) != v {
			attrConflict++
			exAttr = addExample(exAttr, fmt.Sprintf("%s observed %q vs verified %q",
				k, normModel(*b.ObservedModel), v))
		}
	}
	if attrConflict > 0 {
		rep.Findings = append(rep.Findings, Finding{
			Rule: "observed_model_must_match_verified", Severity: Fatal, Count: attrConflict,
			Detail:   "benchmark rows whose observed model contradicts the gateway-verified model",
			Examples: exAttr,
		})
	}

	// ── Rule 4: a seat with decisions but no gateway evidence ──────────────────────────
	//
	// Warn, not fatal: absence is not a contradiction, and "absence never rejects" is the
	// rule that governs binding everywhere else in this codebase. But such a seat cannot
	// support a verified attribution and must not be counted in coverage.
	seatsWithDecisions := map[string]bool{}
	for _, d := range r.Decisions {
		seatsWithDecisions[d.MatchID+"|"+d.Agent] = true
	}
	noEvidence := 0
	var exNoEv []string
	for k := range seatsWithDecisions {
		if len(calledBy[k]) == 0 {
			noEvidence++
			exNoEv = addExample(exNoEv, k)
		}
	}
	if noEvidence > 0 {
		sort.Strings(exNoEv)
		rep.Findings = append(rep.Findings, Finding{
			Rule: "seat_without_gateway_evidence", Severity: Warn, Count: noEvidence,
			Detail:   "seats with logged decisions but no gateway call record",
			Examples: exNoEv,
		})
	}

	// ── Rule 5: scaffold fingerprint present ───────────────────────────────────────────
	//
	// Warn. Migration 0081:11-14 says a decision with no scaffold cannot be paired, and the
	// model board already excludes it by reason `no_scaffold`. Reported here so the census
	// is visible at import rather than only after a board build.
	noScaffold := 0
	for _, d := range r.Decisions {
		if d.Scaffold == nil || strings.TrimSpace(*d.Scaffold) == "" {
			noScaffold++
		}
	}
	if noScaffold > 0 {
		rep.Findings = append(rep.Findings, Finding{
			Rule: "decision_scaffold_present", Severity: Warn, Count: noScaffold,
			Detail: fmt.Sprintf("decisions with no scaffold fingerprint, of %d total; "+
				"these cannot be paired and will be excluded from the board", len(r.Decisions)),
		})
	}

	// ── Rule 6: a bound decision must have a matching model call ───────────────────────
	//
	// agent_match_bound_decisions is what coverage counts. A bound-decision row with no
	// corresponding gateway call is coverage asserted by the file rather than earned.
	orphanBound := 0
	var exOrphan []string
	for _, b := range r.BoundDecisions {
		if len(calledBy[b.MatchID+"|"+b.Agent]) == 0 {
			orphanBound++
			exOrphan = addExample(exOrphan, fmt.Sprintf("%s/%s r%d", b.MatchID, b.Agent, b.Round))
		}
	}
	if orphanBound > 0 {
		rep.Findings = append(rep.Findings, Finding{
			Rule: "bound_decision_needs_a_call", Severity: Fatal, Count: orphanBound,
			Detail:   "bound-decision rows with no gateway call behind them",
			Examples: exOrphan,
		})
	}

	sort.SliceStable(rep.Findings, func(i, j int) bool {
		return rep.Findings[i].Severity > rep.Findings[j].Severity
	})
	for _, f := range rep.Findings {
		if f.Severity == Fatal {
			rep.Fatal = true
			break
		}
	}
	return rep
}
