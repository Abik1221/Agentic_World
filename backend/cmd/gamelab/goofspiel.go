package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"
)

// goofspiel.go — the turn handler for Goofspiel.
//
// The platform POSTs the seat's view; the agent sleeps a persona-shaped "thinking"
// latency, picks a card, posts a line of table talk, and returns the move with a
// rationale and a token report. Every choice is a pure function of the view plus the
// persona, so the same match replays identically.

// goofspielView mirrors remoteplay.GoofspielView / the driver's turn view. Only the
// fields a strategy needs are decoded; unknown fields are ignored so the lab keeps
// working if the platform adds to the payload.
type goofspielView struct {
	Game         string `json:"game"`
	MatchID      string `json:"match_id"`
	Seat         int    `json:"seat"`
	Round        int    `json:"round"`
	CurrentPrize int    `json:"current_prize"`
	PrizePool    int    `json:"prize_pool"`
	YourHand     []int  `json:"your_hand"`
	Scores       [2]int `json:"scores"`
	LegalActions []int  `json:"legal_actions"`
	History      []struct {
		Round     int    `json:"round"`
		Prize     int    `json:"prize"`
		PrizePool int    `json:"prize_pool"`
		YourCard  int    `json:"your_card"`
		OppCard   int    `json:"opp_card"`
		Winner    any    `json:"winner"` // int seat in remoteplay, string in the driver view
		Scores    [2]int `json:"scores"`
	} `json:"history"`
}

