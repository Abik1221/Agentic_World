package monopoly

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
	"github.com/agent-arena/arena/internal/skill"
)

// A decision must be logged against the state it was made FROM.
//
// # The defect
//
// Act recorded the view it had just built for the RESPONSE, which is the post-move state —
// what the agent sees next. Paired with the action that produced it, every row in
// agent_match_decisions described a move against the board that RESULTED from it.
//
// Nothing failed loudly. internal/skill declines any action it cannot find among the options
// its model offers from the given state, and a shifted pair almost never matches — so the
// scorer refused nearly everything and looked selective rather than starved. Measured on the
// lab database: 1,207,717 Monopoly decisions had been through the scorer and 549 carried a
// score, with `buy` filed under the manage phase, `roll` under acquire and `skip_trade` under
// roll. The conclusion drawn from that shape was that Monopoly was unscorable. It is not; it
// was being asked the wrong question 1.2 million times.
//
// # Why the assertion is "the scorer accepts it" and not "the phase string matches"
//
// A phase comparison would pass for a pair that is aligned and still useless. The consumer
// that broke is the oracle here: the recorded (state, action) must be a pair the real scorer
// can score. That is the property the decision log exists to provide, so it is the one pinned.
//
// The negative half runs the SAME scorer against the post-move view and requires it to refuse.
// Without it, a test asserting only "the scorer accepts the recorded pair" would still pass if
// the two views happened to be interchangeable, and would prove nothing about the bug.
func TestActLogsTheDecisionAgainstThePreMoveState(t *testing.T) {
	seed := make([]byte, 32)
	state, _ := mono.New(matchConfig(2)).Init(seed)

	// Park the acting seat in the acquire phase, standing on a purchasable street with the
	// cash to take it. Buy is a decision the scorer models, so a correctly-logged row is
	// scorable and an incorrectly-logged one is not.
	seat := state.Current
	state.Phase = mono.PhaseAcquire
	state.Players[seat].Position = 1 // the first street on the board
	state.Players[seat].Cash = 1500

	deadline := time.Unix(9_000, 0)
	tmpl := &Match{
		Status: StatusActive, EntryFee: 0, Players: 2, Seed: seed, State: state,
		RoundDeadline: &deadline,
		Agents:        []Player{{Seat: seat, AgentPublicID: "agent-buyer"}},
	}

	rec := &captureDecisions{}
	repo := &advancingRepo{fakeRepo: &fakeRepo{}, tmpl: tmpl}
	svc := NewService(repo, fakeLock{}, nil, fakeBcast{}, nil,
		fakeClock{t: time.Unix(2_000, 0)}, Config{})
	svc.SetActDecisionRecorder(rec)

	if _, err := svc.Act(context.Background(), "agent-buyer", "match-1",
		mono.Action{Kind: mono.ActBuy}, "", false); err != nil {
		t.Fatalf("Act: %v", err)
	}
	if len(rec.got) != 1 {
		t.Fatalf("recorded %d decisions, want 1", len(rec.got))
	}
	d := rec.got[0]

	// --- The recorded pair is one the scorer can actually score --------------------------

	if d.Action != mono.ActBuy {
		t.Fatalf("recorded action = %q, want %q", d.Action, mono.ActBuy)
	}
	if _, ok := skill.ScoreMonopolyDecision(d.InputJSON, d.Action); !ok {
		var v struct {
			Phase string `json:"phase"`
		}
		_ = json.Unmarshal(d.InputJSON, &v)
		t.Fatalf("the scorer refused the logged decision (recorded phase %q with action %q) — "+
			"the row stores a state this action could not have been chosen from, which is how "+
			"1.2M scored Monopoly decisions yielded 549 scores", v.Phase, d.Action)
	}

	// --- The post-move view, which is what used to be logged, is NOT scorable -------------
	//
	// Proves the assertion above discriminates rather than passing on any input.

	post, err := repo.Get(context.Background(), "match-1")
	if err != nil {
		t.Fatalf("post-move Get: %v", err)
	}
	postView, err := json.Marshal(svc.view(post, "agent-buyer"))
	if err != nil {
		t.Fatalf("marshal post-move view: %v", err)
	}
	if _, ok := skill.ScoreMonopolyDecision(postView, mono.ActBuy); ok {
		t.Error("the POST-move view scored too, so this test cannot tell the two apart — " +
			"pick a decision whose phase actually advances")
	}

	// --- The round is the round the decision was made in ----------------------------------

	if d.Round != state.TurnCount {
		t.Errorf("recorded round = %d, want %d (the turn the decision was made in, not the "+
			"turn it produced)", d.Round, state.TurnCount)
	}
}

// captureDecisions records what Act logged.
type captureDecisions struct{ got []ActDecision }

func (c *captureDecisions) RecordActDecision(_ context.Context, d ActDecision) error {
	c.got = append(c.got, d)
	return nil
}

func (c *captureDecisions) AggregateSeatBenchmark(context.Context, string, string, map[string]string) error {
	return nil
}

// advancingRepo is fakeRepo's Get with the one property that matters here: after Advance, a
// Get returns the ADVANCED state.
//
// fakeRepo hands back the same template forever, so under it the post-move re-read yields the
// pre-move board and the two views are identical — a test built on it would pass with the bug
// fully present. The whole defect lives in the difference between those two reads, so the fake
// has to reproduce it.
type advancingRepo struct {
	*fakeRepo
	tmpl *Match
	cur  *mono.State
}

func (r *advancingRepo) Get(_ context.Context, id string) (Match, error) {
	m := *r.tmpl
	m.PublicID = id
	if r.cur != nil {
		m.State = *r.cur
	}
	return m, nil
}

func (r *advancingRepo) Advance(_ context.Context, _ string, st mono.State, _ *time.Time, _ []mono.Event) error {
	r.cur = &st
	return nil
}

func (r *advancingRepo) Finish(_ context.Context, _ string, st mono.State, _ int, _ string, _ []Player, _ []mono.Event) error {
	r.cur = &st
	return nil
}
