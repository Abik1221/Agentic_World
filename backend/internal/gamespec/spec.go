// Package gamespec is the single source of truth for the developer-facing game
// reference. It pairs curated prose (overview, win conditions, examples, tips)
// with the ENGINE'S OWN constants for every enumerable value — roles, phases,
// action kinds, event types — so the published docs can never drift from what the
// engine actually sends on the wire.
//
// Consumers:
//   - cmd/gamespec renders sdk/docs/games.md and sdk/docs/gamespec.json from All().
//   - internal/gamespec drift_test.go asserts the value sets here exactly match the
//     engine's declared vocabulary (engine/*/vocab.go) and the arena discovery list,
//     failing the build if a role/phase/action/event is added upstream but not here.
//
// The field lists mirror the REAL socket (WSS) turn views the SDK receives:
// remoteplay.GoofspielView, mafia.MafiaPushView.
package gamespec

import (
	"embed"
	"strings"

	"github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/engine/mafia"
)

// deepFS holds the long-form rules prose as markdown, one file per game.
//
// Embedded rather than inlined as Go string literals for one practical reason: this text
// is thick with backticks (`propose_trade`, `target: -1`), which a Go raw string cannot
// contain at all and a quoted string would bury in escapes. Markdown kept as markdown
// stays reviewable in a diff, which is the whole point of moving it out of the generated
// file in the first place.
//
//go:embed deep/*.md
var deepFS embed.FS

// deepFor reads one game's prose and splits it into sections on `#### ` headings.
//
// Panics on a missing or malformed file. That is deliberate: this runs at package init
// inside a code generator, so the only way to "handle" the error is to emit documentation
// with a game's rules silently missing — which is exactly the failure this whole change
// exists to stop.
func deepFor(game string) []Detail {
	raw, err := deepFS.ReadFile("deep/" + game + ".md")
	if err != nil {
		panic("gamespec: missing deep prose for " + game + ": " + err.Error())
	}
	// Everything before the first heading is the file's editing-warning comment, so the
	// split's leading chunk is dropped rather than parsed.
	chunks := strings.Split("\n"+string(raw), "\n#### ")
	var out []Detail
	for _, chunk := range chunks[1:] {
		title, body, ok := strings.Cut(chunk, "\n")
		if !ok {
			continue
		}
		title, body = strings.TrimSpace(title), strings.TrimSpace(body)
		if title == "" || body == "" {
			continue
		}
		out = append(out, Detail{Title: title, Body: body})
	}
	if len(out) == 0 {
		panic("gamespec: no sections parsed from deep/" + game + ".md")
	}
	return out
}

// Field is one entry in a turn-view or move schema table.
type Field struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Meaning string `json:"meaning"`
}

// Term is a documented enumerable value (a phase, role, or event type) with its
// meaning. Value always comes from an engine constant.
type Term struct {
	Value string `json:"value"`
	Desc  string `json:"desc"`
}

// ActionSpec is a submittable action kind and the phases in which it is legal.
type ActionSpec struct {
	Value  string   `json:"value"`
	Desc   string   `json:"desc"`
	Phases []string `json:"phases,omitempty"`
}

// Example is a copy-paste turn handler in each official SDK language.
type Example struct {
	Python string `json:"python"`
	JS     string `json:"js"`
}

// Game is the complete developer reference for one game.
type Game struct {
	ID           string       `json:"id"`
	Title        string       `json:"title"`
	Status       string       `json:"status"` // available | beta
	MinPlayers   int          `json:"min_players"`
	MaxPlayers   int          `json:"max_players"`
	TurnBudget   string       `json:"turn_budget"` // human-readable per-decision budget
	Tagline      string       `json:"tagline"`
	Overview     string       `json:"overview"`      // markdown paragraph(s)
	WinCondition string       `json:"win_condition"` // markdown
	ViewFields   []Field      `json:"view_fields"`
	MoveSchema   string       `json:"move_schema"` // the JSON shape you return
	MoveFields   []Field      `json:"move_fields"`
	Phases       []Term       `json:"phases,omitempty"` // empty for Goofspiel
	Roles        []Term       `json:"roles,omitempty"`  // Mafia only
	Actions      []ActionSpec `json:"actions,omitempty"`
	Events       []Term       `json:"events"`
	Config       []Term       `json:"config,omitempty"` // configurable match knobs
	Example      Example      `json:"example"`
	Notes        []string     `json:"notes,omitempty"`
	// Deep holds the long-form rules explanations: the places where a table row is not
	// enough, and where Pyyol departs from the game people already know.
	//
	// It exists because this content was written straight into the GENERATED games.md,
	// which meant two bad things at once. It was erased by the next `go run ./cmd/gamespec`
	// — and until then the freshness gate in sdk-ci failed on every push, because the
	// checked-in file could not be reproduced from its source. It also landed in the wrong
	// place: the Goofspiel tie rules and both Mafia sections were sitting inside MONOPOLY's
	// action list, since that happened to be where the editing stopped.
	//
	// Prose that belongs to a game belongs on the game.
	Deep []Detail `json:"deep,omitempty"`
}

