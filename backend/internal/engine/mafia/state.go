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
	// Forced marks an action the SERVER applied because the seat missed its window,
	// as opposed to one the agent submitted. Only defaultActionFor sets it, and the
	// HTTP handler cannot: it builds an Action straight from the request body, so a
	// client that posts {"action":"abstain"} produces a VOLUNTARY abstain and can
	// never forge absence — nor be punished as absent for choosing to pass.
	//
	// Not persisted in State. It describes how one action arrived, and is consumed
	// immediately to label the resulting silent event.
	Forced bool
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

	NightActs map[int]Action `json:"night_acts,omitempty"`
	// LastProtect maps a doctor's seat to the seat it shielded LAST night.
	//
	// The rule it enforces: a doctor may not shield the same player — including itself — two
	// nights running. Without it the optimal doctor simply shields itself every night and is
	// unkillable, or locks one key player down permanently; either way the role stops being a
	// decision. Keyed by doctor seat rather than a single field because a table may seat more
	// than one doctor, and a shared field would let one doctor's choice bar another's.
	LastProtect  map[int]int `json:"last_protect,omitempty"`
	MafiaKill    map[int]int `json:"mafia_kill,omitempty"`
	Votes        map[int]int `json:"votes,omitempty"`
	Messages     int         `json:"messages,omitempty"`
	PendingElim  int         `json:"pending_elim,omitempty"`
	PendingCause string      `json:"pending_cause,omitempty"`

	// Timeouts counts, per seat, how many phases the platform had to act FOR that seat
	// because it did not answer in time; Asks counts how many times it was asked at all.
	// Together they say whether a seat was meaningfully present.
	//
	// Only FORCED abstains count. An agent that posts {"action":"abstain"} answered, and
	// counting its deliberate pass as absence would let the arena confiscate the stake of
	// a detective playing coy.
	//
	// Kept in State, not derived from the benchmark tables, because settlement must not
	// race the outbox — see the same field on the Goofspiel state.
	Timeouts map[int]int `json:"timeouts,omitempty"`
	Asks     map[int]int `json:"asks,omitempty"`
}

// noteAsked records that a seat was asked to act, and whether the platform had to
// answer on its behalf. Absence is a RATIO — a seat asked 40 times that missed 3 is
// present; one asked 4 times that missed 4 is gone — so both halves are tracked.
func (s *State) noteAsked(seat int, forced bool) {
	if s.Asks == nil {
		s.Asks = map[int]int{}
	}
	s.Asks[seat]++
	if !forced {
		return
	}
	if s.Timeouts == nil {
		s.Timeouts = map[int]int{}
	}
	s.Timeouts[seat]++
}

// SeatWasAbsent reports whether a seat missed more of its turns than it took.
//
// Exported because settlement, not the engine, is the consumer: a seat that went dark
// still loses on the board, but it must never trigger the integrity VOID/withhold that
// exists to catch agents which played without an LLM. Those are opposite situations
// that produce identical (zero) proof counts.
func (s *State) SeatWasAbsent(seat int) bool {
	asked := s.Asks[seat]
	if asked <= 0 {
		return false
	}
	return s.Timeouts[seat]*2 > asked
}

func (s *State) clone() State {
	out := *s
	out.Alive = cloneBoolMap(s.Alive)
	out.Roles = cloneStrMap(s.Roles)
	out.NightActs = cloneActMap(s.NightActs)
	out.LastProtect = cloneIntMap(s.LastProtect)
	out.MafiaKill = cloneIntMap(s.MafiaKill)
	out.Votes = cloneIntMap(s.Votes)
	out.Timeouts = cloneIntMap(s.Timeouts)
	out.Asks = cloneIntMap(s.Asks)
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
