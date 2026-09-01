// Package labdriver plays certified-ladder matches on the real Goofspiel engine.
//
// It is the last hop between the certification mathematics and an actual model call. Above it
// internal/ladder decides what to play and internal/exploit decides what the result means;
// below it internal/engine/goofspiel is the same pure engine the staked arena runs.
//
// # The experiment this exists to run
//
// The Lab benchmark is a CONTROLLED experiment, not a tournament. One agent implementation —
// identical code, identical prompts, identical scaffold — is pointed at many different
// providers and models, and each gets its own certification run. The arena's own scaffold
// fingerprint is built the same way and for the same reason: "the board fits models by
// holding the scaffold constant and swapping the model" (cmd/gamelab/agent.go:132-133).
//
// That design is what makes a difference between two runs attributable to the MODEL. In the
// staked arena the same comparison is confounded, because two developers differ in scaffold,
// prompt and model at once and no amount of statistics separates three things varied
// together. Here only one thing varies, so the confound is closed by construction rather than
// by adjustment.
//
// # Why a dedicated play loop
//
// remoteplay.PlayGoofspiel hardcodes DefaultConfig — standard 13-card shuffled Goofspiel.
// The ladder needs the spec's deck (n = 4..6) and FairnessOpen, because an exact solve is only
// tractable on a small deck and the open prize order is what collapses the information set to
// (my hand, opponent hand, carry). Using the 13-card loop would produce matches the solver
// cannot certify, so this drives the engine directly with the spec's config.
//
// Everything else is deliberately the SAME engine: the same Seal/Resolve transitions, the same
// legality checks, the same lowest-card fallback on a bad or missing answer. A certificate
// earned against a special-cased simulator would say nothing about how a model plays here.
package labdriver

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/exploit"
	"github.com/agent-arena/arena/internal/gops"
	"github.com/agent-arena/arena/internal/ladder"
	"github.com/agent-arena/arena/internal/remoteplay"
)

// AgentSource yields a decider for the agent under test.
//
// An interface rather than a concrete transport because the same ladder must be runnable
// against a developer's hosted endpoint, against the platform's own harness agent pointed at
// a chosen provider, and against an in-process fake in tests. The driver does not care which;
// it cares that every seat is asked the same question in the same shape.
type AgentSource interface {
	// Decider is called ONCE per phase and the result is shared across every match in that
	// phase, including matches running concurrently. The returned Decider must therefore be:
	//
	//   SAFE FOR CONCURRENT USE. NewPaced runs several matches at once; a decider carrying
	//   an unsynchronised *rand.Rand or a mutable buffer will race.
	//
	//   STATELESS ACROSS MATCHES, if the run is to be reproducible. A decider that carries
	//   state from one match into the next makes the result depend on the ORDER matches
	//   happened to interleave in, so a parallel run and a sequential run measure different
	//   things and no certificate can be replayed. The race detector found exactly this in
	//   a test double that shared one RNG across matches.
	//
	// labagent.LLMSource satisfies both: its decider holds only configuration and an
	// http.Client, which is itself safe for concurrent use.
	Decider(ctx context.Context, agentPublicID string, s ladder.Spec) (remoteplay.Decider, error)
}

// Driver implements ladder.Driver against the real engine.
type Driver struct {
	agents AgentSource
	pace   Pace
}

// New returns a SEQUENTIAL driver — the pre-existing behaviour, so an existing caller sees no
// change in load against its endpoint.
func New(a AgentSource) *Driver { return &Driver{agents: a} }

// NewPaced returns a driver that plays matches concurrently, bounded and paced. See
// concurrency.go for why both bounds are needed rather than just a worker pool.
func NewPaced(a AgentSource, p Pace) *Driver { return &Driver{agents: a, pace: p} }

