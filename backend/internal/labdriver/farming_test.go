package labdriver

import (
	"context"
	"testing"

	"github.com/agent-arena/arena/internal/exploit"
	"github.com/agent-arena/arena/internal/gops"
	"github.com/agent-arena/arena/internal/ladder"
	"github.com/agent-arena/arena/internal/remoteplay"
)

// memorizingAgent is the attack. It keeps state ACROSS matches: once it has seen what the
// prober plays at a node, it plays the cheapest card that beats it.
//
// This is not strategy. It is a dict.
type memorizingAgent struct {
	cfg      gops.Config
	seen     map[gops.Node]int // node -> the card the prober played there
	fallback gops.Policy
	// salt replaces a shared *rand.Rand. The memoriser is DELIBERATELY stateful — that is
	// the attack — but its fallback sampling must not be, or the run stops being replayable
	// for a reason unrelated to the attack being tested.
	salt int64
}

func (m *memorizingAgent) Decide(_ context.Context, v remoteplay.GoofspielView) (int, error) {
	node, ok := nodeFromView(m.cfg, v)
	if !ok {
		return lowest(v.LegalActions), nil
	}
	// Learn from this match's revealed history: at each past round we now know the card the
	// opponent played, and the node it was played at is reconstructible from the prefix.
	m.learn(v)

	if probeCard, known := m.seen[mirror(node)]; known {
		// Cheapest card that beats the prober's known move; if none, dump the lowest.
		best := 0
		for _, c := range v.LegalActions {
			if c > probeCard && (best == 0 || c < best) {
				best = c
			}
		}
		if best != 0 {
			return best, nil
		}
		return lowest(v.LegalActions), nil
	}
	mix := m.fallback.Step(m.cfg, node)
	u, acc := positionRand(m.salt, v.MatchID, node, m.cfg.Order), 0.0
	for i, p := range mix {
		if acc += p; u <= acc {
			return i + 1, nil
		}
	}
	return lowest(v.LegalActions), nil
}

// learn walks this match's revealed history and records the opponent's card at each node.
func (m *memorizingAgent) learn(v remoteplay.GoofspielView) {
	me, opp := m.cfg.FullHand(), m.cfg.FullHand()
	carry := 0
	for _, h := range v.History {
		if h.YourCard < 1 || h.OppCard < 1 {
			return
		}
		// The opponent decided at ITS node: (its hand, my hand, carry).
		m.seen[gops.Node{Me: opp, Opp: me, Carry: carry}] = h.OppCard
		pot := h.PrizePool
		if pot == 0 {
			pot = h.Prize
		}
		next := 0
		if h.YourCard == h.OppCard {
			next = pot
		}
		me &^= 1 << (h.YourCard - 1)
		opp &^= 1 << (h.OppCard - 1)
		carry = next
	}
}

func mirror(n gops.Node) gops.Node {
	return gops.Node{Me: n.Opp, Opp: n.Me, Carry: n.Carry}
}

