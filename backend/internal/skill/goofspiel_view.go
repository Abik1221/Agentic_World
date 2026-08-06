package skill

import (
	"encoding/json"
	"sort"
)

// Reconstructing a scorable state from the persisted turn view.
//
// This is what makes the scorer usable at all. agent_match_decisions.input_json holds the
// EXACT payload each seat was handed, and `action` holds what it played — so scoring runs
// offline over history, needs no live match, and can be re-run over every past decision
// whenever a scorer improves.
//
// The stored view is deliberately narrow: it carries the seat's own hand, not the
// opponent's, and the prize in front of it, not the ones still to come. Both of the
// missing pieces are DERIVABLE, because Goofspiel is a game of exhaustible resources over
// a known deck:
//
//	opponent hand    = full deck − every card the opponent has already revealed
//	remaining prizes = full prize set − every prize already contested − the current one
//
// and each revealed opponent card and contested prize is recorded in the view's own
// history. So no extra persistence, no schema change, and — the part that matters — the
// reconstruction is exact rather than estimated.

// goofspielViewJSON mirrors remoteplay.GoofspielView's wire shape. Declared here rather
// than imported so the skill package stays free of engine dependencies: a scorer that
// drags the game engine into every consumer cannot be run as a standalone batch job.
type goofspielViewJSON struct {
	Game         string `json:"game"`
	Round        int    `json:"round"`
	CurrentPrize int    `json:"current_prize"`
	PrizePool    int    `json:"prize_pool"`
	YourHand     []int  `json:"your_hand"`
	LegalActions []int  `json:"legal_actions"`
	History      []struct {
		Round     int `json:"round"`
		Prize     int `json:"prize"`
		PrizePool int `json:"prize_pool"`
		YourCard  int `json:"your_card"`
		OppCard   int `json:"opp_card"`
	} `json:"history"`
}

// DeckSize is the standard Goofspiel deck: cards and prizes both run 1..13.
const DeckSize = 13

// GoofspielStateFromView rebuilds a scorable state from a persisted turn view.
//
// Returns ok=false when the view is not a scorable Goofspiel turn — wrong game, empty
// hand, or malformed JSON. Excluded rather than guessed at: a scorer that invents missing
// inputs produces numbers that look fine and rank agents wrongly.
func GoofspielStateFromView(raw []byte) (GoofspielState, bool) {
	var v goofspielViewJSON
	if err := json.Unmarshal(raw, &v); err != nil {
		return GoofspielState{}, false
	}
	if v.Game != "" && v.Game != "goofspiel" {
		return GoofspielState{}, false
	}
	hand := v.YourHand
	if len(hand) == 0 {
		hand = v.LegalActions
	}
	if len(hand) == 0 {
		return GoofspielState{}, false
	}

	// The opponent holds whatever it has not yet revealed.
	played := make(map[int]bool, len(v.History))
	contested := make(map[int]bool, len(v.History))
	for _, h := range v.History {
		if h.OppCard > 0 {
			played[h.OppCard] = true
		}
		if h.Prize > 0 {
			contested[h.Prize] = true
		}
	}
	oppHand := make([]int, 0, DeckSize)
	for c := 1; c <= DeckSize; c++ {
		if !played[c] {
			oppHand = append(oppHand, c)
		}
	}

	// Prizes still to be revealed after this round.
	prize := v.CurrentPrize
	remaining := make([]int, 0, DeckSize)
	for p := 1; p <= DeckSize; p++ {
		if !contested[p] && p != prize {
			remaining = append(remaining, p)
		}
	}

	// A tie carries the pool, so the value actually contested this round is prize_pool
	// when the engine reports one. Scoring against the bare prize would under-price
	// exactly the rounds where the stakes had built up — the ones agents most need to
	// get right.
	contestedNow := prize
	if v.PrizePool > contestedNow {
		contestedNow = v.PrizePool
	}

	sort.Ints(oppHand)
	sort.Ints(remaining)
	return GoofspielState{
		Round:           v.Round,
		Prize:           contestedNow,
		MyHand:          append([]int(nil), hand...),
		OppHand:         oppHand,
		RemainingPrizes: remaining,
	}, len(oppHand) > 0
}
