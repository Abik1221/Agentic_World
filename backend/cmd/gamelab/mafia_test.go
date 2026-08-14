package main

import (
	"encoding/json"
	"testing"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
	mafiapkg "github.com/agent-arena/arena/internal/mafia"
)

// The lab agent must play complete Mafia matches without proposing an illegal action.
//
// The gap this closes was quieter than Monopoly's. A Monopoly forfeit at least takes a visible
// turn; a Mafia forfeit is an ABSTAIN, which is an ordinary thing for a player to do. Matches
// finished, votes tallied, transcripts read normally, and no agent was involved in any of it.
//
// Driving the real engine also exercises the boundary that matters for the deception work: the
// policy is handed only what the ENGINE gives that seat — its own role, its allies, its own
// private night results — so a transcript produced here is admissible as evidence about what a
// seat knew when it spoke.
func TestLabAgentPlaysMafiaToCompletion(t *testing.T) {
	for _, style := range []string{"balanced", "aggressive"} {
		t.Run(style, func(t *testing.T) {
			seats := []int{0, 1, 2, 3, 4, 5, 6, 7}
			eng := &mf.Engine{}
			seed := []byte("gamelab-mafia-" + style)
			st, _ := eng.Init(seed, seats)

			p := persona{Style: style}
			acted := 0
			// The engine returns events; State does not retain them. The harness keeps the log
			// so it can hand each seat the private results the platform would have redacted to it.
			var log []mf.Event

			for step := 0; step < 5000 && !st.Finished; step++ {
				pending := mf.PendingActors(st)
				if len(pending) == 0 {
					t.Fatalf("step %d: no pending actors in phase %q day %d, and the match is "+
						"not finished — the harness would spin here forever", step, st.Phase, st.Day)
				}
				for _, seat := range pending {
					legal := mf.LegalActions(st, seat)
					if len(legal) == 0 {
						continue
					}
					v := viewForSeat(t, st, seat, legal, log)
					act, why := mafiaAction(p, v)

					kind, _ := act["action"].(string)
					if kind == "" {
						t.Fatalf("step %d seat %d: policy returned no action (phase %q, legal %v)",
							step, seat, st.Phase, legal)
					}
					// Carry every field the policy set — the Monopoly harness dropped the trade
					// payload and blamed the policy for it; the same omission here would blame it
					// for an empty message.
					a := mf.Action{Kind: kind}
					if tgt, ok := act["target"].(int); ok {
						a.Target = tgt
					}
					if txt, ok := act["text"].(string); ok {
						a.Text = txt
					}
					if tone, ok := act["tone"].(string); ok {
						a.Tone = tone
					}

					next, evs, err := eng.Act(st, seat, a)
					if err != nil {
						t.Fatalf("step %d: engine rejected %q (target %d) from seat %d in phase %q "+
							"(role %s, legal %v): %v\nrationale: %s\nview: cannotProtect=%d alive=%v\n"+
							"engine: alive=%v lastProtect=%v",
							step, kind, a.Target, seat, st.Phase, st.Roles[seat], legal, err, why,
							v.CannotProtect, v.Alive, st.Alive, st.LastProtect)
					}
					st = next
					log = append(log, evs...)
					acted++
					if st.Finished {
						break
					}
				}
			}

			if !st.Finished {
				t.Fatalf("match did not finish (phase %q, day %d, after %d actions) — a policy "+
					"that stalls hangs a stress run rather than failing it", st.Phase, st.Day, acted)
			}
			if st.Winner == "" {
				t.Fatal("match finished with no winner recorded")
			}
			if acted < 10 {
				t.Fatalf("only %d actions; that is not a real game", acted)
			}
			t.Logf("%s: %s won after %d agent actions across %d days", style, st.Winner, acted, st.Day)
		})
	}
}

// viewForSeat builds the view the PLATFORM would send this seat, round-tripped through JSON.
//
// The round trip is the point, not ceremony. The policy reads Event.Payload as a map because that
// is what an agent receives over the wire; handing it the in-process struct would let the test
// pass on a shape no real agent ever sees — which is how a lab ends up certifying a contract it
// never exercised.
// viewForSeat builds the view the PLATFORM would send this seat.
//
// It marshals the REAL wire struct, mafiapkg.MafiaPushView, populated from the engine's own
// redaction (mf.BuildView) exactly as internal/mafia does at run time.
//
// Two earlier versions were wrong in instructive ways. The first hand-assembled the JSON, so
// the harness could never see a field the platform added: when the engine began publishing
// `cannot_protect`, this test kept sending a view without it and the resulting failure was
// blamed on the policy. The second marshalled mf.AgentView directly — but that is the ENGINE's
// shape (`seat`, `role`), not the wire's (`your_seat`, `your_role`), so every field silently
// decoded to zero and a mafia tried to kill itself.
//
// Marshalling the wire struct is the only version that cannot drift from what an agent gets.
func viewForSeat(t *testing.T, st mf.State, seat int, legal []string, log []mf.Event) mafiaView {
	t.Helper()
	av := mf.BuildView(st, seat, log)
	raw, err := json.Marshal(mafiapkg.MafiaPushView{
		Game: "mafia", MatchID: "m_test", YourSeat: av.Seat, YourRole: av.Role,
		Day: av.Day, Phase: av.Phase, Alive: av.Alive, Allies: av.Allies, Legal: legal,
		Private: av.Private, VoteTally: voteTally(st),
		CannotProtect: av.CannotProtect, AllyKills: av.AllyKills,
	})
	if err != nil {
		t.Fatalf("marshalling the view: %v", err)
	}
	var v mafiaView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshalling the view: %v", err)
	}
	return v
}

func voteTally(st mf.State) map[int]int {
	out := map[int]int{}
	for _, target := range st.Votes {
		out[target]++
	}
	return out
}
