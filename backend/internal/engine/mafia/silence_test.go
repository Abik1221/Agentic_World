package mafia

import "testing"

// silenceEvents returns every EvSilent in a log, decoded.
func silenceEvents(t *testing.T, log []Event) []SilentPayload {
	t.Helper()
	var out []SilentPayload
	for _, ev := range log {
		if ev.Type != EvSilent {
			continue
		}
		p, ok := ev.Payload.(SilentPayload)
		if !ok {
			t.Fatalf("EvSilent carried %T, want SilentPayload", ev.Payload)
		}
		out = append(out, p)
	}
	return out
}

// driveToPhase force-times-out until the table reaches `phase`, returning the state and
// the whole event log produced along the way.
func driveToPhase(t *testing.T, e *Engine, s State, phase string) (State, []Event) {
	t.Helper()
	var log []Event
	for i := 0; i < 200 && s.Phase != phase && !s.Finished; i++ {
		ns, evs, err := e.ForceTimeout(s, seed)
		if err != nil {
			t.Fatalf("ForceTimeout: %v", err)
		}
		s, log = ns, append(log, evs...)
	}
	if s.Phase != phase {
		t.Fatalf("never reached phase %q (stuck at %q, finished=%v)", phase, s.Phase, s.Finished)
	}
	return s, log
}

// A seat that misses its voting window must leave a PUBLIC mark on the transcript.
//
// This is the gap the arena shipped with: actVote recorded the non-vote so the tally
// could complete but emitted no event at all, so the log of a table where three agents
// crashed was byte-identical to one where they had voted and been ignored. Agents were
// asked to reason about each other while the platform withheld the single most telling
// fact available — who had stopped responding.
func TestVotingTimeoutEmitsPublicSilence(t *testing.T) {
	e := NewWithMaxDays(14)
	s, _ := e.Init(seed, StandardSeats())
	s, _ = driveToPhase(t, e, s, PhaseVoting)

	voter := 0
	for seat, alive := range s.Alive {
		if alive {
			voter = seat
			break
		}
	}
	if voter == 0 {
		t.Fatal("no living seat to time out")
	}

	_, evs, err := e.Act(s, voter, defaultActionFor(s, voter, seed))
	if err != nil {
		t.Fatalf("Act(abstain): %v", err)
	}

	got := silenceEvents(t, evs)
	if len(got) == 0 {
		t.Fatal("a timed-out voter produced no silent event — the other agents cannot see who went dark")
	}
	p := got[0]
	if p.Seat != voter {
		t.Errorf("silent seat=%d want %d", p.Seat, voter)
	}
	if p.Reason != SilentTimeout {
		t.Errorf("reason=%q want %q — a forced timeout is absence, not a chosen pass", p.Reason, SilentTimeout)
	}
	if p.Phase != PhaseVoting {
		t.Errorf("phase=%q want %q", p.Phase, PhaseVoting)
	}
	if p.Text == "" {
		t.Error("silent event carried no text; an LLM reading the transcript gets nothing from a bare seat number")
	}

	// A non-vote must still not be counted AS a vote.
	for _, ev := range evs {
		if ev.Type == EvVote {
			if vp, ok := ev.Payload.(VotePayload); ok && vp.From == voter {
				t.Fatal("a timed-out seat emitted a vote event — the log now claims it voted when it did not")
			}
		}
	}
}

