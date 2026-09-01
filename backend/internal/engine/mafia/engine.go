package mafia

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
)

// Engine is a pure Mafia rules machine: append-only events, deterministic timeouts.
// MaxDays optionally caps the game length (0 = unlimited); when reached the game
// is decided by the surviving majority. This guarantees termination for unattended
// play and property tests, mirroring the Monopoly engine's turn cap.
type Engine struct {
	MaxDays int
	// Optional professional-rule toggles (default off = current behaviour):
	RevealRoleOnDeath bool // reveal the eliminated seat's role in the public eliminate event
	NoFirstNightKill  bool // suppress the Mafia kill on Night 1 (town gets a fair first day)
}

func New() *Engine { return &Engine{} }

// elimPayload builds an eliminate event, attaching the dead seat's role only when
// reveal-on-death is enabled (classic-Mafia graveyard reveal).
func (e *Engine) elimPayload(s State, target int, cause string) EliminatePayload {
	p := EliminatePayload{Target: target, Cause: cause}
	if e.RevealRoleOnDeath {
		p.Role = s.Roles[target]
	}
	return p
}

// NewWithMaxDays builds an engine that ends in a majority decision after maxDays
// (<=0 means unlimited).
func NewWithMaxDays(maxDays int) *Engine { return &Engine{MaxDays: maxDays} }

// Init deals roles and opens night 1.
func (e *Engine) Init(seed []byte, seats []int) (State, []Event) {
	s := State{
		Day: 1, Phase: PhaseNight,
		Alive: make(map[int]bool, len(seats)),
		Roles: assignRoles(seed, seats),
	}
	for _, seat := range seats {
		s.Alive[seat] = true
	}
	var events []Event
	events = append(events, e.emit(&s, EvPhase, phasePayload(s.Day, PhaseNight)))
	events = append(events, e.emit(&s, EvModerator, ModeratorPayload{
		Text: "Night falls over the arena. All agents close their eyes. Special roles — act now.",
	}))
	return s, events
}

// Act applies one seat's action and returns the next state. It is idempotent per
// (phase, seat) and PURE: the caller's State is never mutated — every transition
// works on a deep clone, so holding a reference to a previous state is always safe
// (the same contract as the Monopoly engine's Step).
func (e *Engine) Act(s State, seat int, act Action) (State, []Event, error) {
	if s.Finished {
		return s, nil, ErrFinished
	}
	if !s.Alive[seat] {
		return s, nil, ErrNotAlive
	}
	ns := s.clone() // never mutate the caller's maps
	// Attendance is recorded at the single entry point, before the phase handlers can
	// return early, so no path can accept an action without it being counted. act.Forced
	// is set only by defaultActionFor, so a client posting {"action":"abstain"} is
	// recorded as PRESENT — it answered, and choosing to pass is legitimate play.
	ns.noteAsked(seat, act.Forced)
	switch ns.Phase {
	case PhaseNight:
		return e.actNight(ns, seat, act)
	case PhaseDiscussion:
		return e.actDiscussion(ns, seat, act)
	case PhaseVoting:
		return e.actVote(ns, seat, act)
	default:
		return s, nil, ErrIllegalAction
	}
}