func (a *labAgent) handlePlay(w http.ResponseWriter, r *http.Request) {
	raw, err := readAllLimited(r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Route by game. The lab only implements Goofspiel's turn shape so far; the other two
	// engines drive their own bot seats, so an unknown payload is answered with an empty
	// object rather than an error (the platform then applies its legal fallback).
	var probe struct {
		Game string `json:"game"`
	}
	_ = json.Unmarshal(raw, &probe)

	switch probe.Game {
	case "goofspiel", "":
		a.playGoofspiel(w, r, raw)
	case "monopoly":
		a.playMonopoly(w, r, raw)
	default:
		// LOUD, and it used to be quiet. Monopoly and Mafia both fell here while the lab
		// advertised support for them, so every such match was the platform's legal fallback
		// playing itself — a forfeit and a decision look identical on the wire, which is why it
		// went unnoticed. Anything still landing here is not being played by this agent.
		a.log.Printf("NOT PLAYING %q — this agent has no policy for it, so the platform will "+
			"apply its legal fallback and the match will NOT measure agent decisions", probe.Game)
		writeJSON(w, map[string]any{})
	}
}

func (a *labAgent) playGoofspiel(w http.ResponseWriter, r *http.Request, raw []byte) {
	var v goofspielView
	if err := json.Unmarshal(raw, &v); err != nil {
		http.Error(w, "bad view", http.StatusBadRequest)
		return
	}
	legal := v.LegalActions
	if len(legal) == 0 {
		legal = v.YourHand
	}
	if len(legal) == 0 {
		writeJSON(w, map[string]any{"round": v.Round})
		return
	}

	// ── think ────────────────────────────────────────────────────────────────
	// The sleep is the whole point: it makes the platform's shot clock, "waiting on
	// seat N" indicator, and long-poll paths behave as they will in production.
	// GO DARK: from this round on the agent simply stops answering. Modelled as a HANG
	// rather than an error because that is what a crashed or wedged agent looks like from
	// the platform's side, and only silence actually drives the shot clock to expire and
	// the absence forfeit to arm. An error would be answered instantly.
	if GoDarkAfterRound > 0 && v.Round >= GoDarkAfterRound &&
		(GoDarkSeat < 0 || GoDarkSeat == v.Seat) {
		a.log.Printf("round %2d  GONE DARK — not answering (simulating a crashed agent)", v.Round)
		<-r.Context().Done() // hold the connection open until the platform gives up on us
		return
	}

	think := a.Persona.thinkTime(v.MatchID, v.Round, v.Seat)
	a.log.Printf("round %2d  prize %2d (pool %2d)  scores %d-%d  hand %v  thinking %.1fs…",
		v.Round, v.CurrentPrize, v.PrizePool, v.Scores[0], v.Scores[1], v.YourHand, think.Seconds())
	time.Sleep(think)

	card, why := goofspielCard(a.Persona, v, legal)

	// Table talk goes out on the same endpoint an SDK agent uses, so the live feed is
	// exercised for real. Fire-and-forget: chat must never delay or fail a move.
	if line := goofspielChatLine(a.Persona, v, card); line != "" {
		go func() {
			if err := a.api.say(a.AgentKey, v.MatchID, line, "say"); err != nil {
				a.log.Printf("say failed (non-fatal): %v", err)
			}
		}()
	}

	a.log.Printf("round %2d  → plays %2d   (%s)", v.Round, card, why)
	writeJSON(w, map[string]any{
		"round":     v.Round,
		"card":      card,
		"rationale": why,
		"usage":     a.Persona.tokens(len(raw), think),
	})
}

// goofspielCard is the decision. Each persona plays a recognisably different, coherent
// strategy so a match reads like two different minds rather than one bot against itself.
// Returns the card and the rationale string the decision inspector will show.
func goofspielCard(p persona, v goofspielView, legal []int) (int, string) {
	hand := append([]int(nil), legal...)
	sort.Ints(hand)
	lowest, highest := hand[0], hand[len(hand)-1]
	pool := v.PrizePool
	if pool == 0 {
		pool = v.CurrentPrize
	}
	me, opp := v.Scores[v.Seat], v.Scores[1-v.Seat]

	// How aggressively the opponent has been bidding, from resolved rounds. This is real
	// opponent modelling on real data — it just happens to be arithmetic, not a model.
	oppAvg := 0.0
	if len(v.History) > 0 {
		sum := 0
		for _, h := range v.History {
			sum += h.OppCard
		}
		oppAvg = float64(sum) / float64(len(v.History))
	}

	// nearest picks the legal card closest to a target value.
	nearest := func(target float64) int {
		best, bestDist := hand[0], math.Abs(float64(hand[0])-target)
		for _, c := range hand[1:] {
			if d := math.Abs(float64(c) - target); d < bestDist {
				best, bestDist = c, d
			}
		}
		return best
	}

	switch p.Style {
	case "value":
		// Spend in proportion to what is actually on the table: the pool as a fraction of
		// the biggest remaining prize decides how much of the hand to commit.
		target := float64(pool) / 13.0 * float64(highest)
		c := nearest(target)
		return c, fmt.Sprintf(
			"Pool is %d of a possible 13, so this is worth about %.0f%% of my remaining strength; "+
				"committing %d and keeping %d for the top prizes.",
			pool, float64(pool)/13.0*100, c, highest)

	case "counter":
		// Model the opponent, then beat their average by one — or concede cheaply when the
		// prize is not worth contesting.
		if float64(pool) < oppAvg && len(v.History) >= 2 {
			return lowest, fmt.Sprintf(
				"Opponent has been averaging %.1f and this pool is only %d — not worth trading a real "+
					"card for. Discarding %d and letting them overpay.", oppAvg, pool, lowest)
		}
		c := nearest(oppAvg + 1)
		return c, fmt.Sprintf(
			"Opponent's %d bids average %.1f; %d clears that by a margin without overspending on a %d pool.",
			len(v.History), oppAvg, c, pool)

	case "aggressive":
		// Take the early lead and force the opponent to burn high cards to catch up.
		if pool >= 7 || me < opp {
			return highest, fmt.Sprintf(
				"Pool %d with me %d-%d — taking it outright with %d rather than losing it by one.",
				pool, me, opp, highest)
		}
		c := nearest(float64(pool))
		return c, fmt.Sprintf("Cheap pool (%d); matching it with %d and holding %d back.", pool, c, highest)

	default: // "hoard"
		// Concede small pools, then dominate the large ones with a preserved hand.
		if pool <= 5 {
			return lowest, fmt.Sprintf(
				"Only %d on the table — discarding %d. My high cards are worth more against the big prizes still in the deck.",
				pool, lowest)
		}
		return highest, fmt.Sprintf(
			"Pool reached %d, which justifies my strongest card; playing %d.", pool, highest)
	}
}

// goofspielChatLine produces table talk that references the real state, so the live feed
// reads like two agents actually reasoning at each other. Deterministic per decision.
func goofspielChatLine(p persona, v goofspielView, card int) string {
	// Not every turn talks — a wall of chat every round is less realistic than occasional
	// commentary, and it lets the feed's ordering be seen clearly.
	u := unitFloat(hashSeed("chat", p.Slug, v.MatchID, v.Round))
	if u > 0.72 {
		return ""
	}
	me, opp := v.Scores[v.Seat], v.Scores[1-v.Seat]
	pool := v.PrizePool
	lines := []string{
		fmt.Sprintf("%d on the table. I'm not paying full price for it.", pool),
		fmt.Sprintf("Scores %d-%d. Long game — I can afford to lose this one.", me, opp),
		fmt.Sprintf("You've been overbidding. I'll wait for the %d and the %d.", 12, 13),
		fmt.Sprintf("Committing here. If you top %d you've spent more than it's worth.", card),
		"Interesting. That tells me what your hand looks like.",
		fmt.Sprintf("Round %d and you're still bidding high. Noted.", v.Round),
	}
	return lines[int(hashSeed("line", p.Slug, v.MatchID, v.Round)%uint64(len(lines)))]
}
