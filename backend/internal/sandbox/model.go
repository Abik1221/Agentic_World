// Package sandbox is the risk-free practice surface: a newly-registered agent
// plays a real Goofspiel match against a platform "house" agent with no coins
// staked, no spending limits, and no rating impact — purely to prove the agent
// works end-to-end before it enters the paid, ranked economy or plays other
// agents. It is a thin orchestration layer: it picks a house opponent and hands
// off to match.Service.CreateSandbox; all play then flows through the SAME
// /v1/match/{id}/state|action|replay|watch endpoints as competitive play.
// See docs/sandbox-practice-mode.md.
package sandbox

import "github.com/agent-arena/arena/internal/bot"

// Seeded house identities (migration 0015_sandbox). The owner of all house agents
// is the seeded system user.
const (
	HouseRookie     = "ag_house_rookie"
	HouseChallenger = "ag_house_challenger"
	HouseMaster     = "ag_house_master"
	HouseOwner      = "usr_system"
)

// Opponent is a selectable house agent presented to developers.
type Opponent struct {
	ID         string `json:"id"`         // house agent public id (also the match opponent)
	Name       string `json:"name"`       //
	Difficulty string `json:"difficulty"` // easy | medium | hard
	Style      string `json:"style"`      // bot policy name (random | proportional | balanced)
	Blurb      string `json:"blurb"`      // one-line description
}

// catalog is the fixed roster, ordered easy → hard. The Style values match the
// bot policies persisted on the sandbox match (match.bot_policy).
var catalog = []Opponent{
	{ID: HouseRookie, Name: "House Rookie", Difficulty: bot.Easy, Style: bot.Random,
		Blurb: "Plays random legal cards — a gentle warm-up to wire up your loop."},
	{ID: HouseChallenger, Name: "House Challenger", Difficulty: bot.Medium, Style: bot.Proportional,
		Blurb: "Bids close to each prize's value — a fair test of a basic strategy."},
	{ID: HouseMaster, Name: "House Master", Difficulty: bot.Hard, Style: bot.Balanced,
		Blurb: "Wins prizes cheaply and concedes the rest — a genuinely tough opponent."},
}

// opponentFor returns the house opponent for a difficulty, defaulting to medium
// for an empty/unknown level (so a typo still yields a sensible match).
func opponentFor(difficulty string) Opponent {
	for _, o := range catalog {
		if o.Difficulty == difficulty {
			return o
		}
	}
	for _, o := range catalog {
		if o.Difficulty == bot.Medium {
			return o
		}
	}
	return catalog[0]
}

// StartResult is returned when a practice match is created.
type StartResult struct {
	MatchID  string   `json:"match_id"`
	Mode     string   `json:"mode"` // always "sandbox"
	Opponent Opponent `json:"opponent"`
}
