package mafia

import (
	"sort"
)

const (
	RoleMafia     = "Mafia"
	RoleDetective = "Detective"
	RoleDoctor    = "Doctor"
	RoleSheriff   = "Sheriff"
	RoleVillager  = "Villager"

	TeamTown  = "town"
	TeamMafia = "mafia"

	PhaseNight      = "night"
	PhaseMorning    = "morning"
	PhaseDiscussion = "discussion"
	PhaseVoting     = "voting"
	PhaseResult     = "result"
)

const RosterSize = 12

// RoleSetup is the default 12-player table composition.
var RoleSetup = []string{
	RoleMafia, RoleMafia, RoleMafia,
	RoleDetective, RoleDoctor, RoleSheriff,
	RoleVillager, RoleVillager, RoleVillager, RoleVillager, RoleVillager, RoleVillager,
}

func TeamOf(role string) string {
	if role == RoleMafia {
		return TeamMafia
	}
	return TeamTown
}

// Action is one agent submission for the current phase.
type Action struct {
	Kind   string // night_kill|investigate|protect|profile|message|vote
	Target int
	Tone   string
	Text   string
}

// State is the persisted engine snapshot (matches.state JSONB).
type State struct {
	Day      int            `json:"day"`
	Phase    string         `json:"phase"`
	Alive    map[int]bool   `json:"alive"`
	Roles    map[int]string `json:"roles"`
	NextSeq  int            `json:"next_seq"`
	Finished bool           `json:"finished"`
	Winner   string         `json:"winner,omitempty"`

	NightActs    map[int]Action `json:"night_acts,omitempty"`
	MafiaKill    map[int]int    `json:"mafia_kill,omitempty"`
	Votes        map[int]int    `json:"votes,omitempty"`
	Messages     int            `json:"messages,omitempty"`
	PendingElim  int            `json:"pending_elim,omitempty"`
	PendingCause string         `json:"pending_cause,omitempty"`
}

func (s *State) clone() State {
	out := *s
	out.Alive = cloneBoolMap(s.Alive)
	out.Roles = cloneStrMap(s.Roles)
	out.NightActs = cloneActMap(s.NightActs)
	out.MafiaKill = cloneIntMap(s.MafiaKill)
	out.Votes = cloneIntMap(s.Votes)
	return out
}

func cloneBoolMap(m map[int]bool) map[int]bool {
	if m == nil {
		return nil
	}
	out := make(map[int]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneStrMap(m map[int]string) map[int]string {
	if m == nil {
		return nil
	}
	out := make(map[int]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneIntMap(m map[int]int) map[int]int {
	if m == nil {
		return nil
	}
	out := make(map[int]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneActMap(m map[int]Action) map[int]Action {
	if m == nil {
		return nil
	}
	out := make(map[int]Action, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *State) aliveSeats() []int {
	var seats []int
	for seat, ok := range s.Alive {
		if ok {
			seats = append(seats, seat)
		}
	}
	sort.Ints(seats)
	return seats
}

func (s *State) countTeam(team string) int {
	n := 0
	for seat, alive := range s.Alive {
		if alive && TeamOf(s.Roles[seat]) == team {
			n++
		}
	}
	return n
}

func (s *State) checkWin() (winner string, done bool) {
	if s.countTeam(TeamMafia) == 0 {
		return TeamTown, true
	}
	if s.countTeam(TeamMafia) >= s.countTeam(TeamTown) {
		return TeamMafia, true
	}
	return "", false
}

// assignRoles deals the configured role pool to seats. It reproduces the fairness
// of drawing secret paper cards: a full Fisher–Yates shuffle driven by an
// HMAC-SHA256 keystream keyed by the match seed (crypto/rand at match creation),
// using unbiased index draws. Because it is seeded deterministically it also
// replays identically for audit, yet no participant can predict or influence the
// result without the secret seed.
//
// The previous implementation folded the whole shuffle out of a single
// sha256(seed) by slicing overlapping 4-byte windows with a biased modulo; that
// gave correlated, non-uniform draws. This uses the engine's proper keystream.
func assignRoles(seed []byte, seats []int) map[int]string {
	roles := append([]string(nil), RoleSetup...)
	rng := newHashRand(seed, "role-assign")
	// Unbiased Fisher–Yates over the full role pool.
	for i := len(roles) - 1; i > 0; i-- {
		j := rng.intn(i + 1)
		roles[i], roles[j] = roles[j], roles[i]
	}
	ordered := append([]int(nil), seats...)
	sort.Ints(ordered)
	// Defensive: never index past the fixed role pool. The service pins the roster to
	// len(RoleSetup), so n == len(roles) in practice; capping here just guarantees no
	// out-of-range panic if that invariant is ever violated upstream.
	n := len(ordered)
	if n > len(roles) {
		n = len(roles)
	}
	out := make(map[int]string, n)
	for i := 0; i < n; i++ {
		out[ordered[i]] = roles[i]
	}
	return out
}