// TestProberCanBeMemorized refutes a claim the exploit package doc makes.
//
// internal/exploit says the metric "cannot be gamed, by construction" because "the prober is
// computed against YOU" and "there is no pool to manipulate". That is true of exploitability
// as a DEFINITION and false of THIS MEASUREMENT, for a reason that is entirely an artefact of
// how the ladder is currently configured:
//
//   - labdriver forces PrizeOrder to 1..N and the engine runs FairnessOpen, so every match of
//     every run is the SAME 566-node position.
//   - the prober is a PURE deterministic strategy, pinned once and replayed up to 1,024 times.
//
// So an agent with cross-match memory learns the prober outright and counter-exploits it. The
// bound then reports the agent as unexploitable — the most flattering possible verdict — for
// an agent that is in fact MORE exploitable than an honest one, since a counter-exploiting
// policy is itself wide open to a different best response.
//
// The attack also breaks the i.i.d. assumption the concentration bound rests on, so the
// published number is not merely wrong, it is unsupported.
func TestProberCanBeMemorized(t *testing.T) {
	spec := ladder.DefaultSpec()
	gcfg, err := spec.Game()
	if err != nil {
		t.Fatal(err)
	}
	base := skewed(gcfg, 77)
	allCfgs, err := spec.Games()
	if err != nil {
		t.Fatal(err)
	}

	measure := func(agent remoteplay.Decider, sp ladder.Spec, mixSize int) (mean float64, lb float64) {
		t.Helper()
		cfgs, err := sp.Games()
		if err != nil {
			t.Fatal(err)
		}
		counts := exploit.TallyOrdered(fitObsFor(t, sp, base))
		mix, err := exploit.BootstrapMultiProbers(cfgs, counts, sp.Alpha, mixSize, 4242)
		if err != nil {
			t.Fatal(err)
		}
		d := New(fixedSource{agent})
		payoffs, err := d.PlayCertify(context.Background(), "ag", sp, mix, 512, 0)
		if err != nil {
			t.Fatal(err)
		}
		vals := make([]float64, len(payoffs))
		for i, p := range payoffs {
			vals[i] = p.Payoff
			mean += p.Payoff
		}
		mean /= float64(len(vals))
		cert, err := exploit.Certify(cfgs[0], vals, sp.Delta)
		if err != nil {
			t.Fatal(err)
		}
		return mean, cert.LowerBound
	}

	newCheat := func() remoteplay.Decider {
		return &memorizingAgent{cfg: gcfg, seen: map[gops.Node]int{},
			fallback: base, salt: 77}
	}
	newHonest := func() remoteplay.Decider {
		return &scriptedAgent{cfg: gcfg, cfgs: allCfgs, pol: base, salt: 77}
	}

	// One board, one prober: the original vulnerable configuration.
	oneBoard := spec
	oneBoard.PrizeOrders = [][]int{spec.PrizeOrders[0]}
	h1, l1 := measure(newHonest(), oneBoard, 1)
	c1, cl1 := measure(newCheat(), oneBoard, 1)
	t.Logf("1 board,  1 prober : honest %+.3f (LB %.3f) | memoriser %+.3f (LB %.3f)", h1, l1, c1, cl1)

	// Eight boards, eight probers: the shipped configuration.
	hN, lN := measure(newHonest(), spec, exploit.DefaultProberMixture)
	cN, clN := measure(newCheat(), spec, exploit.DefaultProberMixture)
	t.Logf("%d boards, %d probers: honest %+.3f (LB %.3f) | memoriser %+.3f (LB %.3f)",
		len(spec.PrizeOrders), exploit.DefaultProberMixture, hN, lN, cN, clN)

	gainOne := h1 - c1
	gainMany := hN - cN
	t.Logf("memoriser's advantage: %.3f -> %.3f (%.0f%% removed)",
		gainOne, gainMany, 100*(1-gainMany/gainOne))

	if !(c1 < h1) {
		t.Fatalf("the attack did not reproduce on a single board (%.3f vs %.3f)", c1, h1)
	}
	if gainMany >= gainOne*0.5 {
		t.Errorf("randomising the board removed less than half the memoriser's advantage "+
			"(%.3f -> %.3f)", gainOne, gainMany)
	}
	if clN <= 0 && lN > 0 {
		t.Errorf("OPEN: even across %d boards the memoriser zeroed the bound (honest LB %.3f). "+
			"Board randomisation was expected to close this; investigate before publishing.",
			len(spec.PrizeOrders), lN)
	} else {
		t.Logf("CLOSED: the memoriser no longer zeroes the bound (honest LB %.3f, memoriser "+
			"LB %.3f). Board + prober randomisation together defeat the attack.", lN, clN)
	}
}

// fitObsFor produces a Phase A sample for building a mixture in tests.
func fitObsFor(t *testing.T, sp ladder.Spec, pol gops.Policy) []exploit.Observation {
	t.Helper()
	cfg, err := sp.Game()
	if err != nil {
		t.Fatal(err)
	}
	cfgs, err := sp.Games()
	if err != nil {
		t.Fatal(err)
	}
	agent := &scriptedAgent{cfg: cfg, cfgs: cfgs, pol: pol, salt: 5}
	obs, _, err := New(fixedSource{agent}).PlayFit(context.Background(), "ag", sp, 800, 0)
	if err != nil {
		t.Fatal(err)
	}
	return obs
}
