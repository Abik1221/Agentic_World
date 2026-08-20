package store

import (
	"testing"

	"github.com/agent-arena/arena/internal/ladder"
)

// TestLadderRepoImplementsThePort. A repo that drifted from ladder.Repo would only fail at
// the wiring site, which is far from the code that broke.
func TestLadderRepoImplementsThePort(t *testing.T) {
	var _ ladder.Repo = (*LadderRepo)(nil)
}