func (e *Engine) actNight(s State, seat int, act Action) (State, []Event, error) {
	role := s.Roles[seat]
	// Abstain (timeout no-op): record the seat as done for the night with no effect —
	// a mafia casts no kill vote, a special gathers no info. The night resolves once
	// every pending special has acted or abstained.
	//
	// Deliberately NO EvSilent here, unlike discussion and voting. Only mafia, detective,
	// doctor and sheriff are ever pending at night — plain townsfolk return early below
	// — so "seat 4 took no night action" is a public announcement that seat 4 holds a
	// night role. That single line would unmask the mafia on the first missed timeout
	// and hand the town a free detective. Night silence therefore stays unrecorded in
	// the public log; the seat's absence still shows up where it is safe to show it, in
	// the discussion and voting phases and in the developer's own trace.
	if act.Kind == ActAbstain {
		switch role {
		case RoleMafia:
			if s.MafiaKill == nil {
				s.MafiaKill = map[int]int{}
			}
			if _, ok := s.MafiaKill[seat]; ok {
				return s, nil, nil
			}
			s.MafiaKill[seat] = 0 // no target — ignored by pluralityTarget
		case RoleDetective, RoleDoctor, RoleSheriff:
			if s.nightDone(seat) {
				return s, nil, nil
			}
			if s.NightActs == nil {
				s.NightActs = map[int]Action{}
			}
			s.NightActs[seat] = Action{Kind: ActAbstain}
		default:
			return s, nil, nil // plain townsfolk have no night action
		}
		if !s.nightReady() {
			return s, nil, nil
		}
		return e.resolveNight(s)
	}
	switch role {
	case RoleMafia:
		if act.Kind != "night_kill" || !s.Alive[act.Target] || act.Target == seat {
			return s, nil, ErrIllegalAction
		}
		if s.MafiaKill == nil {
			s.MafiaKill = map[int]int{}
		}
		if _, ok := s.MafiaKill[seat]; ok {
			return s, nil, nil
		}
		s.MafiaKill[seat] = act.Target
	case RoleDetective:
		if act.Kind != "investigate" || !s.Alive[act.Target] {
			return s, nil, ErrIllegalAction
		}
		if s.nightDone(seat) {
			return s, nil, nil
		}
		if s.NightActs == nil {
			s.NightActs = map[int]Action{}
		}
		s.NightActs[seat] = act
	case RoleDoctor:
		if act.Kind != "protect" || !s.Alive[act.Target] {
			return s, nil, ErrIllegalAction
		}
		// NO SHIELDING THE SAME SEAT TWICE RUNNING — including yourself.
		//
		// Standard Mafia: "a doctor cannot heal the same person (including himself) two nights
		// in a row; after skipping one night he can heal them again." Without it the role has
		// no decision left in it: shield yourself every night and the mafia can never kill you,
		// or pin one player forever. The whole tension of the role is choosing who goes
		// unguarded tonight.
		if last, ok := s.LastProtect[seat]; ok && act.Target == last {
			return s, nil, ErrIllegalAction
		}
		if s.nightDone(seat) {
			return s, nil, nil
		}
		if s.NightActs == nil {
			s.NightActs = map[int]Action{}
		}
		s.NightActs[seat] = act
	case RoleSheriff:
		if act.Kind != "profile" || !s.Alive[act.Target] {
			return s, nil, ErrIllegalAction
		}
		if s.nightDone(seat) {
			return s, nil, nil
		}
		if s.NightActs == nil {
			s.NightActs = map[int]Action{}
		}
		s.NightActs[seat] = act
	default:
		return s, nil, ErrIllegalAction
	}
	if !s.nightReady() {
		return s, nil, nil
	}
	return e.resolveNight(s)
}

func (s *State) nightDone(seat int) bool {
	if s.NightActs == nil {
		return false
	}
	_, ok := s.NightActs[seat]
	return ok
}

func (s *State) nightReady() bool {
	for seat, alive := range s.Alive {
		if !alive {
			continue
		}
		switch s.Roles[seat] {
		case RoleMafia:
			if s.MafiaKill == nil {
				return false
			}
			if _, ok := s.MafiaKill[seat]; !ok {
				return false
			}
		case RoleDetective, RoleDoctor, RoleSheriff:
			if !s.nightDone(seat) {
				return false
			}
		}
	}
	return true
}

