package devplatform

import (
	"fmt"
	"strings"
	"time"
)

// String renders a certification report as a readable validation report —
// exactly the artifact the spec asks for on both pass and fail.
func (r CertificationReport) String() string {
	var b strings.Builder
	verdict := "❌ NOT CERTIFIED"
	if r.Certified {
		verdict = "✅ CERTIFIED"
	}
	fmt.Fprintf(&b, "Certification Report — %s\n", r.GameName)
	fmt.Fprintf(&b, "  game:    %s (engine %s)\n", r.Game, r.EngineVersion)
	fmt.Fprintf(&b, "  agent:   %s\n", r.AgentID)
	fmt.Fprintf(&b, "  verdict: %s\n", verdict)
	fmt.Fprintf(&b, "  elapsed: %s\n\n", roundDur(r.Elapsed))

	for _, m := range r.Matches {
		status := "PASS"
		if !m.Passed {
			status = "FAIL"
		}
		fmt.Fprintf(&b, "  Match %d — %s  [%s]  (%s)\n", m.Index, m.Name, status, roundDur(m.Duration))
		fmt.Fprintf(&b, "    seed=%q  completed=%v  winner=%q  moves=%d  events=%d\n",
			m.Seed, m.Outcome.Completed, m.Outcome.Winner, m.Outcome.Moves, m.Outcome.Events)
		if m.Err != "" {
			fmt.Fprintf(&b, "    error: %s\n", m.Err)
		}
		for _, c := range m.Checks {
			mark := "✓"
			if !c.Passed {
				mark = "✗"
			}
			fmt.Fprintf(&b, "      %s %-22s %s\n", mark, c.Name, c.Detail)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Summary is a single-line verdict, handy for logs and CI output.
func (r CertificationReport) Summary() string {
	passed := 0
	for _, m := range r.Matches {
		if m.Passed {
			passed++
		}
	}
	v := "not-certified"
	if r.Certified {
		v = "certified"
	}
	return fmt.Sprintf("%s: %s (%d/%d matches, %s)", r.GameName, v, passed, len(r.Matches), roundDur(r.Elapsed))
}

func roundDur(d time.Duration) time.Duration {
	if d >= time.Millisecond {
		return d.Round(time.Millisecond)
	}
	return d.Round(time.Microsecond)
}