// engineConfig is the spec expressed as the engine's own config.
//
// FairnessOpen, always: the certified ladder's whole tractability rests on the prize order
// being public, which is what reduces the information set to (my hand, opponent hand, carry).
// A shuffled ladder would need the revealed-prize history in the state and is deliberately
// out of scope rather than quietly approximated.
// engineConfigFor is the engine config for ONE prize order.
//
// Under FairnessOpen the engine reveals prizes in Cards order, so a randomised prize order is
// expressed by handing it a permuted deck. That is the whole mechanism: no engine change, no
// change to the information set, just a different game per match. See exploit/orders.go.
func engineConfigFor(s ladder.Spec, order []int) (goofspiel.Config, error) {
	if s.TieRule != goofspiel.TieCarry {
		return goofspiel.Config{}, fmt.Errorf("labdriver: unsupported tie rule %q", s.TieRule)
	}
	if len(order) != s.N {
		return goofspiel.Config{}, fmt.Errorf(
			"labdriver: prize order has %d entries, want N=%d", len(order), s.N)
	}
	return goofspiel.Config{
		Cards: append([]int(nil), order...), Rounds: s.N,
		FairnessMode: goofspiel.FairnessOpen, TieRule: goofspiel.TieCarry,
	}, nil
}

// matchSeed derives a deterministic seed so a run can be replayed exactly.
//
// Under FairnessOpen the prize order is fixed, so the seed changes nothing about the board —
// but it is still what the engine's Init consumes, and a certification that could not be
// re-driven to the same sequence would not be reproducible in the sense we are claiming.
func matchSeed(agentPublicID, phase string, index int) []byte {
	h := sha256.New()
	fmt.Fprintf(h, "pyyol-ladder-v1|%s|%s|%d", agentPublicID, phase, index)
	sum := h.Sum(nil)
	out := make([]byte, 32)
	copy(out, sum)
	binary.BigEndian.PutUint32(out[28:], uint32(index))
	return out
}

// proberDecider plays a precomputed pure strategy.
//
// It reconstructs the gops node from the view it is handed rather than tracking state, so it
// cannot drift out of step with the engine over a match. A node the strategy does not cover
// falls back to the lowest legal card — the same fallback the engine applies to a silent
// agent — and that is safe for the bound: an under-specified prober scores LESS, and a lower
// realised value is a WEAKER lower bound, never an invalid one.
type proberDecider struct {
	cfg   gops.Config
	moves map[gops.Node]int
}

func (p proberDecider) Decide(_ context.Context, v remoteplay.GoofspielView) (int, error) {
	node, ok := nodeFromView(p.cfg, v)
	if !ok {
		return lowest(v.LegalActions), nil
	}
	if card, hit := p.moves[node]; hit && node.Me&(1<<(card-1)) != 0 {
		return card, nil
	}
	return lowest(v.LegalActions), nil
}

// nodeFromView reuses the tested bridge so the driver and the estimator can never disagree
// about which node a view represents. Reconstructing it a second way here would be a second
// definition of the information set, and the two would drift.
func nodeFromView(cfg gops.Config, v remoteplay.GoofspielView) (gops.Node, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return gops.Node{}, false
	}
	// A legal card is supplied only so the bridge's own legality check passes; the returned
	// Card is discarded.
	probe := lowest(v.LegalActions)
	if probe == 0 {
		return gops.Node{}, false
	}
	obs, why := exploit.ObservationFromView(cfg, "probe", raw, probe)
	if why != exploit.RejectNone {
		return gops.Node{}, false
	}
	return obs.Node, true
}

func lowest(legal []int) int {
	best := 0
	for _, c := range legal {
		if best == 0 || c < best {
			best = c
		}
	}
	return best
}

// referenceDecider builds the Phase A opponent named by the spec.
func referenceDecider(s ladder.Spec, seed []byte) (remoteplay.Decider, error) {
	switch s.ReferenceOpponent {
	case ladder.RefNearestPool:
		return remoteplay.NearestPool{}, nil
	case ladder.RefUniform:
		return &uniformDecider{state: seedToU64(seed)}, nil
	default:
		return nil, fmt.Errorf("labdriver: unknown reference opponent %q", s.ReferenceOpponent)
	}
}

// uniformDecider bids uniformly over the legal cards.
//
// Its own splitmix64 rather than math/rand, for the reason internal/modelboard gives for the
// same choice: the stdlib generator's output has changed between Go releases, and a reference
// opponent that played differently after a toolchain upgrade would silently change what every
// past fit explored.
type uniformDecider struct{ state uint64 }

func (u *uniformDecider) Decide(_ context.Context, v remoteplay.GoofspielView) (int, error) {
	n := len(v.LegalActions)
	if n == 0 {
		return 0, fmt.Errorf("labdriver: no legal actions")
	}
	return v.LegalActions[int(u.next()%uint64(n))], nil
}