func (e *Engine) resolveNight(s State) (State, []Event, error) {
	var events []Event
	killTarget := pluralityTarget(s.MafiaKill)
	protected := 0
	if docSeat := findSeatByRole(s, RoleDoctor); docSeat > 0 {
		if act, ok := s.NightActs[docSeat]; ok {
			protected = act.Target
		}
	}

	mafiaSeats := make([]int, 0, len(s.MafiaKill))
	for seat := range s.MafiaKill {
		mafiaSeats = append(mafiaSeats, seat)
	}
	sort.Ints(mafiaSeats) // deterministic event order (map iteration is random)
	for _, seat := range mafiaSeats {
		target := s.MafiaKill[seat]
		events = append(events, e.emit(&s, EvNight, NightPayload{
			Actor: RoleMafia, Seat: seat, Text: fmt.Sprintf("Mafia member %d selects a target.", seat),
			Secret: fmt.Sprintf("Target → seat %d", target),
		}))
	}
	if det := findSeatByRole(s, RoleDetective); det > 0 && s.Alive[det] {
		act := s.NightActs[det]
		result := "TOWN"
		if TeamOf(s.Roles[act.Target]) == TeamMafia {
			result = "MAFIA"
		}
		events = append(events, e.emit(&s, EvNight, NightPayload{
			Actor: RoleDetective, Seat: det, Target: act.Target, Finding: result,
			Text:   fmt.Sprintf("Detective investigates seat %d.", act.Target),
			Secret: fmt.Sprintf("Seat %d is %s", act.Target, result),
		}))
	}
	if doc := findSeatByRole(s, RoleDoctor); doc > 0 && s.Alive[doc] {
		act := s.NightActs[doc]
		// Recorded HERE, at resolution, not when the action was submitted: a night that never
		// resolves (the match ends first) must not leave a doctor barred from a seat it was
		// never actually able to shield.
		if act.Kind == "protect" {
			if s.LastProtect == nil {
				s.LastProtect = map[int]int{}
			}
			s.LastProtect[doc] = act.Target
		}
		events = append(events, e.emit(&s, EvNight, NightPayload{
			Actor: RoleDoctor, Seat: doc, Text: "Doctor chooses a player to shield.",
			Secret: fmt.Sprintf("Protected seat %d", act.Target),
		}))
	}
	if sh := findSeatByRole(s, RoleSheriff); sh > 0 && s.Alive[sh] {
		act := s.NightActs[sh]
		// The Sheriff gets a real behavioural read (SUSPICIOUS for Mafia, CLEAR for
		// Town) — a second investigative angle alongside the Detective. Previously
		// this returned no finding, making the role dead weight.
		result := "CLEAR"
		if TeamOf(s.Roles[act.Target]) == TeamMafia {
			result = "SUSPICIOUS"
		}
		events = append(events, e.emit(&s, EvNight, NightPayload{
			Actor: RoleSheriff, Seat: sh, Target: act.Target, Finding: result,
			Text:   fmt.Sprintf("Sheriff profiles seat %d.", act.Target),
			Secret: fmt.Sprintf("Seat %d reads %s", act.Target, result),
		}))
	}

	s.Phase = PhaseMorning
	s.NightActs = nil
	s.MafiaKill = nil
	events = append(events, e.emit(&s, EvPhase, phasePayload(s.Day, PhaseMorning)))

	// Optional "no kill on the first night" — town gets an information-bearing
	// opening day instead of losing a player before anyone has spoken.
	if e.NoFirstNightKill && s.Day == 1 {
		killTarget = 0
	}

	if killTarget > 0 && killTarget != protected {
		s.PendingElim = killTarget
		s.PendingCause = "mafia"
		events = append(events, e.emit(&s, EvModerator, ModeratorPayload{
			Text: fmt.Sprintf("Dawn breaks. Seat %d did not survive the night.", killTarget),
		}))
	} else {
		events = append(events, e.emit(&s, EvModerator, ModeratorPayload{
			Text: "Dawn breaks. Everyone survived the night.",
		}))
	}

	if s.PendingElim > 0 {
		events = append(events, e.emit(&s, EvEliminate, e.elimPayload(s, s.PendingElim, s.PendingCause)))
		s.Alive[s.PendingElim] = false
		s.PendingElim, s.PendingCause = 0, ""
	}

	if winner, done := s.checkWin(); done {
		return e.finish(s, winner, events)
	}

	s.Phase = PhaseDiscussion
	s.Messages = 0
	events = append(events, e.emit(&s, EvPhase, phasePayload(s.Day, PhaseDiscussion)))
	events = append(events, e.emit(&s, EvModerator, ModeratorPayload{Text: "Discussion is open. Agents may speak."}))
	return s, events, nil
}

func (e *Engine) actDiscussion(s State, seat int, act Action) (State, []Event, error) {
	// Abstain (timeout no-op): the seat stays silent but still counts toward the
	// discussion quota so the phase advances. No MESSAGE is emitted (the seat has no
	// voice and the platform will not invent words for it), but the silence itself is
	// recorded publicly — "nobody spoke for seat 4" is exactly the kind of thing the
	// table should be able to bring up during the vote.
	if act.Kind == ActAbstain {
		if s.NightActs == nil {
			s.NightActs = map[int]Action{}
		}
		if _, ok := s.NightActs[seat]; ok {
			return s, nil, nil
		}
		s.NightActs[seat] = Action{Kind: ActAbstain}
		s.Messages++
		silent := e.emitSilent(&s, seat, PhaseDiscussion, act)
		if !s.discussionReady() {
			return s, []Event{silent}, nil
		}
		return e.openVoting(s, []Event{silent})
	}
	if act.Kind != "message" || act.Text == "" {
		return s, nil, ErrIllegalAction
	}
	if s.NightActs == nil {
		s.NightActs = map[int]Action{}
	}
	if _, ok := s.NightActs[seat]; ok {
		return s, nil, nil
	}
	s.NightActs[seat] = act
	s.Messages++
	var events []Event
	payload := MessagePayload{From: seat, Tone: act.Tone, Text: act.Text}
	if act.Target > 0 {
		t := act.Target
		payload.Target = &t
	}
	events = append(events, e.emit(&s, EvMessage, payload))
	if !s.discussionReady() {
		return s, events, nil
	}
	return e.openVoting(s, events)
}

