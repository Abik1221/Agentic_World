package bot

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The house must never stake.
//
// These agents are deterministic code. A deterministic agent taking coins off a developer is
// fraud, and the platform's verification gate exists to stop exactly that — it flagged the house
// bots 72,469 times, correctly, because they are a rules engine and not an LLM.
//
// The tempting fix was to exempt house bots from the gate. That trades the integrity of the whole
// arena for a fuller lobby. The house seeds PRACTICE tables instead, and every staked seat
// belongs to a verified LLM-backed agent whose key a developer holds.
func TestHouseStakeIsZero(t *testing.T) {
	if got := (&Runner{}).houseStake(); got != 0 {
		t.Fatalf("houseStake() = %d, want 0 — a deterministic agent winning real coins is fraud, "+
			"and no lobby-fullness argument outweighs that", got)
	}
}

// No path may hand a non-zero fee to a table the house creates or joins.
//
// Source-reading because the alternative is a full match stack; the registration line is also
// where someone would reintroduce a stake, so it is the honest place to check. The seeding fee
// went 100 -> 500 earlier in this session before the direction was reversed, so this is a real
// regression, not a hypothetical one.
func TestNoHardcodedStakeReachesAHouseTable(t *testing.T) {
	src, err := os.ReadFile("runner.go")
	if err != nil {
		t.Fatalf("reading runner.go: %v", err)
	}
	// A literal int64(N) or a bare number assigned to something fee-shaped.
	bad := regexp.MustCompile(`(?i)(bid|entry|entryFee|stake)\s*:?=\s*(int64\()?\s*[1-9][0-9]*`)
	for i, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue // prose about the old values is the point of the comments
		}
		if m := bad.FindString(line); m != "" {
			t.Errorf("runner.go:%d assigns a non-zero stake (%q). The house plays practice only; "+
				"staked seats belong to verified LLM agents.", i+1, m)
		}
	}
}
