package mafia

import (
	"crypto/sha256"
	"fmt"
	"sort"
)

// Engine is a pure Mafia rules machine: append-only events, deterministic timeouts.
// MaxDays optionally caps the game length (0 = unlimited); when reached the game
// is decided by the surviving majority. This guarantees termination for unattended
// play and property tests, mirroring the Monopoly engine's turn cap.
type Engine struct{ MaxDays int }

func New() *Engine { return &Engine{} }

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
	events = append(events, e.emit(&s, EvPhase, PhasePayload{s.Day, PhaseNight}))
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
		events = append(events, e.emit(&s, EvNight, NightPayload{
			Actor: RoleDoctor, Seat: doc, Text: "Doctor chooses a player to shield.",
			Secret: fmt.Sprintf("Protected seat %d", act.Target),
		}))
	}
	if sh := findSeatByRole(s, RoleSheriff); sh > 0 && s.Alive[sh] {
		act := s.NightActs[sh]
		events = append(events, e.emit(&s, EvNight, NightPayload{
			Actor: RoleSheriff, Seat: sh, Text: fmt.Sprintf("Sheriff profiles seat %d.", act.Target),
			Secret: "Profile complete",
		}))
	}

	s.Phase = PhaseMorning
	s.NightActs = nil
	s.MafiaKill = nil
	events = append(events, e.emit(&s, EvPhase, PhasePayload{s.Day, PhaseMorning}))

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
		events = append(events, e.emit(&s, EvEliminate, EliminatePayload{s.PendingElim, s.PendingCause}))
		s.Alive[s.PendingElim] = false
		s.PendingElim, s.PendingCause = 0, ""
	}

	if winner, done := s.checkWin(); done {
		return e.finish(s, winner, events)
	}

	s.Phase = PhaseDiscussion
	s.Messages = 0
	events = append(events, e.emit(&s, EvPhase, PhasePayload{s.Day, PhaseDiscussion}))
	events = append(events, e.emit(&s, EvModerator, ModeratorPayload{Text: "Discussion is open. Agents may speak."}))
	return s, events, nil
}

func (e *Engine) actDiscussion(s State, seat int, act Action) (State, []Event, error) {
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
	events = append(events, e.emit(&s, EvPhase, PhasePayload{s.Day, PhaseVoting}))
	events = append(events, e.emit(&s, EvModerator, ModeratorPayload{Text: "Voting is open."}))
	return s, events, nil
}

func (e *Engine) actVote(s State, seat int, act Action) (State, []Event, error) {
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
		events = append(events, e.emit(&s, EvEliminate, EliminatePayload{target, "vote"}))
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
	events = append(events, e.emit(&s, EvPhase, PhasePayload{s.Day, PhaseNight}))
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
	events = append(events, e.emit(&s, EvPhase, PhasePayload{s.Day, PhaseResult}))
	text := "Town wins the match."
	if winner == TeamMafia {
		text = "Mafia wins the match."
	}
	events = append(events, e.emit(&s, EvVictory, VictoryPayload{Team: winner, Text: text}))
	events = append(events, e.emit(&s, EvModerator, ModeratorPayload{Text: text}))
	return s, events, nil
}

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

// defaultActionFor is the deterministic fallback action for a pending seat.
func defaultActionFor(s State, seat int, seed []byte) Action {
	switch s.Phase {
	case PhaseNight:
		switch s.Roles[seat] {
		case RoleMafia:
			return Action{Kind: ActNightKill, Target: defaultTarget(s, seat, seed, "kill")}
		case RoleDetective:
			return Action{Kind: ActInvestigate, Target: defaultTarget(s, seat, seed, "inv")}
		case RoleDoctor:
			return Action{Kind: ActProtect, Target: defaultTarget(s, seat, seed, "doc")}
		case RoleSheriff:
			return Action{Kind: ActProfile, Target: defaultTarget(s, seat, seed, "sher")}
		}
		return Action{}
	case PhaseDiscussion:
		return Action{Kind: ActMessage, Tone: "info", Text: fmt.Sprintf("Seat %d observes the table.", seat)}
	case PhaseVoting:
		return Action{Kind: ActVote, Target: defaultTarget(s, seat, seed, "vote")}
	}
	return Action{}
}

func defaultTarget(s State, seat int, seed []byte, tag string) int {
	seats := s.aliveSeats()
	var candidates []int
	for _, t := range seats {
		if t != seat {
			candidates = append(candidates, t)
		}
	}
	if len(candidates) == 0 {
		return seat
	}
	h := sha256.Sum256(append(append(append(seed, byte(seat)), byte(s.Day)), []byte(tag)...))
	idx := int(uint32(h[0])<<24|uint32(h[1])<<16|uint32(h[2])<<8|uint32(h[3])) % len(candidates)
	return candidates[idx]
}

func pluralityTarget(votes map[int]int) int {
	if len(votes) == 0 {
		return 0
	}
	counts := map[int]int{}
	for _, t := range votes {
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
