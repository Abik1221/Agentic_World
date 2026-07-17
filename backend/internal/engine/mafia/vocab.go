package mafia

// vocab.go declares Mafia's PUBLIC, agent-facing vocabulary as enumerable slices.
//
// The consts elsewhere in this package are the source of truth for the string
// VALUES; these slices are the source of truth for the SET of values an agent can
// observe on the wire. They are load-bearing: the developer-docs generator
// (cmd/gamespec) renders the game reference from them, and a drift test
// (internal/gamespec) fails the build if the docs and these slices disagree — so a
// new role/phase/action/event can never ship without also appearing in the docs.
//
// INVARIANT: when you add or remove a Role*/Phase*/Act*/Ev* const above, update
// the matching slice here in the same change.

// AllRoles is every role that can be assigned on a table, in table-composition
// order (the strongest information roles first, then plain townsfolk).
var AllRoles = []string{
	RoleMafia,
	RoleDetective,
	RoleDoctor,
	RoleSheriff,
	RoleVillager,
}

// AllTeams is every allegiance a role resolves to (see TeamOf).
var AllTeams = []string{
	TeamTown,
	TeamMafia,
}

// AllPhases is every phase value an agent may see in a view, in cycle order.
var AllPhases = []string{
	PhaseNight,
	PhaseMorning,
	PhaseDiscussion,
	PhaseVoting,
	PhaseResult,
}

// AllActions is every action kind an agent may submit (LegalActions returns a
// phase/role-appropriate subset).
var AllActions = []string{
	ActNightKill,
	ActInvestigate,
	ActProtect,
	ActProfile,
	ActMessage,
	ActVote,
}

// AllEventTypes is every event `type` that can appear in a view's public/private
// transcript or the spectator stream.
var AllEventTypes = []EventType{
	EvPhase,
	EvModerator,
	EvNight,
	EvMessage,
	EvVote,
	EvEliminate,
	EvVictory,
}
