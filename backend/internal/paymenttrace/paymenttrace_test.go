package paymenttrace

import (
	"testing"
	"time"
)

var base = time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

func ev(flow, ref, stage, status, detail string, offset time.Duration) Event {
	at := base.Add(offset)
	return Event{Flow: flow, Ref: ref, Stage: stage, Status: status, Detail: detail,
		FirstAt: at, At: at, Attempts: 1}
}

func stageByKey(t *testing.T, tl Timeline, key string) TimelineStage {
	t.Helper()
	for _, s := range tl.Stages {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("stage %q missing from the rendered timeline", key)
	return TimelineStage{}
}

// The diagram must always show the FULL expected path, including steps that never
// happened. A timeline that only lists what occurred cannot show where it broke —
// the break is precisely the step that is absent.
func TestEveryExpectedStageIsRenderedEvenWhenNeverReached(t *testing.T) {
	events := []Event{ev(FlowDeposit, "dep_1", StageSessionCreated, StatusOK, "", 0)}

	tl := Assemble(events, base.Add(time.Minute))[0]

	if len(tl.Stages) != len(depositFlow.Stages) {
		t.Fatalf("rendered %d stages, the deposit flow has %d", len(tl.Stages), len(depositFlow.Stages))
	}
	if got := stageByKey(t, tl, StageCoinsCredited).Status; got != "" {
		t.Errorf("an unreached stage must have no status, got %q", got)
	}
}

func TestCompletedDepositReportsCompleted(t *testing.T) {
	events := []Event{
		ev(FlowDeposit, "dep_1", StageSessionCreated, StatusOK, "", 0),
		ev(FlowDeposit, "dep_1", StagePaymentDetected, StatusOK, "", 10*time.Second),
		ev(FlowDeposit, "dep_1", StageChainFinalized, StatusOK, "", 25*time.Second),
		ev(FlowDeposit, "dep_1", StageCoinsCredited, StatusOK, "", 26*time.Second),
		ev(FlowDeposit, "dep_1", StageUserNotified, StatusOK, "", 26*time.Second),
	}

	tl := Assemble(events, base.Add(time.Minute))[0]

	if tl.State != "completed" {
		t.Fatalf("state = %q, want completed (%s)", tl.State, tl.Summary)
	}
	if tl.BrokeAt != "" {
		t.Errorf("a completed flow must not name a break point, got %q", tl.BrokeAt)
	}
}

// Per-stage duration is the whole point of the timing: "the chain took 15s" and
// "our credit took 15s" are the same total and completely different problems.
func TestElapsedIsMeasuredFromThePreviousCompletedStage(t *testing.T) {
	events := []Event{
		ev(FlowDeposit, "dep_1", StageSessionCreated, StatusOK, "", 0),
		ev(FlowDeposit, "dep_1", StagePaymentDetected, StatusOK, "", 10*time.Second),
		ev(FlowDeposit, "dep_1", StageChainFinalized, StatusOK, "", 25*time.Second),
	}

	tl := Assemble(events, base.Add(time.Minute))[0]

	finalized := stageByKey(t, tl, StageChainFinalized)
	if finalized.ElapsedMs == nil || *finalized.ElapsedMs != 15_000 {
		t.Fatalf("finality elapsed = %v, want 15000ms measured from detection", finalized.ElapsedMs)
	}
	if created := stageByKey(t, tl, StageSessionCreated); created.ElapsedMs != nil {
		t.Error("the first stage has no predecessor and must report no elapsed time")
	}
}

// The core behaviour: a terminal failure is folded ONTO the expected step it
// stands in for, so the diagram marks the break in place instead of appending an
// orphan node the reader has to relate back to the path.
func TestTerminalFailureMarksTheStageItReplaces(t *testing.T) {
	events := []Event{
		ev(FlowDeposit, "dep_1", StageSessionCreated, StatusOK, "", 0),
		ev(FlowDeposit, "dep_1", StageSessionExpired, StatusFailed, "no payment within the window", 40*time.Minute),
	}

	tl := Assemble(events, base.Add(time.Hour))[0]

	if tl.State != "failed" {
		t.Fatalf("state = %q, want failed", tl.State)
	}
	if tl.BrokeAt != StagePaymentDetected {
		t.Fatalf("broke_at = %q, want the stage the expiry replaced (%s)", tl.BrokeAt, StagePaymentDetected)
	}
	node := stageByKey(t, tl, StagePaymentDetected)
	if node.Status != StatusFailed || node.FailureStage != StageSessionExpired {
		t.Fatalf("the replaced node must carry the failure: status=%q failure_stage=%q", node.Status, node.FailureStage)
	}
	if tl.Summary == "" {
		t.Error("a failed flow must say what to do next")
	}
}

// Blame decides who the user chases. Getting it wrong is the difference between
// "your wallet never sent it" and a support ticket accusing us of losing money.
func TestBlameNamesTheOwnerOfTheBrokenStage(t *testing.T) {
	cases := map[string]struct {
		events []Event
		want   string
	}{
		"awaiting the user's wallet": {
			events: []Event{ev(FlowDeposit, "dep_1", StageSessionCreated, StatusOK, "", 0)},
			want:   "your wallet",
		},
		"awaiting the chain": {
			events: []Event{
				ev(FlowDeposit, "dep_2", StageSessionCreated, StatusOK, "", 0),
				ev(FlowDeposit, "dep_2", StagePaymentDetected, StatusOK, "", time.Second),
			},
			want: "solana",
		},
		"our own credit failed": {
			events: []Event{
				ev(FlowDeposit, "dep_3", StageSessionCreated, StatusOK, "", 0),
				ev(FlowDeposit, "dep_3", StagePaymentDetected, StatusOK, "", time.Second),
				ev(FlowDeposit, "dep_3", StageChainFinalized, StatusOK, "", 2*time.Second),
				ev(FlowDeposit, "dep_3", StageCreditFailed, StatusFailed, "ledger post rejected", 3*time.Second),
			},
			want: "pyyol",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tl := Assemble(c.events, base.Add(2*time.Hour))[0]
			if tl.Blame != c.want {
				t.Fatalf("blame = %q, want %q (broke_at=%s)", tl.Blame, c.want, tl.BrokeAt)
			}
		})
	}
}