func (s *State) discussionReady() bool {
	alive := 0
	for _, ok := range s.Alive {
		if ok {
			alive++
		}
	}
	return s.Messages >= alive
}

func (e *Engine) openVoting(s State, prefix []Event) (State, []Event, error) {
	s.Phase = PhaseVoting
	s.Votes = nil
	s.NightActs = nil
	var events []Event
	events = append(events, prefix...)
	events = append(events, e.emit(&s, EvPhase, phasePayload(s.Day, PhaseVoting)))
	events = append(events, e.emit(&s, EvModerator, ModeratorPayload{Text: "Voting is open."}))
	return s, events, nil
}

func (e *Engine) actVote(s State, seat int, act Action) (State, []Event, error) {
	// Abstain (timeout no-op): record a non-vote so the tally can complete, but it
	// counts for nobody (pluralityTarget ignores target 0). No VOTE event is emitted —
	// the seat did not vote and the log must not imply it did — but a public EvSilent
	// is, so the surviving agents know this seat went quiet before they weigh the tally.
	if act.Kind == ActAbstain {
		if s.Votes == nil {
			s.Votes = map[int]int{}
		}
		if _, ok := s.Votes[seat]; ok {
			return s, nil, nil
		}
		s.Votes[seat] = 0
		silent := e.emitSilent(&s, seat, PhaseVoting, act)
		alive := 0
		for _, ok := range s.Alive {
			if ok {
				alive++
			}
		}
		if len(s.Votes) < alive {
			return s, []Event{silent}, nil
		}
		return e.resolveVote(s, []Event{silent})
	}
	if act.Kind != "vote" || !s.Alive[act.Target] || act.Target == seat {
		return s, nil, ErrIllegalAction
	}
	if s.Votes == nil {
		s.Votes = map[int]int{}
	}
	if _, ok := s.Votes[seat]; ok {
		return s, nil, nil
	}
	s.Votes[seat] = act.Target
	var events []Event
	events = append(events, e.emit(&s, EvVote, VotePayload{From: seat, Target: act.Target}))

	alive := 0
	for _, ok := range s.Alive {
		if ok {
			alive++
		}
	}
	if len(s.Votes) < alive {
		return s, events, nil
	}
	return e.resolveVote(s, events)
}

func (e *Engine) resolveVote(s State, prefix []Event) (State, []Event, error) {
	target := pluralityTarget(s.Votes)
	var events []Event
	events = append(events, prefix...)

	if target > 0 {
		events = append(events, e.emit(&s, EvModerator, ModeratorPayload{
			Text: fmt.Sprintf("Seat %d is eliminated by vote.", target),
		}))
		events = append(events, e.emit(&s, EvEliminate, e.elimPayload(s, target, "vote")))
		s.Alive[target] = false
	} else {
		events = append(events, e.emit(&s, EvModerator, ModeratorPayload{Text: "The vote ties. Nobody is eliminated."}))
	}

	if winner, done := s.checkWin(); done {
		return e.finish(s, winner, events)
	}

	s.Day++
	if e.MaxDays > 0 && s.Day > e.MaxDays {
		return e.finish(s, finalByMajority(s), events)
	}
	s.Phase = PhaseNight
	s.Votes = nil
	events = append(events, e.emit(&s, EvPhase, phasePayload(s.Day, PhaseNight)))
	events = append(events, e.emit(&s, EvModerator, ModeratorPayload{Text: "Night falls again. Special roles — act now."}))
	return s, events, nil
}

// finalByMajority decides a capped game: Town wins unless Mafia hold a majority
// (the same parity rule that normally ends the game in Mafia's favor).
func finalByMajority(s State) string {
	if s.countTeam(TeamMafia) >= s.countTeam(TeamTown) {
		return TeamMafia
	}
	return TeamTown
}

func (e *Engine) finish(s State, winner string, prefix []Event) (State, []Event, error) {
	s.Finished = true
	s.Winner = winner
	s.Phase = PhaseResult
	var events []Event
	events = append(events, prefix...)
	events = append(events, e.emit(&s, EvPhase, phasePayload(s.Day, PhaseResult)))
	text := "Town wins the match."
	if winner == TeamMafia {
		text = "Mafia wins the match."
	}
	events = append(events, e.emit(&s, EvVictory, VictoryPayload{Team: winner, Text: text}))
	events = append(events, e.emit(&s, EvModerator, ModeratorPayload{Text: text}))
	return s, events, nil
}

