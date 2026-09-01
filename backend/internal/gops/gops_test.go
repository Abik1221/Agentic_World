package gops

import (
	"math"
	"math/rand"
	"testing"
)

func cfgN(t *testing.T, n int) Config {
	t.Helper()
	order := make([]int, n)
	for i := range order {
		order[i] = i + 1
	}
	c := Config{N: n, Order: order, Tie: TieCarry}
	if err := c.Validate(); err != nil {
		t.Fatalf("cfgN(%d): %v", n, err)
	}
	return c
}

// randomPolicy builds a reproducible random behavioural strategy over every node the game
// can reach. Reproducible because a solver test that fails one run in twenty teaches nothing.
func randomPolicy(cfg Config, seed int64) Policy {
	rng := rand.New(rand.NewSource(seed))
	p := make(Policy)
	var walk func(n Node)
	seen := map[Node]bool{}
	walk = func(n Node) {
		if n.Me == 0 || seen[n] {
			return
		}
		seen[n] = true
		w := make([]float64, cfg.N)
		for card := 1; card <= cfg.N; card++ {
			if n.Me&(1<<(card-1)) != 0 {
				w[card-1] = rng.Float64() + 0.01
			}
		}
		p[n] = w
		pot := cfg.Pot(n)
		for mine := 1; mine <= cfg.N; mine++ {
			if n.Me&(1<<(mine-1)) == 0 {
				continue
			}
			for theirs := 1; theirs <= cfg.N; theirs++ {
				if n.Opp&(1<<(theirs-1)) == 0 {
					continue
				}
				_, carry := resolve(mine, theirs, pot)
				walk(Node{
					Me:    n.Me &^ (1 << (mine - 1)),
					Opp:   n.Opp &^ (1 << (theirs - 1)),
					Carry: carry,
				})
			}
		}
	}
	root := Node{Me: cfg.FullHand(), Opp: cfg.FullHand()}
	walk(root)
	// The mirrored nodes matter too: the opponent decides at Node{Me:Opp, Opp:Me}.
	for n := range seen {
		m := Node{Me: n.Opp, Opp: n.Me, Carry: n.Carry}
		if _, ok := p[m]; !ok && m.Me != 0 {
			w := make([]float64, cfg.N)
			for card := 1; card <= cfg.N; card++ {
				if m.Me&(1<<(card-1)) != 0 {
					w[card-1] = rng.Float64() + 0.01
				}
			}
			p[m] = w
		}
	}
	return p
}

// TestValueIsZeroBySymmetry is the load-bearing test of this package.
//
// The whole reason exploitability is computable here without a linear program is that the
// game is symmetric, so its value is exactly 0 and eps(sigma) = u(BR(sigma), sigma). If a
// future change to the tie rule, the prize permutation, or resolve() breaks the symmetry,
// every published exploitability number silently becomes wrong by an unknown constant.
//
// Any strategy played against ITSELF must have expected differential exactly 0: the joint
// distribution is exchangeable under swapping the seats and the payoff is antisymmetric.
func TestValueIsZeroBySymmetry(t *testing.T) {
	for n := 2; n <= 6; n++ {
		cfg := cfgN(t, n)
		for seed := int64(1); seed <= 3; seed++ {
			p := randomPolicy(cfg, seed)
			v, err := Evaluate(cfg, p, p)
			if err != nil {
				t.Fatalf("n=%d seed=%d: %v", n, seed, err)
			}
			if math.Abs(v) > 1e-9 {
				t.Errorf("n=%d seed=%d: self-play value = %v, want 0 (symmetry broken)", n, seed, v)
			}
		}
		if v, err := Evaluate(cfg, Uniform(), Uniform()); err != nil || math.Abs(v) > 1e-9 {
			t.Errorf("n=%d: uniform self-play = %v (err %v), want 0", n, v, err)
		}
	}
}

// TestBestResponseIsOptimal checks the DP against the definition: no other strategy may score
// more against the same opponent. Random strategies are a weak witness individually, but a
// best response that is wrong at ANY reachable node will lose to at least one of them.
func TestBestResponseIsOptimal(t *testing.T) {
	for n := 2; n <= 5; n++ {
		cfg := cfgN(t, n)
		for oppSeed := int64(1); oppSeed <= 3; oppSeed++ {
			opp := randomPolicy(cfg, oppSeed)
			best, moves, err := BestResponse(cfg, opp)
			if err != nil {
				t.Fatalf("n=%d: %v", n, err)
			}
			// The returned pure strategy must actually achieve the returned value.
			got, err := Evaluate(cfg, PureFrom(cfg, moves), opp)
			if err != nil {
				t.Fatalf("n=%d: %v", n, err)
			}
			if math.Abs(got-best) > 1e-9 {
				t.Errorf("n=%d opp=%d: BR value %v but playing it scores %v", n, oppSeed, best, got)
			}
			// Nothing may beat it.
			for chalSeed := int64(100); chalSeed < 130; chalSeed++ {
				chal := randomPolicy(cfg, chalSeed)
				v, err := Evaluate(cfg, chal, opp)
				if err != nil {
					t.Fatalf("n=%d: %v", n, err)
				}
				if v > best+1e-9 {
					t.Errorf("n=%d opp=%d: challenger %d scored %v > BR %v", n, oppSeed, chalSeed, v, best)
				}
			}
		}
	}
}

