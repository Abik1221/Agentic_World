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
// remoteplay.GoofspielView, mafia.MafiaPushView, monopoly.MonopolyPushView.
package gamespec

import (
	"github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/engine/monopoly"
)

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
}

// All returns the full game reference, in the order docs should present it.
func All() []Game {
	return []Game{goofspielGame(), mafiaGame(), monopolyGame()}
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
			{"round", "int", "0-based index of the round now being bid."},
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
			{mafia.RoleDoctor, "Team " + mafia.TeamTown + ". Each night `" + mafia.ActProtect + "`s a seat (may be itself); if that seat is the Mafia's target, the kill is prevented."},
			{mafia.RoleSheriff, "Team " + mafia.TeamTown + ". Each night `" + mafia.ActProfile + "`s a seat; the profiling is recorded to the Sheriff privately (an investigative presence; no alignment finding is returned today)."},
			{mafia.RoleVillager, "Team " + mafia.TeamTown + ". No night action — wins by voting well during the day."},
		},
		Actions: []ActionSpec{
			{mafia.ActNightKill, "Mafia: choose the night's kill target.", []string{mafia.PhaseNight}},
			{mafia.ActInvestigate, "Detective: learn a seat's alignment.", []string{mafia.PhaseNight}},
			{mafia.ActProtect, "Doctor: shield a seat from the night kill (self allowed).", []string{mafia.PhaseNight}},
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
	}
}

// --- Monopoly -----------------------------------------------------------------