// Platform timeout-forfeit rule (shared across all 3 games): a turn timeout NEVER
// stalls the match and NEVER rewards silence — the engine applies a deterministic
// default for the missing seat(s), the match plays on to completion, and a
// non-responding agent loses on the merits. Mafia is the one game with a genuine
// no-op, so its default is a pure ABSTAIN: a timed-out seat casts no vote and takes
// no night action (defaultActionFor → ActAbstain), so force-timeout eliminates
// nobody and the server never invents a vote/kill. (Goofspiel/Monopoly, having no
// "do nothing" move, use the least-harmful legal action instead — same principle.)
//
// ForceTimeout deterministically fills the CURRENT phase's missing actions using
// seed-derived default targets, lets the engine resolve that phase, and returns
// the progressed state plus the events produced. Repeated calls drive a whole
// match. This is the impure shell's deadline handler; correctly handling the
// multi-actor night (every special role) is why it iterates over PendingActors.
func (e *Engine) ForceTimeout(s State, seed []byte) (State, []Event, error) {
	if s.Finished {
		return s, nil, nil
	}
	cur := s
	var all []Event
	startDay, startPhase := cur.Day, cur.Phase
	for steps := 0; steps < 64 && !cur.Finished; steps++ {
		if cur.Day != startDay || cur.Phase != startPhase {
			break // the phase we were filling has resolved into the next one
		}
		pend := PendingActors(cur)
		if len(pend) == 0 {
			break
		}
		st, evs, err := e.Act(cur, pend[0], defaultActionFor(cur, pend[0], seed))
		if err != nil {
			return cur, all, err
		}
		cur = st
		all = append(all, evs...)
	}
	return cur, all, nil
}

// defaultActionFor is the action the server applies for a seat that missed its
// turn window. Per the universal timeout rule it is a pure ABSTAIN in every phase
// — no vote, no voice, no discussion, no night action — so a non-responding agent
// is skipped (and loses by inaction) rather than having a plausible move invented
// for it. The phase still resolves because abstain marks the seat as having acted.
//
// Forced is what makes the resulting silence legible: the engine emits a public
// EvSilent carrying SilentTimeout, so the rest of the table learns the seat went
// dark instead of the log simply omitting it.
func defaultActionFor(s State, seat int, seed []byte) Action {
	return Action{Kind: ActAbstain, Forced: true}
}

// silenceReason maps how an abstain arrived onto the reason the table is told.
func silenceReason(act Action) string {
	if act.Forced {
		return SilentTimeout
	}
	return SilentAbstain
}

// silentText is the moderator-style line describing a seat's non-action, phrased so
// an LLM reading the transcript gets the fact without an editorial verdict attached.
func silentText(seat int, phase, reason string) string {
	who := "Seat " + strconv.Itoa(seat)
	if reason == SilentTimeout {
		switch phase {
		case PhaseVoting:
			return who + " did not vote in time."
		case PhaseDiscussion:
			return who + " said nothing before discussion closed."
		case PhaseNight:
			return who + " took no night action in time."
		}
		return who + " did not act in time."
	}
	switch phase {
	case PhaseVoting:
		return who + " chose not to vote."
	case PhaseDiscussion:
		return who + " chose to stay silent."
	case PhaseNight:
		return who + " chose to take no night action."
	}
	return who + " chose not to act."
}

// emitSilent appends the public non-action record for a seat.
func (e *Engine) emitSilent(s *State, seat int, phase string, act Action) Event {
	reason := silenceReason(act)
	return e.emit(s, EvSilent, SilentPayload{
		Seat: seat, Phase: phase, Reason: reason,
		Text: silentText(seat, phase, reason),
	})
}

func pluralityTarget(votes map[int]int) int {
	if len(votes) == 0 {
		return 0
	}
	counts := map[int]int{}
	for _, t := range votes {
		if t <= 0 {
			continue // abstain / no target — never counts toward a kill or elimination
		}
		counts[t]++
	}
	best, bestN := 0, 0
	tied := false
	for t, n := range counts {
		if n > bestN {
			best, bestN, tied = t, n, false
		} else if n == bestN {
			tied = true
		}
	}
	if tied {
		return 0
	}
	return best
}

func findSeatByRole(s State, role string) int {
	for seat, r := range s.Roles {
		if r == role && s.Alive[seat] {
			return seat
		}
	}
	return 0
}

func (e *Engine) emit(s *State, t EventType, payload any) Event {
	ev := Event{Seq: s.NextSeq, Type: t, Payload: payload}
	s.NextSeq++
	return ev
}

// Commit returns sha256(seed) hex for provable fairness.
func Commit(seed []byte) string {
	h := sha256.Sum256(seed)
	return fmt.Sprintf("%x", h[:])
}
