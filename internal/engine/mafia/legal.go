package mafia

import "sort"

// legal.go exposes whose turn it is and what each seat may legally submit. Unlike
// a turn-based game, a Mafia phase has MANY simultaneous actors (every special
// role at night, every survivor in discussion/voting). PendingActors lists the
// seats still owed an action this phase; the Table runner drives them.

// Action kinds an agent submits.
const (
	ActNightKill   = "night_kill"
	ActInvestigate = "investigate"
	ActProtect     = "protect"
	ActProfile     = "profile"
	ActMessage     = "message"
	ActVote        = "vote"
)

// RoleActsAtNight reports whether a role submits a night action.
func RoleActsAtNight(role string) bool {
	switch role {
	case RoleMafia, RoleDetective, RoleDoctor, RoleSheriff:
		return true
	}
	return false
}

// actedNight reports whether a seat has already submitted its night action.
func actedNight(s State, seat int) bool {
	if s.Roles[seat] == RoleMafia {
		if s.MafiaKill == nil {
			return false
		}
		_, ok := s.MafiaKill[seat]
		return ok
	}
	if s.NightActs == nil {
		return false
	}
	_, ok := s.NightActs[seat]
	return ok
}

// spoke / voted report discussion/voting participation.
func spoke(s State, seat int) bool {
	if s.NightActs == nil {
		return false
	}
	_, ok := s.NightActs[seat]
	return ok
}

func voted(s State, seat int) bool {
	if s.Votes == nil {
		return false
	}
	_, ok := s.Votes[seat]
	return ok
}

// sortedAliveSeats returns the living seats in ascending order.
func sortedAliveSeats(s State) []int {
	var seats []int
	for seat, alive := range s.Alive {
		if alive {
			seats = append(seats, seat)
		}
	}
	sort.Ints(seats)
	return seats
}

// PendingActors returns the living seats that still owe an action in the current
// phase, in ascending seat order. Empty when the phase is fully submitted (the
// engine resolves on the final action) or the match is over.
func PendingActors(s State) []int {
	if s.Finished {
		return nil
	}
	var out []int
	for _, seat := range sortedAliveSeats(s) {
		switch s.Phase {
		case PhaseNight:
			if RoleActsAtNight(s.Roles[seat]) && !actedNight(s, seat) {
				out = append(out, seat)
			}
		case PhaseDiscussion:
			if !spoke(s, seat) {
				out = append(out, seat)
			}
		case PhaseVoting:
			if !voted(s, seat) {
				out = append(out, seat)
			}
		}
	}
	return out
}

// LegalActions returns the action kinds `seat` may submit right now (nil if the
// seat is dead, the match is over, or the seat has already acted this phase).
func LegalActions(s State, seat int) []string {
	if s.Finished || !s.Alive[seat] {
		return nil
	}
	switch s.Phase {
	case PhaseNight:
		if actedNight(s, seat) {
			return nil
		}
		switch s.Roles[seat] {
		case RoleMafia:
			return []string{ActNightKill}
		case RoleDetective:
			return []string{ActInvestigate}
		case RoleDoctor:
			return []string{ActProtect}
		case RoleSheriff:
			return []string{ActProfile}
		}
		return nil // villagers have no night action
	case PhaseDiscussion:
		if spoke(s, seat) {
			return nil
		}
		return []string{ActMessage}
	case PhaseVoting:
		if voted(s, seat) {
			return nil
		}
		return []string{ActVote}
	}
	return nil
}
