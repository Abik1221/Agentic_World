package mafia

import (
	"context"
	"testing"
	"time"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/platform/telemetry"
)

type recordingTracer struct {
	said     []telemetry.ChatEvent
	rejected []telemetry.ChatEvent
}

func (r *recordingTracer) EmitAgentSaid(ev telemetry.ChatEvent) { r.said = append(r.said, ev) }
func (r *recordingTracer) EmitAgentSayRejected(ev telemetry.ChatEvent) {
	r.rejected = append(r.rejected, ev)
}

// A line SILENCED by the rules must still be traced.
//
// "Tried to speak at night and was refused" and "stayed quiet" are different facts,
// and only one of them means the agent is misbehaving. Tracing only the accepted
// lines would make a broken agent look like a silent one.
func TestRejectedChatIsTraced(t *testing.T) {
	seats := make([]int, mf.RosterSize)
	for i := range seats {
		seats[i] = i + 1
	}
	state, _ := mf.New().Init([]byte("chat-trace-seed"), seats) // opens at NIGHT: floor closed

	repo := &fakeRepo{getMatch: Match{
		PublicID: "mf_x", Status: StatusActive, State: state,
		Players: []Player{{AgentPublicID: "ag_1", Seat: 1}},
	}}
	tr := &recordingTracer{}
	svc := newTestSvc(repo, fakeLock{}, fakeClock{t: time.Unix(1_700_000_000, 0)}, Config{})
	svc.SetChatTracer(tr)

	if _, err := svc.Say(context.Background(), "ag_1", "mf_x", "who is with me?", "alliance", 0); err == nil {
		t.Fatal("speaking at night should be refused — the town is asleep")
	}
	if len(tr.rejected) != 1 {
		t.Fatalf("rejected lines traced = %d, want 1 — a silenced agent would be invisible", len(tr.rejected))
	}
	if got := tr.rejected[0].Reason; got != "closed_floor" {
		t.Fatalf("reason = %q, want closed_floor so an operator can tell WHY it was refused", got)
	}
	if tr.rejected[0].MatchID != "mf_x" || tr.rejected[0].AgentID != "ag_1" {
		t.Fatalf("rejection not attributable: %+v", tr.rejected[0])
	}
	if len(tr.said) != 0 {
		t.Fatalf("a refused line was recorded as spoken: %+v", tr.said)
	}
}

// A nil tracer (telemetry off) must not break the game path.
func TestChatWorksWithoutTracer(t *testing.T) {
	repo := &fakeRepo{getMatch: Match{Status: StatusWaiting}}
	svc := newTestSvc(repo, fakeLock{}, fakeClock{t: time.Unix(0, 0)}, Config{})
	if _, err := svc.Say(context.Background(), "ag", "m1", "hi", "info", 0); err == nil {
		t.Fatal("expected ErrNotActive on a waiting table")
	}
}
