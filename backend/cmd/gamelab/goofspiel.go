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
	Game    string `json:"game"`
	MatchID string `json:"match_id"`
	Seat    int    `json:"seat"`
	Round   int    `json:"round"`
	// TurnProof is the per-turn token the platform mints (internal/turnproof). Shipped in the
	// view so an agent can attach it to its model call; without it a decision cannot be bound.
	TurnProof    string `json:"turn_proof"`
	CurrentPrize int    `json:"current_prize"`
	PrizePool    int    `json:"prize_pool"`
	YourHand     []int  `json:"your_hand"`
	// Two view shapes carry the score under different names, and the harness used to read
	// only one of them. The STAKED pushed view (internal/match/drive.go) sends
	// your_score/opponent_score; the sandbox/remoteplay view sends a scores[2] array
	// indexed by seat. Decoding only `scores` meant every staked match looked 0-0 for all
	// 13 rounds — the log said so, and worse, the fallback heuristic and every line of
	// table talk reasoned about a score that never moved. Decode BOTH; resolve in score().
	Scores       [2]int `json:"scores"`
	YourScore    int    `json:"your_score"`
	OppScore     int    `json:"opponent_score"`
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

// score returns this seat's score and its opponent's, from whichever view shape arrived.
// your_score/opponent_score wins when present because it is what the staked path sends;
// the scores[2] array is the sandbox fallback. Both agree at 0-0, so a genuine opening
// round resolves the same either way.
func (v goofspielView) score() (me, opp int) {
	if v.YourScore != 0 || v.OppScore != 0 {
		return v.YourScore, v.OppScore
	}
	return v.Scores[v.Seat], v.Scores[1-v.Seat]
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
	case "mafia":
		a.playMafia(w, r, raw)
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
	logMe, logOpp := v.score()
	a.log.Printf("round %2d  prize %2d (pool %2d)  scores %d-%d  hand %v  thinking %.1fs…",
		v.Round, v.CurrentPrize, v.PrizePool, logMe, logOpp, v.YourHand, think.Seconds())
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

	// COMPLETION BINDING. Ask the model — through the gateway, with the turn proof attached —
	// to report the move as a structured tool call, and play what it answers. The strategy above
	// still chooses; the gateway path is what makes that choice PROVABLE.
	if BindThroughGateway {
		// A BATCHED round the agent already decided. No model call: the anchor call planned
		// this round, the gateway bound it from that one completion, and the agent must now
		// play exactly what it planned — a span is a commitment, so submitting anything else
		// would be rejected as a substitution.
		if planned, ok := a.plannedCard(v.MatchID, v.Round); ok {
			play, note := planned, "played from the batched decision that also bound this round"
			// The negative half, INSIDE a span. A round covered by a batched decision is bound
			// exactly as an anchor round is, so submitting a different card here must be
			// refused too. Without this the span would be credited for coverage while being
			// enforced nowhere, which is the one way range bindings could become a loophole.
			if SubstituteAtRound > 0 && v.Round >= SubstituteAtRound {
				if sub, ok := substitutedCard(planned, legal); ok {
					a.log.Printf("round %2d  SUBSTITUTING INSIDE A SPAN — the batched call bound card %d, "+
						"submitting %d instead; the platform MUST reject this", v.Round, planned, sub)
					play = sub
					note = fmt.Sprintf("deliberate substitution inside a batched span: the plan said %d", planned)
				}
			}
			if play == planned {
				a.log.Printf("round %2d  → plays %2d   (from the batched plan; bound by the span, no new call)",
					v.Round, planned)
			}
			writeJSON(w, map[string]any{
				"round": v.Round, "card": play,
				"rationale": note,
				"usage":     a.usage(len(raw), think),
			})
			return
		}

		// An honest agent does not bind every turn. A failed provider call leaves the round
		// unbound and the agent PLAYS ON — see bindThisTurn. Skipping the call entirely
		// (rather than making it and discarding it) is what makes the resulting coverage figure
		// a real measurement of the honest population.
		if ok, why := bindThisTurn(v.MatchID, v.Round, v.Seat); !ok {
			a.log.Printf("round %2d  UNBOUND: %s", v.Round, why)
			writeJSON(w, map[string]any{
				"round": v.Round, "card": card,
				"rationale": "unbound this turn: " + why,
				"usage":     a.usage(len(raw), think),
			})
			return
		}

		// A batching agent decides several rounds in ONE call. span is length 1 otherwise, so
		// the non-batching path is byte-for-byte what it was.
		span := []planStep{{Round: v.Round, Card: card}}
		if isSpanAnchor(v.Round) {
			span = buildSpan(v.Round, card, legal, BindBatchRounds)
		}
		res, err := a.decideThroughGateway(v.MatchID, v.Round, card, span, v.TurnProof, legal, v.CurrentPrize, raw)
		if err != nil {
			// LOUD and fatal to the turn. A run that fell back to playing unbound would report
			// a completed match and prove nothing about binding, which is worse than failing.
			a.log.Printf("round %2d  BINDING FAILED — not playing: %v", v.Round, err)
			http.Error(w, "binding failed", http.StatusInternalServerError)
			return
		}
		card = res.Card
		why = fmt.Sprintf("bound to the model's own tool call (card %d)", res.Card)
		if len(res.Span) > 1 {
			// Remember the rest of the span. Those rounds are ALREADY bound — the gateway wrote
			// a row for each from this one completion — so the agent is committed to them.
			a.planFor(v.MatchID, res.Span[1:])
			why = fmt.Sprintf("bound to the model's own tool call (card %d); this call also decided %d later rounds",
				res.Card, len(res.Span)-1)
		}

		// The negative half of the proof: submit a card the model did NOT choose.
		if SubstituteAtRound > 0 && v.Round >= SubstituteAtRound {
			if sub, ok := substitutedCard(res.Card, legal); ok {
				a.log.Printf("round %2d  SUBSTITUTING — model bound card %d, submitting %d instead; "+
					"the platform MUST reject this", v.Round, res.Card, sub)
				card = sub
				why = fmt.Sprintf("deliberate substitution: model said %d", res.Card)
			} else {
				a.log.Printf("round %2d  cannot substitute — only one legal card (%d)", v.Round, res.Card)
			}
		}
	}

	a.log.Printf("round %2d  → plays %2d   (%s)", v.Round, card, why)
	writeJSON(w, map[string]any{
		"round":     v.Round,
		"card":      card,
		"rationale": why,
		"usage":     a.usage(len(raw), think),
		"scaffold":  a.scaffold(),
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
	me, opp := v.score()

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
	me, opp := v.score()
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