// Reachable-but-passing is NOT absence, and the table is told which it was.
//
// The HTTP handler builds an Action straight from the request body, so an agent can
// post {"action":"abstain"}. That agent answered: it must not be labelled timed-out,
// because the forfeit rule keys off absence and a deliberate pass is legitimate play.
func TestVoluntaryAbstainIsNotReportedAsATimeout(t *testing.T) {
	e := NewWithMaxDays(14)
	s, _ := e.Init(seed, StandardSeats())
	s, _ = driveToPhase(t, e, s, PhaseVoting)

	voter := 0
	for seat, alive := range s.Alive {
		if alive {
			voter = seat
			break
		}
	}

	// Exactly what handler.go builds from {"action":"abstain"} — note Forced is false.
	_, evs, err := e.Act(s, voter, Action{Kind: ActAbstain})
	if err != nil {
		t.Fatalf("Act(voluntary abstain): %v", err)
	}
	got := silenceEvents(t, evs)
	if len(got) == 0 {
		t.Fatal("a voluntary abstain produced no silent event")
	}
	if got[0].Reason != SilentAbstain {
		t.Fatalf("reason=%q want %q — an agent that answered must never be recorded as absent",
			got[0].Reason, SilentAbstain)
	}
}

// Night silence must stay invisible, or the timeout rule becomes a role oracle.
//
// Only mafia, detective, doctor and sheriff are ever pending at night; plain townsfolk
// are never asked. So a public "seat 4 took no night action" announces that seat 4 holds
// a night role — it would unmask the mafia on their first missed timeout and hand the
// town a free detective. This asserts the asymmetry is deliberate and stays.
func TestNightSilenceIsNeverPublic(t *testing.T) {
	e := NewWithMaxDays(14)
	s, _ := e.Init(seed, StandardSeats())
	if s.Phase != PhaseNight {
		s, _ = driveToPhase(t, e, s, PhaseNight)
	}

	var nightEvents []Event
	for _, seat := range PendingActors(s) {
		ns, evs, err := e.Act(s, seat, defaultActionFor(s, seat, seed))
		if err != nil {
			t.Fatalf("Act(night abstain) seat %d: %v", seat, err)
		}
		s = ns
		nightEvents = append(nightEvents, evs...)
		if s.Phase != PhaseNight {
			break // the night resolved; anything after belongs to the next phase
		}
	}

	for _, p := range silenceEvents(t, nightEvents) {
		if p.Phase == PhaseNight {
			t.Fatalf("night silence was published for seat %d — this reveals that the seat holds "+
				"a night role, unmasking the mafia on their first timeout", p.Seat)
		}
	}
}

// The event is worthless if the redactor drops it before the agent sees it.
func TestSilenceReachesEverySeatsView(t *testing.T) {
	if !isPublic(EvSilent) {
		t.Fatal("EvSilent is not public, so BuildView drops it and no agent can act on it")
	}

	e := NewWithMaxDays(14)
	s, _ := e.Init(seed, StandardSeats())
	s, log := driveToPhase(t, e, s, PhaseVoting)

	voter := 0
	for seat, alive := range s.Alive {
		if alive {
			voter = seat
			break
		}
	}
	_, evs, err := e.Act(s, voter, defaultActionFor(s, voter, seed))
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	log = append(log, evs...)

	// Every OTHER living seat must be able to see it — that is the point.
	checked := 0
	for seat, alive := range s.Alive {
		if !alive || seat == voter {
			continue
		}
		v := BuildView(s, seat, log)
		found := false
		for _, ev := range v.Public {
			if ev.Type != EvSilent {
				continue
			}
			if p, ok := ev.Payload.(SilentPayload); ok && p.Seat == voter {
				found = true
			}
		}
		if !found {
			t.Fatalf("seat %d cannot see that seat %d went silent", seat, voter)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no other living seat to check against")
	}
}

// Payloads round-trip through the database as themselves, or BuildView's type
// assertions silently stop matching after a reload.
func TestSilentPayloadDecodes(t *testing.T) {
	raw := []byte(`{"seat":4,"phase":"voting","reason":"timeout","text":"Seat 4 did not vote in time."}`)
	got, err := DecodePayload(EvSilent, raw)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	p, ok := got.(SilentPayload)
	if !ok {
		t.Fatalf("decoded to %T, want SilentPayload", got)
	}
	if p.Seat != 4 || p.Reason != SilentTimeout || p.Phase != PhaseVoting {
		t.Fatalf("round-trip lost fields: %+v", p)
	}
}
