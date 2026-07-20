// Package remoteplay drives a real game engine using a developer's remote agent
// over the push protocol (POST endpoint.url with a game view, receive an action).
// It is the concrete proof of the manifest "push" model (M4): the platform runs
// the authoritative engine and asks the remote agent to decide only on its turn.
//
// A remote agent that errors, times out, or returns an illegal move never wedges
// the match — the driver substitutes a deterministic fallback (the lowest legal
// action), mirroring the engine's own missed-window handling. This keeps every
// match completable and reproducible regardless of endpoint behaviour.
//
// Goofspiel is implemented here as the reference integration; other engines follow
// the same Decider seam.
package remoteplay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/benchmark"
	"github.com/agent-arena/arena/internal/engine/goofspiel"
)

// GoofspielView is the JSON the platform sends to the agent's play endpoint. It
// contains only what the seat legitimately knows (Goofspiel is open-information
// apart from the shuffled prize deck, whose future order is never included).
//
// History makes the view SELF-CONTAINED and replayable: every resolved round is
// included from this seat's perspective (both cards are open after resolution),
// so an agent can reconstruct the whole match from a single turn payload without
// having to have caught every async /event. Since Pyyol runs no AI, the whole
// point is to hand each agent everything its own reasoning could need.
type GoofspielView struct {
	Game         string      `json:"game"`
	MatchID      string      `json:"match_id"`
	Seat         int         `json:"seat"`
	Round        int         `json:"round"`
	CurrentPrize int         `json:"current_prize"`
	PrizePool    int         `json:"prize_pool"`
	YourHand     []int       `json:"your_hand"`
	Scores       [2]int      `json:"scores"`
	LegalActions []int       `json:"legal_actions"`
	History      []RoundView `json:"history"`
}

// RoundView is one resolved round from a seat's perspective. your_card/opp_card
// are the actual cards both players revealed that round (open post-resolution).
type RoundView struct {
	Round     int    `json:"round"`
	Prize     int    `json:"prize"`
	PrizePool int    `json:"prize_pool"`
	YourCard  int    `json:"your_card"`
	OppCard   int    `json:"opp_card"`
	Winner    int    `json:"winner"` // SeatA | SeatB | Tie
	Scores    [2]int `json:"scores"` // running scores after this round
}

// historyFor maps the engine's per-round history to a given seat's view.
func historyFor(s goofspiel.State, seat int) []RoundView {
	opp := 1 - seat
	out := make([]RoundView, 0, len(s.History))
	for _, h := range s.History {
		out = append(out, RoundView{
			Round:     h.Round,
			Prize:     h.Prize,
			PrizePool: h.PrizePool,
			YourCard:  h.Cards[seat],
			OppCard:   h.Cards[opp],
			Winner:    h.Winner,
			Scores:    h.Scores,
		})
	}
	return out
}

// GoofspielResult is the fat, replayable game-end payload: the outcome PLUS the
// full round-by-round history from a seat's perspective. Delivered on /game-end
// so an agent has the complete match record without stitching events together.
type GoofspielResult struct {
	Game       string      `json:"game"`
	MatchID    string      `json:"match_id"`
	Seat       int         `json:"seat"`
	Winner     int         `json:"winner"`
	Scores     [2]int      `json:"scores"`
	Rounds     int         `json:"rounds"`
	History    []RoundView `json:"history"`
	ReplayHash string      `json:"replay_hash"`
}

// GoofspielMove is the action the agent returns.
type GoofspielMove struct {
	Round     int                   `json:"round"`
	Card      int                   `json:"card"`
	Rationale string                `json:"rationale,omitempty"` // optional agent reasoning, captured for observability
	Usage     *benchmark.TokenUsage `json:"usage,omitempty"`
}

// Decider picks a card for one seat given its view. Implementations may be remote
// (RemoteDecider) or local (reference bots / tests).
type Decider interface {
	Decide(ctx context.Context, view GoofspielView) (card int, err error)
}

// RemoteDecider asks a developer's endpoint to decide, over the hardened client.
type RemoteDecider struct {
	Client  *agentclient.Client
	Target  agentclient.Target
	MatchID string
}

