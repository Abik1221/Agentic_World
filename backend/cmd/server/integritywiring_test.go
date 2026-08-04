package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// RANKED INTEGRITY MUST NOT BE WIRED BEHIND AN UNRELATED FEATURE FLAG.
//
// The zero-proof gate in match.rankedIntegrityFailed is well covered by
// internal/match/integrity_test.go — every branch of the rule is asserted there. None of
// that mattered, because the checker was never installed on a default deployment:
// SetTurnMinter and SetIntegrityCheck both sat inside `if cfg.RankedAutoDrive`, an
// unrelated flag (drive paired agents over their sockets) that is off by default. With no
// checker, rankedIntegrityFailed returns at its first line and every ranked match settles
// unexamined.
//
// So the gate was written, documented as "always active once a checker is installed",
// unit-tested in full — and switched off in main(). A logic test cannot see that; the
// defect lives in the wiring, one layer above everything the unit tests can reach.
//
// This is a source assertion, in the same spirit as internal/docs/claims_test.go: it reads
// main.go and checks a structural property that no runtime test can observe without
// standing up the whole server. It is deliberately narrow — it does not care where the
// calls are, only that they are not nested under the auto-drive branch.
func TestRankedIntegrityIsNotGatedOnAutoDrive(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(src)

	for _, call := range []string{"SetIntegrityCheck(", "SetTurnMinter("} {
		idx := strings.Index(body, call)
		if idx < 0 {
			t.Fatalf("main.go no longer calls %s — ranked integrity is not wired at all, "+
				"which is strictly worse than the bug this test guards", call)
		}
		if in, branch := insideConditional(body, idx); in && strings.Contains(branch, "RankedAutoDrive") {
			t.Errorf("%s is nested under `if %s`. Ranked integrity depends on TURN_PROOF_SECRET "+
				"(no secret ⇒ the signer is inert and the gate cannot fire), NOT on whether the "+
				"server drives paired agents over their sockets. Auto-drive is off by default, so "+
				"this leaves every ranked match on a default deployment settling with no integrity "+
				"check at all.", strings.TrimSuffix(call, "("), strings.TrimSpace(branch))
		}
	}
}

// insideConditional finds the innermost `if …{` block containing offset and returns its
// condition. Brace counting is enough here: main.go is ordinary formatted Go, and the
// assertion above only needs to know whether one specific identifier guards the call.
func insideConditional(body string, offset int) (bool, string) {
	depth := 0
	// Walk backwards from the call, tracking brace depth. The first `{` that closes
	// above our starting depth opens the block we are in.
	for i := offset; i > 0; i-- {
		switch body[i] {
		case '}':
			depth++
		case '{':
			if depth > 0 {
				depth--
				continue
			}
			// Found the opening brace of our enclosing block. Its condition is the line
			// it sits on.
			lineStart := strings.LastIndexByte(body[:i], '\n') + 1
			line := body[lineStart:i]
			if m := regexp.MustCompile(`^\s*if\s+(.*)$`).FindStringSubmatch(line); m != nil {
				return true, m[1]
			}
			return false, ""
		}
	}
	return false, ""
}

// The share rule stays off until someone has measured what honest agents score. This is a
// deliberate product decision, not an oversight, and it is easy to "fix" by guessing a
// number — which would void the matches of developers who ARE paying for inference, since
// a proof only exists when the agent routed through the gateway. The zero-proof gate needs
// no threshold, which is exactly why it can be on while this stays 0.
func TestShareRuleStaysOffByDefault(t *testing.T) {
	src, err := os.ReadFile("../../internal/config/config.go")
	if err != nil {
		t.Skipf("config.go not readable from here: %v", err)
	}
	m := regexp.MustCompile(`intVal\(\s*"RANKED_INTEGRITY_MIN_PCT"\s*,\s*(\d+)\s*\)`).
		FindStringSubmatch(string(src))
	if m == nil {
		t.Fatal("RANKED_INTEGRITY_MIN_PCT is no longer read from config with an int default")
	}
	if m[1] != "0" {
		t.Errorf("RANKED_INTEGRITY_MIN_PCT defaults to %s. Turning the SHARE rule on by default "+
			"voids ranked matches for every honest agent that does not route through the Pyyol "+
			"gateway — batching, caching and retries all legitimately produce fewer proofs than "+
			"decisions. Derive this from observed bound_decisions, then set it per-deployment.", m[1])
	}
}