func (u *uniformDecider) next() uint64 {
	u.state += 0x9E3779B97F4A7C15
	z := u.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func seedToU64(seed []byte) uint64 {
	if len(seed) >= 8 {
		return binary.BigEndian.Uint64(seed[:8])
	}
	return 0x243F6A8885A308D3
}

// matchResult is one finished ladder match.
type matchResult struct {
	// AgentObs is every decision the agent under test made, at its own node.
	AgentObs []exploit.Observation
	// OppScore - AgentScore, i.e. the differential FROM THE OPPONENT'S SIDE. Phase B needs
	// the prober's payoff, and taking the sign the wrong way round would report an
	// exploitable agent as unexploitable while every test on synthetic data still passed.
	OppMinusAgent float64
	// Census counts decisions that could not be reconstructed.
	Census exploit.Census
}

// play runs one match: the agent on seat A, the opponent on seat B.
func play(ctx context.Context, ecfg goofspiel.Config, gcfg gops.Config, matchID string,
	agent, opp remoteplay.Decider, seed []byte) (matchResult, error) {

	e := goofspiel.New(ecfg)
	s, _ := e.Init(seed) // Init returns (State, []Event); it cannot fail on a validated config
	out := matchResult{Census: exploit.Census{Dropped: map[exploit.RejectReason]int{}}}

	for guard := 0; !s.Finished; guard++ {
		if guard > 10_000 {
			return matchResult{}, fmt.Errorf("labdriver: match did not terminate")
		}
		for seat := goofspiel.SeatA; seat <= goofspiel.SeatB; seat++ {
			legal := e.LegalActions(s, seat)
			if len(legal) == 0 {
				continue
			}
			view := viewFor(matchID, s, seat, legal)
			d := opp
			if seat == goofspiel.SeatA {
				d = agent
			}
			card := lowest(legal)
			if d != nil {
				if got, derr := d.Decide(ctx, view); derr == nil && contains(legal, got) {
					card = got
				}
				// An error or an illegal card falls through to the lowest legal card,
				// matching the engine's own behaviour in the arena. A model that answers
				// badly is measured as playing badly, which is the honest treatment: it is
				// what would have happened at a real table.
			}
			if seat == goofspiel.SeatA {
				record(gcfg, matchID, view, card, &out)
			}
			ns, _, serr := e.Seal(s, seat, card)
			if serr != nil {
				if ns, _, serr = e.Seal(s, seat, lowest(legal)); serr != nil {
					return matchResult{}, serr
				}
			}
			s = ns
		}
		ns, _, rerr := e.Resolve(s)
		if rerr != nil {
			return matchResult{}, rerr
		}
		s = ns
	}
	out.OppMinusAgent = float64(s.Scores[goofspiel.SeatB] - s.Scores[goofspiel.SeatA])
	return out, nil
}

func record(gcfg gops.Config, matchID string, v remoteplay.GoofspielView, card int, out *matchResult) {
	raw, err := json.Marshal(v)
	if err != nil {
		out.Census.Dropped[exploit.RejectMalformed]++
		return
	}
	obs, why := exploit.ObservationFromView(gcfg, matchID, raw, card)
	if why != exploit.RejectNone {
		out.Census.Dropped[why]++
		return
	}
	out.AgentObs = append(out.AgentObs, obs)
	out.Census.Kept++
}

// viewFor mirrors remoteplay's seat view. Duplicated rather than exported from remoteplay
// because that package's version is tied to its own 13-card loop; the shape is what the
// bridge parses and is pinned by TestViewShapeMatchesTheBridge.
func viewFor(matchID string, s goofspiel.State, seat int, legal []int) remoteplay.GoofspielView {
	return remoteplay.GoofspielView{
		Game: "goofspiel", MatchID: matchID, Seat: seat, Round: s.Round,
		CurrentPrize: s.CurrentPrize(), PrizePool: s.PrizePool,
		YourHand: append([]int(nil), s.Hands[seat]...),
		Scores:   s.Scores, LegalActions: append([]int(nil), legal...),
		History: historyFor(s, seat),
	}
}

func historyFor(s goofspiel.State, seat int) []remoteplay.RoundView {
	opp := 1 - seat
	out := make([]remoteplay.RoundView, 0, len(s.History))
	for _, r := range s.History {
		out = append(out, remoteplay.RoundView{
			Round: r.Round, Prize: r.Prize, PrizePool: r.PrizePool,
			YourCard: r.Cards[seat], OppCard: r.Cards[opp],
			Winner: r.Winner, Scores: r.Scores,
		})
	}
	return out
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// PlayFit runs Phase A: the agent against the spec's reference opponent, cycling the prize
// orders round-robin so every board is fitted equally.
func (d *Driver) PlayFit(ctx context.Context, agentPublicID string, s ladder.Spec, n, startIndex int) (
	[]exploit.Observation, exploit.Census, error) {

	cfgs, err := s.Games()
	if err != nil {
		return nil, exploit.Census{}, err
	}
	agent, err := d.agents.Decider(ctx, agentPublicID, s)
	if err != nil {
		return nil, exploit.Census{}, fmt.Errorf("labdriver: agent decider: %w", err)
	}

	type fitResult struct {
		obs    []exploit.Observation
		census exploit.Census
	}
	results, err := runMatches(ctx, d.pace, n, func(ctx context.Context, k int) (fitResult, error) {
		i := startIndex + k
		ord := exploit.OrderForMatch(i, len(cfgs))
		gcfg := cfgs[ord]
		ecfg, err := engineConfigFor(s, gcfg.Order)
		if err != nil {
			return fitResult{}, err
		}
		seed := matchSeed(agentPublicID, "fit", i)
		ref, err := referenceDecider(s, seed)
		if err != nil {
			return fitResult{}, err
		}
		id := fmt.Sprintf("fit-%s-%d", agentPublicID, i)
		r, err := play(ctx, ecfg, gcfg, id, agent, ref, seed)
		if err != nil {
			return fitResult{}, fmt.Errorf("labdriver: fit match %d: %w", i, err)
		}
		tagged := make([]exploit.Observation, 0, len(r.AgentObs))
		for _, o := range r.AgentObs {
			o.Order = ord
			tagged = append(tagged, o)
		}
		return fitResult{obs: tagged, census: r.Census}, nil
	})
	if err != nil {
		return nil, exploit.Census{}, err
	}

	var obs []exploit.Observation
	census := exploit.Census{Dropped: map[exploit.RejectReason]int{}}
	for _, r := range results {
		obs = append(obs, r.obs...)
		census.Kept += r.census.Kept
		for kk, v := range r.census.Dropped {
			census.Dropped[kk] += v
		}
	}
	return obs, census, nil
}

// PlayCertify runs Phase B: the agent against the pinned prober set, cycling orders.
//
// The payoff is the PROBER's. Payoffs from every order are pooled by the caller, which is
// correct: the per-order probers together are one strategy in the meta-game where nature
// draws the order, so the pooled mean estimates the average exploitability directly.
func (d *Driver) PlayCertify(ctx context.Context, agentPublicID string, s ladder.Spec,
	prober exploit.MultiProber, n, startIndex int) ([]ladder.MatchPayoff, error) {

	cfgs, err := s.Games()
	if err != nil {
		return nil, err
	}
	agent, err := d.agents.Decider(ctx, agentPublicID, s)
	if err != nil {
		return nil, fmt.Errorf("labdriver: agent decider: %w", err)
	}

	return runMatches(ctx, d.pace, n, func(ctx context.Context, k int) (ladder.MatchPayoff, error) {
		i := startIndex + k
		ord := exploit.OrderForMatch(i, len(cfgs))
		gcfg := cfgs[ord]
		ecfg, err := engineConfigFor(s, gcfg.Order)
		if err != nil {
			return ladder.MatchPayoff{}, err
		}
		seed := matchSeed(agentPublicID, "certify", i)
		// One mixture member, for THIS order, chosen deterministically from the match seed.
		p := proberDecider{cfg: gcfg, moves: prober.Select(ord, seed)}
		id := fmt.Sprintf("cert-%s-%d", agentPublicID, i)
		r, err := play(ctx, ecfg, gcfg, id, agent, p, seed)
		if err != nil {
			return ladder.MatchPayoff{}, fmt.Errorf("labdriver: certify match %d: %w", i, err)
		}
		return ladder.MatchPayoff{MatchID: id, Seq: i + 1, Payoff: r.OppMinusAgent}, nil
	})
}