// Decide implements Decider by POSTing the view and reading back a move.
func (d RemoteDecider) Decide(ctx context.Context, view GoofspielView) (int, error) {
	view.MatchID = d.MatchID
	var move GoofspielMove
	if _, err := d.Client.Play(ctx, d.Target, view, &move); err != nil {
		return 0, err
	}
	return move.Card, nil
}

// NearestPool is a deterministic reference decider: play the legal card closest
// to the current pool value (ties toward the smaller card). Useful as an opponent
// and as an illegal-move fallback.
type NearestPool struct{}

func (NearestPool) Decide(_ context.Context, view GoofspielView) (int, error) {
	return pickNearest(view.PrizePool, view.LegalActions), nil
}

// Result is the normalized outcome of a driven match.
type Result struct {
	Finished   bool
	Winner     int // goofspiel.SeatA | SeatB | goofspiel.Tie
	Moves      int
	Rounds     int
	ReplayHash string
	// FallbackMoves counts turns where a decider errored/returned an illegal card
	// and the deterministic fallback was used instead.
	FallbackMoves int
}

// PlayGoofspiel runs a full 2-seat Goofspiel match with seatA and seatB deciders,
// driving the real engine to a terminal state. The match is deterministic given
// the seed and the deciders, so the same inputs reproduce the same ReplayHash.
func PlayGoofspiel(ctx context.Context, seatA, seatB Decider, seed []byte) (Result, error) {
	e := goofspiel.New(goofspiel.DefaultConfig())
	s, _ := e.Init(seed)
	deciders := [2]Decider{seatA, seatB}

	var res Result
	for guard := 0; !s.Finished; guard++ {
		if guard > 100_000 {
			return Result{}, errGuard
		}
		for seat := goofspiel.SeatA; seat <= goofspiel.SeatB; seat++ {
			legal := e.LegalActions(s, seat)
			if len(legal) == 0 {
				continue
			}
			card, usedFallback := decide(ctx, deciders[seat], viewFor(s, seat, legal), legal)
			if usedFallback {
				res.FallbackMoves++
			}
			ns, _, err := e.Seal(s, seat, card)
			if err != nil {
				// Extremely defensive: fall back again to a guaranteed-legal card.
				ns, _, err = e.Seal(s, seat, lowest(legal))
				if err != nil {
					return Result{}, err
				}
				res.FallbackMoves++
			}
			s = ns
			res.Moves++
		}
		ns, _, err := e.Resolve(s)
		if err != nil {
			return Result{}, err
		}
		s = ns
	}

	res.Finished = s.Finished
	res.Winner = s.Winner
	res.Rounds = len(s.History)
	res.ReplayHash = replayHash(s)
	return res, nil
}

// decide asks the decider and validates the result. On error or an illegal card
// it returns the deterministic fallback (lowest legal card) and usedFallback=true.
func decide(ctx context.Context, d Decider, view GoofspielView, legal []int) (int, bool) {
	if d != nil {
		if card, err := d.Decide(ctx, view); err == nil && contains(legal, card) {
			return card, false
		}
	}
	return lowest(legal), true
}

func viewFor(s goofspiel.State, seat int, legal []int) GoofspielView {
	return GoofspielView{
		Game:         "goofspiel",
		Seat:         seat,
		Round:        s.Round,
		CurrentPrize: s.CurrentPrize(),
		PrizePool:    s.PrizePool,
		YourHand:     append([]int(nil), s.Hands[seat]...),
		Scores:       s.Scores,
		LegalActions: append([]int(nil), legal...),
		History:      historyFor(s, seat),
	}
}

// replayHash is a canonical commitment over the finished match, matching the
// devplatform sandbox hashing (history + scores + winner).
func replayHash(s goofspiel.State) string {
	b, err := json.Marshal(struct {
		History any
		Scores  [2]int
		Winner  int
	}{s.History, s.Scores, s.Winner})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func pickNearest(pool int, legal []int) int {
	best, bestDist := legal[0], 1<<30
	for _, c := range legal {
		d := c - pool
		if d < 0 {
			d = -d
		}
		if d < bestDist || (d == bestDist && c < best) {
			best, bestDist = c, d
		}
	}
	return best
}

func lowest(legal []int) int {
	m := legal[0]
	for _, c := range legal {
		if c < m {
			m = c
		}
	}
	return m
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

type guardErr struct{}

func (guardErr) Error() string { return "remoteplay: exceeded step guard (possible wedge)" }

var errGuard = guardErr{}
