package sandbox_test

import (
	"context"
	"testing"

	"github.com/agent-arena/arena/internal/bot"
	"github.com/agent-arena/arena/internal/sandbox"
)

type fakeStarter struct {
	calls          int
	lastHouseAgent string
	lastHouseOwner string
	lastPolicy     string
}

func (f *fakeStarter) CreateSandbox(_ context.Context, _, _, houseAgent, houseOwner, policy string) (string, error) {
	f.calls++
	f.lastHouseAgent, f.lastHouseOwner, f.lastPolicy = houseAgent, houseOwner, policy
	return "m_test", nil
}

func TestStartPicksOpponentByDifficulty(t *testing.T) {
	f := &fakeStarter{}
	svc := sandbox.New(f, true)

	res, err := svc.Start(context.Background(), "ag_dev", "usr_dev", bot.Hard)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if res.MatchID != "m_test" || res.Mode != "sandbox" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Opponent.ID != sandbox.HouseMaster || res.Opponent.Style != bot.Balanced {
		t.Fatalf("hard should map to the master/balanced opponent, got %+v", res.Opponent)
	}
	if f.lastHouseAgent != sandbox.HouseMaster || f.lastHouseOwner != sandbox.HouseOwner || f.lastPolicy != bot.Balanced {
		t.Fatalf("CreateSandbox got house=%s owner=%s policy=%s", f.lastHouseAgent, f.lastHouseOwner, f.lastPolicy)
	}
}

func TestStartDefaultsToStrongest(t *testing.T) {
	f := &fakeStarter{}
	res, err := sandbox.New(f, true).Start(context.Background(), "ag_dev", "usr_dev", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Empty difficulty defaults to the STRONGEST house opponent (hard/balanced) so a
	// sandbox match is a genuinely strong, pure-engine test by default.
	if res.Opponent.Difficulty != bot.Hard || res.Opponent.Style != bot.Balanced {
		t.Fatalf("empty difficulty should default to hard/balanced, got %+v", res.Opponent)
	}
}

func TestStartDisabledIsRejected(t *testing.T) {
	f := &fakeStarter{}
	_, err := sandbox.New(f, false).Start(context.Background(), "ag_dev", "usr_dev", bot.Easy)
	if err == nil {
		t.Fatal("expected an error when sandbox is disabled")
	}
	if f.calls != 0 {
		t.Fatalf("disabled sandbox must not create a match, but CreateSandbox was called %d times", f.calls)
	}
}

func TestOpponentsRoster(t *testing.T) {
	opps := sandbox.New(&fakeStarter{}, true).Opponents()
	if len(opps) != 3 {
		t.Fatalf("want 3 house opponents, got %d", len(opps))
	}
	wantDiff := map[string]bool{bot.Easy: true, bot.Medium: true, bot.Hard: true}
	for _, o := range opps {
		if !wantDiff[o.Difficulty] {
			t.Fatalf("unexpected difficulty %q", o.Difficulty)
		}
		if o.ID == "" || o.Style == "" {
			t.Fatalf("opponent missing id/style: %+v", o)
		}
	}
}