// TestExploitabilityOfUniformIsPositive. A uniform-random opponent must be exploitable; a
// solver reporting ~0 here is not solving anything.
func TestExploitabilityOfUniformIsPositive(t *testing.T) {
	for n := 3; n <= 6; n++ {
		cfg := cfgN(t, n)
		eps, _, err := BestResponse(cfg, Uniform())
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if eps <= 0 {
			t.Errorf("n=%d: exploitability of uniform = %v, want > 0", n, eps)
		}
	}
}

// TestBestResponseIsDeterministic. A prober whose behaviour changes between runs cannot
// certify anything, and Go map iteration order is deliberately random.
func TestBestResponseIsDeterministic(t *testing.T) {
	cfg := cfgN(t, 5)
	opp := randomPolicy(cfg, 7)
	v0, m0, err := BestResponse(cfg, opp)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		v, m, err := BestResponse(cfg, opp)
		if err != nil {
			t.Fatal(err)
		}
		if v != v0 {
			t.Fatalf("run %d: value %v != %v", i, v, v0)
		}
		if len(m) != len(m0) {
			t.Fatalf("run %d: %d nodes != %d", i, len(m), len(m0))
		}
		for n, card := range m0 {
			if m[n] != card {
				t.Fatalf("run %d: node %+v gave %d, want %d", i, n, m[n], card)
			}
		}
	}
}

// TestBestResponseNeverPlaysAnIllegalCard. A prober that plays a card it does not hold would
// be rejected by the engine mid-certification, turning a measurement into a forfeit.
func TestBestResponseNeverPlaysAnIllegalCard(t *testing.T) {
	cfg := cfgN(t, 5)
	_, moves, err := BestResponse(cfg, randomPolicy(cfg, 11))
	if err != nil {
		t.Fatal(err)
	}
	for n, card := range moves {
		if card < 1 || card > cfg.N {
			t.Fatalf("node %+v: card %d out of range", n, card)
		}
		if n.Me&(1<<(card-1)) == 0 {
			t.Fatalf("node %+v: card %d not in hand %b", n, card, n.Me)
		}
	}
}

// TestStepIgnoresMassOnCardsNotHeld. A policy estimated from a dirty log can carry a count
// for a card the seat did not hold; letting that mass through would drag a best response
// toward an action the opponent could never have taken.
func TestStepIgnoresMassOnCardsNotHeld(t *testing.T) {
	cfg := cfgN(t, 4)
	n := Node{Me: 0b0011, Opp: 0b0011} // holds 1 and 2 only
	p := Policy{n: {5, 5, 999, 999}}   // huge mass on cards 3 and 4
	got := p.Step(cfg, n)
	if got[2] != 0 || got[3] != 0 {
		t.Fatalf("mass leaked onto unheld cards: %v", got)
	}
	if math.Abs(got[0]-0.5) > 1e-9 || math.Abs(got[1]-0.5) > 1e-9 {
		t.Fatalf("held cards not renormalised: %v", got)
	}
}

// TestUnseenNodeFallsBackToUniform. An opponent we never observed at a node is one we cannot
// claim to exploit there. Inventing an exploit would inflate every certified number.
func TestUnseenNodeFallsBackToUniform(t *testing.T) {
	cfg := cfgN(t, 4)
	n := Node{Me: 0b1111, Opp: 0b1111}
	got := Policy{}.Step(cfg, n)
	for card := 1; card <= 4; card++ {
		if math.Abs(got[card-1]-0.25) > 1e-9 {
			t.Fatalf("unseen node not uniform: %v", got)
		}
	}
}

func TestValidateRejectsBadSpecs(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"n too small", Config{N: 1, Order: []int{1}}},
		{"n too large", Config{N: MaxCards + 1, Order: make([]int, MaxCards+1)}},
		{"order wrong length", Config{N: 3, Order: []int{1, 2}}},
		{"order not a permutation", Config{N: 3, Order: []int{1, 1, 2}}},
		{"order out of range", Config{N: 3, Order: []int{1, 2, 9}}},
		{"unimplemented tie rule", Config{N: 3, Order: []int{1, 2, 3}, Tie: TieSplit}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

// TestCarryIsActuallyCarried pins the tie rule the certified ladder is defined against.
// With N=2 and order [1,2]: if both play card 1 in round 1 the pot (1) carries, so round 2
// is worth 1+2=3 and is decided by the remaining cards, which are equal again — so the pot
// is lost and the game is a 0-0 draw. A solver that dropped the carry would score the same
// here, so the discriminating check is the pot SIZE seen at round 2.
func TestCarryIsActuallyCarried(t *testing.T) {
	cfg := cfgN(t, 2)
	afterTie := Node{Me: 0b10, Opp: 0b10, Carry: 1}
	if got := cfg.Pot(afterTie); got != 3 {
		t.Fatalf("pot after a tied round = %d, want 3 (prize 2 + carry 1)", got)
	}
	if got := cfg.Round(afterTie); got != 1 {
		t.Fatalf("round = %d, want 1", got)
	}
}
