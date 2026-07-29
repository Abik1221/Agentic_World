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

// chatLines is the house bots' table talk, grouped by what they are reacting to.
//
// Kept here as DATA rather than in chat.go, so tuning how the table sounds is editing
// a list — not touching the logic that decides when to speak. Adding a line needs no
// code change and cannot break determinism.
//
// Every line references BEHAVIOUR, never a seat number alone. "Seat 4 is suspicious"
// is noise a player learns to skip; "you were quiet all game and now you're certain"
// lands because it points at something that actually happened. That contingency is
// what makes a deterministic bot read as a participant.
//
// The tone is needling rather than abusive: sharp enough to feel like a real table
// under pressure, without shipping insults nobody would want their name attached to.
// %d, where present, is filled with the seat being addressed.
var chatLines = map[intent][]string{
	intentProbe: {
		"Quiet so far. Someone say something worth reading.",
		"No one's committed yet. That's usually deliberate.",
		"I've got nothing solid. Who's actually claiming anything?",
		"Day's young and everyone's hedging. Convenient.",
		"Somebody start, or we're all voting blind again.",
	},
	intentAccuse: {
		"Seat %d has been steering this the whole time.",
		"I don't buy seat %d's read. It's too convenient.",
		"Watch seat %d — that reasoning went nowhere and fast.",
		"Seat %d talks a lot without ever landing on a name.",
		"I'm on seat %d. Nothing they've said costs them anything.",
	},
	intentDefend: {
		"Seat %d, that's a reach and you know it.",
		"You're pointing at me because I'm easy, not because I'm wrong.",
		"If I were mafia I'd have played that far quieter.",
		"Fine — vote me. You'll waste the day and learn nothing.",
		"Seat %d is loud about me and silent about everyone else.",
	},
	intentDoubt: {
		"Seat %d, walk me through that. Slowly.",
		"That's a claim, not a case. Try again, seat %d.",
		"Seat %d changed their story between rounds. Anyone else catch that?",
		"You accused me the second pressure moved. That's a tell, seat %d.",
		"Seat %d is very sure for someone with nothing behind it.",
	},
	intentMourn: {
		"They went for seat %d. That tells us who felt threatened.",
		"Seat %d is gone. Whoever's quiet now was comfortable last night.",
		"Losing seat %d hurts. Let's not waste it on a random vote.",
		"That kill wasn't random. Ask who benefits.",
	},
	intentPressure: {
		"Seat %d hasn't said a word. That's a choice.",
		"Still waiting on seat %d. Silence is a position too.",
		"Seat %d, you're coasting. Say something or wear the vote.",
		"Nothing from seat %d all game. Comfortable, are we?",
	},
}