// A payment that is merely young must read as in-progress. Calling a 10-second-old
// deposit "stalled" teaches people to ignore the word, and then the real stall is
// invisible too.
func TestRecentIncompleteFlowIsInProgressNotStalled(t *testing.T) {
	events := []Event{ev(FlowDeposit, "dep_1", StageSessionCreated, StatusOK, "", 0)}

	tl := Assemble(events, base.Add(30*time.Second))[0]

	if tl.State != "in_progress" {
		t.Fatalf("state = %q, want in_progress for a 30-second-old deposit", tl.State)
	}
}

func TestOldIncompleteFlowIsStalled(t *testing.T) {
	events := []Event{ev(FlowDeposit, "dep_1", StageSessionCreated, StatusOK, "", 0)}

	tl := Assemble(events, base.Add(90*time.Minute))[0]

	if tl.State != "stalled" {
		t.Fatalf("state = %q, want stalled after 90 minutes", tl.State)
	}
	if tl.BrokeAt != StagePaymentDetected {
		t.Errorf("broke_at = %q, want the first incomplete stage", tl.BrokeAt)
	}
}

// A withdrawal waiting on a human reviewer is NORMAL for hours. Applying the
// deposit's 20-minute patience to it would flag every healthy payout as stuck.
func TestWithdrawalPatienceIsLongerThanDeposit(t *testing.T) {
	events := []Event{
		ev(FlowWithdrawal, "wd_1", StageRequested, StatusOK, "", 0),
		ev(FlowWithdrawal, "wd_1", StageCoinsHeld, StatusOK, "", time.Second),
	}

	sixHours := Assemble(events, base.Add(6*time.Hour))[0]
	if sixHours.State != "in_progress" {
		t.Fatalf("a 6-hour-old withdrawal awaiting review is normal, got %q", sixHours.State)
	}

	threeDays := Assemble(events, base.Add(72*time.Hour))[0]
	if threeDays.State != "stalled" {
		t.Fatalf("a 3-day-old withdrawal is stalled, got %q", threeDays.State)
	}
}

func TestFlowsAreSeparatedAndNewestFirst(t *testing.T) {
	events := []Event{
		ev(FlowDeposit, "dep_old", StageSessionCreated, StatusOK, "", 0),
		ev(FlowWithdrawal, "wd_new", StageRequested, StatusOK, "", time.Hour),
	}

	out := Assemble(events, base.Add(2*time.Hour))

	if len(out) != 2 {
		t.Fatalf("got %d timelines, want one per (flow, ref)", len(out))
	}
	if out[0].Ref != "wd_new" {
		t.Fatalf("timelines must be newest-first, got %q first", out[0].Ref)
	}
}

// A pending stage is not a failure. Marking "awaiting finality" as broken would
// accuse the platform of losing money that is simply in flight.
func TestPendingStageDoesNotFailTheFlow(t *testing.T) {
	events := []Event{
		ev(FlowDeposit, "dep_1", StageSessionCreated, StatusOK, "", 0),
		ev(FlowDeposit, "dep_1", StagePaymentDetected, StatusOK, "", time.Second),
		ev(FlowDeposit, "dep_1", StageChainFinalized, StatusPending, "awaiting finality", 2*time.Second),
	}

	tl := Assemble(events, base.Add(time.Minute))[0]

	if tl.State == "failed" {
		t.Fatalf("a pending stage must not fail the flow: %s", tl.Summary)
	}
	if tl.State != "in_progress" {
		t.Fatalf("state = %q, want in_progress", tl.State)
	}
}

// Every stage of every flow must carry copy. A diagram node with a blank caption
// or a blank recovery hint is worse than no diagram — it looks authoritative and
// says nothing.
func TestEveryStageOfEveryFlowIsFullyDescribed(t *testing.T) {
	for _, spec := range Specs() {
		if spec.Label == "" {
			t.Errorf("flow %q has no label", spec.Flow)
		}
		if len(spec.Stages) == 0 {
			t.Errorf("flow %q has no stages", spec.Flow)
		}
		for _, s := range spec.Stages {
			if s.Key == "" || s.Label == "" || s.Actor == "" || s.Description == "" || s.StuckHint == "" {
				t.Errorf("flow %q stage %q is incompletely described: %+v", spec.Flow, s.Key, s)
			}
		}
	}
}

// Every terminal failure must fold onto a stage that actually exists in its flow,
// or the break would silently vanish from the diagram.
func TestEveryFailureStageMapsOntoARealStage(t *testing.T) {
	known := map[string]string{} // stage key → flow
	for _, spec := range Specs() {
		for _, s := range spec.Stages {
			known[s.Key] = spec.Flow
		}
	}
	for failure, target := range failureStages {
		if _, ok := known[target]; !ok {
			t.Errorf("failure stage %q folds onto %q, which is not a stage of any flow", failure, target)
		}
	}
}