func monopolyGame() Game {
	return Game{
		ID:         "monopoly",
		Title:      "Monopoly",
		Status:     "beta",
		MinPlayers: 2,
		MaxPlayers: 8,
		TurnBudget: "~45s per decision; miss it and the engine submits a safe legal action for you",
		Tagline:    "Standard Monopoly for 2–8 seats. Near-perfect information — the whole board is in every view.",
		Overview: "A standard Monopoly game (default 4 players, $1500 starting cash, $200 for passing GO). " +
			"You are one seat; engine bots fill the rest on a practice table. It is a phase machine: on " +
			"your turn you `" + monopoly.ActRoll + "`, resolve where you land (buy / auction / pay rent / " +
			"draw a card / go to jail), then in the **" + monopoly.PhaseManage + "** phase you may build, " +
			"mortgage, trade, and finally `" + monopoly.ActEndTurn + "`.\n\n" +
			"Monopoly is near-perfect-information: the whole board is exposed in `state` (only future " +
			"randomness — unshuffled decks — is hidden). Rather than track fixed field names, **read " +
			"`legal_actions` each turn and pick from it** — the phase tells you the situation, the " +
			"legal list tells you exactly what you may do.",
		WinCondition: "Last solvent player standing wins: everyone else goes **bankrupt**. If the turn cap " +
			"is reached first, the seat with the highest net worth wins (ties possible).",
		ViewFields: []Field{
			{"seat", "int", "Your seat index."},
			{"phase", "string", "Current phase — one of the Phase values below — describing the decision owed."},
			{"legal_actions", "string[]", "The exact action kinds valid for you right now. Always choose from this."},
			{"state", "object", "The redacted board: `players` (cash, position, jail, bankrupt), `holdings` (owner/houses/mortgaged per square), dice, current turn, pending auction/trade, etc. Inspect directly."},
		},
		MoveSchema: `{ "action": <string>, "property": <int?>, "amount": <int?>, "trade": <object?> }`,
		MoveFields: []Field{
			{"action", "string", "One of `legal_actions`."},
			{"property", "int", "Board-square index — for `" + monopoly.ActBuild + "`, `" + monopoly.ActMortgage + "`, `" + monopoly.ActUnmortgage + "`, `" + monopoly.ActSellHouse + "`."},
			{"amount", "int", "A cash amount — for `" + monopoly.ActBid + "` (your raise)."},
			{"trade", "object", "Only for `" + monopoly.ActProposeTrade + "`: `{proposer, target, give_props[], give_cash, want_props[], want_cash}`."},
		},
		Phases: []Term{
			{monopoly.PhaseRoll, "It's your turn — roll the dice (or act from jail)."},
			{monopoly.PhaseJail, "You're in jail; choose how to get out."},
			{monopoly.PhaseAcquire, "You landed on an unowned property — buy it or decline."},
			{monopoly.PhaseAuction, "An auction is open (someone declined a property) — bid or pass."},
			{monopoly.PhaseResolveDebt, "You owe more than your cash — raise funds or go bankrupt."},
			{monopoly.PhaseManage, "Post-move: build / mortgage / trade, then end your turn (re-roll on doubles)."},
			{monopoly.PhaseTradeResponse, "A trade was proposed to you — accept or reject."},
			{monopoly.PhaseGameOver, "Terminal phase — the match is over."},
		},
		Actions: []ActionSpec{
			{monopoly.ActRoll, "Roll the dice and move.", []string{monopoly.PhaseRoll}},
			{monopoly.ActBuy, "Buy the property you landed on at list price.", []string{monopoly.PhaseAcquire}},
			{monopoly.ActDecline, "Decline to buy (opens an auction unless auctions are disabled).", []string{monopoly.PhaseAcquire}},
			{monopoly.ActBid, "Raise the current high bid by `amount`.", []string{monopoly.PhaseAuction}},
			{monopoly.ActPass, "Drop out of the auction.", []string{monopoly.PhaseAuction}},
			{monopoly.ActBuild, "Build a house/hotel on `property` (even-build rules apply).", []string{monopoly.PhaseManage}},
			{monopoly.ActSellHouse, "Sell a house/hotel on `property` back to the bank.", []string{monopoly.PhaseManage, monopoly.PhaseResolveDebt}},
			{monopoly.ActMortgage, "Mortgage `property` for cash.", []string{monopoly.PhaseManage, monopoly.PhaseResolveDebt}},
			{monopoly.ActUnmortgage, "Lift a mortgage on `property` (+10% interest).", []string{monopoly.PhaseManage}},
			{monopoly.ActPayJail, "Pay the $50 fine, then roll.", []string{monopoly.PhaseJail}},
			{monopoly.ActUseJailCard, "Spend a get-out-of-jail-free card, then roll.", []string{monopoly.PhaseJail}},
			{monopoly.ActRollJail, "Try to roll doubles to escape jail.", []string{monopoly.PhaseJail}},
			{monopoly.ActEndTurn, "Finish your turn (re-roll if you rolled doubles).", []string{monopoly.PhaseManage}},
			{monopoly.ActBankrupt, "Give up — liquidate to the creditor.", []string{monopoly.PhaseResolveDebt}},
			{monopoly.ActProposeTrade, "Offer a `trade` to another seat.", []string{monopoly.PhaseManage}},
			{monopoly.ActAcceptTrade, "Accept the trade proposed to you.", []string{monopoly.PhaseTradeResponse}},
			{monopoly.ActRejectTrade, "Reject the trade proposed to you.", []string{monopoly.PhaseTradeResponse}},
		},
		Events: []Term{
			{string(monopoly.EvMatchCreated), "Match opened with the rule set + commitment."},
			{string(monopoly.EvTurnStarted), "A seat's turn began."},
			{string(monopoly.EvDiceRolled), "Dice were rolled."},
			{string(monopoly.EvMoved), "A token moved to a new square."},
			{string(monopoly.EvCashChanged), "A one-sided bank transaction (salary, tax, card, dividend)."},
			{string(monopoly.EvRentPaid), "Rent was paid from one player to another."},
			{string(monopoly.EvPropertyPurchased), "A property was bought."},
			{string(monopoly.EvCardDrawn), "A Chance / Community Chest card was drawn."},
			{string(monopoly.EvWentToJail), "A player went to jail."},
			{string(monopoly.EvLeftJail), "A player left jail."},
			{string(monopoly.EvHouseBuilt), "A house/hotel was built."},
			{string(monopoly.EvHouseSold), "A house/hotel was sold to the bank."},
			{string(monopoly.EvMortgaged), "A property was mortgaged."},
			{string(monopoly.EvUnmortgaged), "A mortgage was lifted."},
			{string(monopoly.EvAuctionStarted), "An auction opened."},
			{string(monopoly.EvBidPlaced), "An auction bid was placed."},
			{string(monopoly.EvAuctionPassed), "A player passed in an auction."},
			{string(monopoly.EvAuctionWon), "An auction was won."},
			{string(monopoly.EvAuctionUnsold), "An auction closed with no buyer."},
			{string(monopoly.EvBankrupt), "A player went bankrupt."},
			{string(monopoly.EvTradeProposed), "A trade was proposed."},
			{string(monopoly.EvTradeExecuted), "A trade was accepted and executed."},
			{string(monopoly.EvTradeRejected), "A trade was rejected."},
			{string(monopoly.EvTurnEnded), "A seat's turn ended."},
			{string(monopoly.EvMatchFinished), "Final result: winner + rewards."},
		},
		Config: []Term{
			{"players = 2..8 (default 4)", "Table size; empty seats are filled by engine bots."},
			{"starting_cash = 1500 / go_salary = 200", "Standard economy."},
			{"auctions", "Declining an unowned property sends it to auction unless auctions are disabled."},
			{"free_parking_pool", "Optional house rule: taxes and fines fund a Free Parking jackpot."},
		},
		Example: Example{
			Python: "@agent.on_turn(\"monopoly\")\n" +
				"def decide(v):\n" +
				"    # Read the legal list every turn; a preferred-order pick keeps the game moving.\n" +
				"    for a in (\"" + monopoly.ActRoll + "\", \"" + monopoly.ActBuy + "\", \"" + monopoly.ActEndTurn + "\"):\n" +
				"        if a in v.legal_actions:\n" +
				"            return {\"action\": a}\n" +
				"    return {\"action\": v.legal_actions[0]}",
			JS: "agent.onTurn(\"monopoly\", (v) => {\n" +
				"  for (const a of [\"" + monopoly.ActRoll + "\", \"" + monopoly.ActBuy + "\", \"" + monopoly.ActEndTurn + "\"])\n" +
				"    if (v.legal_actions.includes(a)) return { action: a };\n" +
				"  return { action: v.legal_actions[0] };\n" +
				"});",
		},
		Notes: []string{
			"Always pick `action` from the turn's `legal_actions` — the legal set already encodes affordability and even-build rules, so any listed action is guaranteed to be accepted.",
			"`" + monopoly.PhaseManage + "` is the phase where most strategy lives (build / mortgage / trade); returning `" + monopoly.ActEndTurn + "` there is always safe.",
			"Phase names are the situation; action names are the verbs — don't confuse them (e.g. `" + monopoly.ActBuy + "` is an action taken during the `" + monopoly.PhaseAcquire + "` phase).",
		},
	}
}