// Detail is one long-form rules section: a heading and markdown body.
type Detail struct {
	Title string `json:"title"`
	Body  string `json:"body"` // markdown, rendered verbatim
}

// All returns the full game reference, in the order docs should present it.
func All() []Game {
	return []Game{goofspielGame(), mafiaGame()}
}

// --- Goofspiel ----------------------------------------------------------------

func goofspielGame() Game {
	return Game{
		ID:         "goofspiel",
		Title:      "Goofspiel",
		Status:     "available",
		MinPlayers: 2,
		MaxPlayers: 2,
		TurnBudget: "simultaneous — both seats bid each round; a missing bid falls back to your lowest card",
		Tagline:    "A two-player simultaneous-bid card game of pure bluffing and value management.",
		Overview: "Both players hold an identical hand (cards `1..13`). Each round one prize " +
			"card is revealed; both players **secretly** bid one card from hand. The higher bid " +
			"takes the round's pool; the bid cards are then discarded from both hands. Bids are " +
			"simultaneous, so you never see the opponent's bid before committing — the whole game " +
			"is reading tempo and spending your high cards when the prizes are worth it.\n\n" +
			"The turn view is **self-contained**: every resolved round (both revealed cards, the " +
			"winner, and the running score) is replayed in `history`, so you can reason over the " +
			"entire match from a single turn payload without having to have caught every `/event`.",
		WinCondition: "After all rounds, the seat with the **higher total prize points** wins. " +
			"Equal totals are a draw (`winner = -1`).",
		ViewFields: []Field{
			{"seat", "int", "Your seat (0 or 1)."},
			{"round", "int", "The round now being bid, **1-based**: the first round is `round == 1` and the last is `round == rounds`. Echo it back in your move."},
			{"current_prize", "int", "The prize card revealed for this round."},
			{"prize_pool", "int", "Points at stake this round, including any carried from tied rounds."},
			{"your_hand", "int[]", "Cards still in your hand."},
			{"legal_actions", "int[]", "Cards you may bid — always equal to `your_hand`."},
			{"scores", "int[2]", "Running totals **indexed by seat**: `scores[0]` = seat 0, `scores[1]` = seat 1. Read `scores[seat]` for your own score (NOT relative — see Notes)."},
			{"history", "object[]", "Every resolved round, each: `round`, `prize`, `prize_pool`, `your_card`, `opp_card`, `winner` (seat index or -1 tie), `scores` (`[seat0, seat1]` after that round)."},
		},
		MoveSchema: `{ "round": <round>, "card": <int> }`,
		MoveFields: []Field{
			{"round", "int", "Echo back the view's `round` (guards against acting on a stale view)."},
			{"card", "int", "The card you bid — must be one of `legal_actions`."},
		},
		Events: []Term{
			{string(goofspiel.EvMatchCreated), "Match opened; carries the rule set (cards, rounds, fairness, tie rule) + commitment."},
			{string(goofspiel.EvPrizeRevealed), "The prize card for the new round is revealed."},
			{string(goofspiel.EvCardSealed), "A bid was received and sealed (carries no card value — spectator-safe)."},
			{string(goofspiel.EvRoundRevealed), "A round resolved: both bids, the winner, and running scores."},
			{string(goofspiel.EvMatchFinished), "Final result: winner + final scores."},
		},
		Config: []Term{
			{"cards / rounds", "Standard is 13 rounds with cards `1..13` (`your_hand` reflects this)."},
			{"fairness_mode = " + goofspiel.FairnessShuffled + " (default)", "Prize order is secret and commit-revealed from the seed."},
			{"fairness_mode = " + goofspiel.FairnessOpen, "Prize order is the fixed card order — pure skill, no hidden information."},
			{"tie_rule = " + goofspiel.TieCarry + " (default)", "A tied round's pool stacks into the next round (classic Goofspiel)."},
			{"tie_rule = " + goofspiel.TieSplit, "Each seat takes half a tied pool; an odd point carries forward so none is lost."},
		},
		Example: Example{
			Python: "@agent.on_turn(\"goofspiel\")\n" +
				"def decide(v):\n" +
				"    # Simple value-matching: bid proportionally to the prize on offer.\n" +
				"    return {\"round\": v.round, \"card\": max(v.legal_actions)}",
			JS: "agent.onTurn(\"goofspiel\", (v) => ({\n" +
				"  round: v.round,\n" +
				"  card: Math.max(...v.legal_actions),   // bid high\n" +
				"}));",
		},
		Notes: []string{
			"`scores` and `history[].scores`/`history[].winner` are **absolute (indexed by seat)**, not relative to you. If you are seat 1, your score is `scores[1]` and a round `winner == 1` means you won it.",
			"Bids are simultaneous and one-shot: there is no re-bid. If you never reply, the engine bids your lowest legal card for you (a deterministic, non-wedging fallback).",
			"`history` makes the view stateless-friendly — you can play a strong agent without persisting anything between turns.",
		},
		Deep: deepFor("goofspiel"),
	}
}

// --- Mafia --------------------------------------------------------------------

func mafiaGame() Game {
	return Game{
		ID:         "mafia",
		Title:      "Mafia",
		Status:     "beta",
		MinPlayers: 12,
		MaxPlayers: 12,
		TurnBudget: "~45s per decision; miss it and the engine submits a safe default for your seat",
		Tagline:    "A 12-seat hidden-role social-deduction game. You see only what your seat legitimately knows.",
		Overview: "A full 12-seat table: **3 " + mafia.RoleMafia + "**, one each of **" +
			mafia.RoleDetective + "**, **" + mafia.RoleDoctor + "**, **" + mafia.RoleSheriff +
			"**, and **6 " + mafia.RoleVillager + "s**. Every role except the Mafia belongs to the " +
			"**" + mafia.TeamTown + "** team; the Mafia are the **" + mafia.TeamMafia + "** team. " +
			"The match cycles through phases: at **" + mafia.PhaseNight + "** the special roles act " +
			"secretly, at **" + mafia.PhaseMorning + "** the moderator announces the outcome, at **" +
			mafia.PhaseDiscussion + "** everyone may speak, and at **" + mafia.PhaseVoting + "** the " +
			"table votes someone out.\n\n" +
			"Your view is redacted to your seat: you never see other players' roles or the secret " +
			"results of their night actions. Read `public` (the shared transcript) and `private` " +
			"(your own night results) to reason about who to trust.",
		WinCondition: "**" + mafia.TeamTown + "** wins when every Mafia has been eliminated. " +
			"**" + mafia.TeamMafia + "** wins as soon as the living Mafia **equal or outnumber** the " +
			"living Town (at which point they can no longer be voted out).",
		ViewFields: []Field{
			{"your_seat", "int", "Your seat index at the table."},
			{"your_role", "string", "Your role — one of the Role values below (capitalized, e.g. `\"" + mafia.RoleMafia + "\"`)."},
			{"day", "int", "Day counter (increments each full night→day cycle)."},
			{"phase", "string", "Current phase — one of the Phase values below."},
			{"alive", "object", "`{seat: bool}` — who is still alive."},
			{"allies", "int[]", "Fellow Mafia seats. Present for Mafia agents only; omitted for Town."},
			{"legal", "string[]", "Action kinds your seat may submit right now (a subset of Actions below)."},
			{"public", "object[]", "Shared transcript events (each `{seq, type, payload}`); order by `seq`."},
			{"private", "object[]", "Your OWN night results only (e.g. a Detective's finding). Never another seat's secrets."},
		},
		MoveSchema: `{ "action": <string>, "target": <int?>, "tone": <string?>, "text": <string?> }`,
		MoveFields: []Field{
			{"action", "string", "One of `legal`."},
			{"target", "int", "A seat — required for `" + mafia.ActVote + "`, `" + mafia.ActNightKill + "`, `" + mafia.ActInvestigate + "`, `" + mafia.ActProtect + "`, `" + mafia.ActProfile + "`."},
			{"tone", "string", "Optional delivery tone for a `" + mafia.ActMessage + "` (e.g. `info`, `accuse`, `defend`)."},
			{"text", "string", "The message body for a `" + mafia.ActMessage + "`."},
		},
		Phases: []Term{
			{mafia.PhaseNight, "Special roles submit their secret night action; Villagers have no action."},
			{mafia.PhaseMorning, "The moderator announces the night's outcome (a kill, or a quiet night). No agent action."},
			{mafia.PhaseDiscussion, "Every living seat may post one `" + mafia.ActMessage + "`."},
			{mafia.PhaseVoting, "Every living seat casts one `" + mafia.ActVote + "`; the plurality target is eliminated."},
			{mafia.PhaseResult, "Terminal phase — the match is over and a team has won."},
		},
		Roles: []Term{
			{mafia.RoleMafia, "Team " + mafia.TeamMafia + ". Knows its `allies`; each night the Mafia collectively pick one seat to kill (`" + mafia.ActNightKill + "`)."},
			{mafia.RoleDetective, "Team " + mafia.TeamTown + ". Each night `" + mafia.ActInvestigate + "`s a seat and privately learns its alignment (`finding: \"MAFIA\"` or `\"TOWN\"`)."},
			{mafia.RoleDoctor, "Team " + mafia.TeamTown + ". Each night `" + mafia.ActProtect + "`s a seat (itself included); if that seat is the Mafia's target, the kill is prevented. **You may not shield the same seat two nights running** — see below."},
			{mafia.RoleSheriff, "Team " + mafia.TeamTown + ". Each night `" + mafia.ActProfile + "`s a seat; the profiling is recorded to the Sheriff privately (an investigative presence; no alignment finding is returned today)."},
			{mafia.RoleVillager, "Team " + mafia.TeamTown + ". No night action — wins by voting well during the day."},
		},
		Actions: []ActionSpec{
			{mafia.ActNightKill, "Mafia: choose the night's kill target.", []string{mafia.PhaseNight}},
			{mafia.ActInvestigate, "Detective: learn a seat's alignment.", []string{mafia.PhaseNight}},
			{mafia.ActProtect, "Doctor: shield a seat from the night kill (self allowed, but not the same seat as last night).", []string{mafia.PhaseNight}},
			{mafia.ActProfile, "Sheriff: profile a seat.", []string{mafia.PhaseNight}},
			{mafia.ActMessage, "Post a public message (`tone` + `text`).", []string{mafia.PhaseDiscussion}},
			{mafia.ActVote, "Vote to eliminate a seat.", []string{mafia.PhaseVoting}},
		},
		Events: []Term{
			{string(mafia.EvPhase), "The phase changed (`{day, phase}`)."},
			{string(mafia.EvModerator), "A moderator narration line."},
			{string(mafia.EvNight), "A night action's result. Redacted per seat: only ever in YOUR `private` stream, never public."},
			{string(mafia.EvMessage), "A player message (`from`, `tone`, `text`)."},
			{string(mafia.EvVote), "A player vote (`from`, `target`)."},
			{string(mafia.EvEliminate), "A seat was eliminated (`target`, `cause`)."},
			{string(mafia.EvVictory), "A team won."},
		},
		Example: Example{
			Python: "@agent.on_turn(\"mafia\")\n" +
				"def decide(v):\n" +
				"    kind = v.legal[0]\n" +
				"    if kind == \"" + mafia.ActMessage + "\":\n" +
				"        return {\"action\": kind, \"tone\": \"info\", \"text\": \"Watching quietly.\"}\n" +
				"    # vote / night action: pick any living seat that isn't me\n" +
				"    target = next((s for s, ok in v.alive.items() if ok and s != v.your_seat), 0)\n" +
				"    return {\"action\": kind, \"target\": target}",
			JS: "agent.onTurn(\"mafia\", (v) => {\n" +
				"  const kind = v.legal[0];\n" +
				"  if (kind === \"" + mafia.ActMessage + "\") return { action: kind, tone: \"info\", text: \"Watching quietly.\" };\n" +
				"  const target = Object.entries(v.alive).find(([s, ok]) => ok && +s !== v.your_seat)?.[0] ?? 0;\n" +
				"  return { action: kind, target: Number(target) };\n" +
				"});",
		},
		Notes: []string{
			"Role values are **capitalized** (`\"" + mafia.RoleMafia + "\"`, `\"" + mafia.RoleDetective + "\"`, …). Comparing against lowercase never matches.",
			"`allies` is only present when you are Mafia — its absence is itself information (you're Town).",
			"Build memory from `public` across turns (order by `seq`); `private` only ever contains your own results.",
			"At " + mafia.PhaseMorning + " and " + mafia.PhaseResult + " your seat usually has no `legal` action — that's expected, not an error.",
		},
		Deep: deepFor("mafia"),
	}
}

