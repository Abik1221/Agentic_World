package attest

import (
	"context"
	"math/rand"
	"testing"

	"github.com/agent-arena/arena/internal/exploit"
	"github.com/agent-arena/arena/internal/gops"
	"github.com/agent-arena/arena/internal/ladder"
	"github.com/agent-arena/arena/internal/platformsign"
)

// buildBundle runs a small certification and packages it, so every test works on a bundle
// that a real run could have produced rather than a hand-written fixture.
func buildBundle(t *testing.T, runID int64, prev string) Bundle {
	t.Helper()
	spec := ladder.DefaultSpec()
	spec.PhaseAMatches = 240
	spec.MaxPhaseB = 128
	spec.ProberMixture = 4

	cfgs, err := spec.Games()
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(runID))
	pol := skewed(cfgs[0], runID+900)

	var obs []exploit.Observation
	for i := 0; i < spec.PhaseAMatches; i++ {
		ord := exploit.OrderForMatch(i, len(cfgs))
		_, o := simMatch(cfgs[ord], gops.Uniform(), pol, rng)
		for _, x := range o {
			x.Order = ord
			obs = append(obs, x)
		}
	}
	counts := exploit.TallyOrdered(obs)

	mix, err := exploit.BootstrapMultiProbers(cfgs, counts, spec.Alpha, spec.ProberMixture,
		proberSeed(runID, spec.Version))
	if err != nil {
		t.Fatal(err)
	}
	var payoffs []float64
	for i := 0; i < spec.MaxPhaseB; i++ {
		ord := exploit.OrderForMatch(i, len(cfgs))
		p := gops.PureFrom(cfgs[ord], mix.Select(ord, []byte{byte(i), byte(i >> 8)}))
		v, _ := simMatch(cfgs[ord], p, pol, rng)
		payoffs = append(payoffs, v)
	}

	lower, err := exploit.SequentialCertify(cfgs[0], payoffs, spec.Delta/2, spec.FirstCheckpoint)
	if err != nil {
		t.Fatal(err)
	}
	upper, err := exploit.MultiUpperBound(cfgs, counts, spec.Delta/2)
	if err != nil {
		t.Fatal(err)
	}
	specHash, err := spec.Hash()
	if err != nil {
		t.Fatal(err)
	}
	cert := ladder.Certificate{
		RunID: runID, AgentID: "ag", SpecVersion: spec.Version, SpecHash: specHash,
		ProberDigest: mix.Digest(cfgs, ladder.ProberDigest),
		Games:        lower.Games, MeanPayoff: lower.MeanPayoff, StdDev: lower.StdDev,
		LowerBound: lower.LowerBound, UpperBound: upper,
		Separable: (upper - lower.LowerBound) <= exploit.PayoffRange(cfgs[0])/3,
		Delta:     spec.Delta, Informative: lower.Informative,
		StopReason: "budget_exhausted", FitMatches: spec.PhaseAMatches, KeptFraction: 1,
	}
	return Bundle{
		Version: BundleVersion, Spec: spec, SpecHash: specHash, FitCounts: counts,
		Payoffs: payoffs, Certificate: cert, PrevHash: prev, IssuedAt: "2026-08-20T00:00:00Z",
	}
}

// TestAStrangerCanRecomputeTheNumbers is the point of the package: verification is
// recomputation, not trust.
func TestAStrangerCanRecomputeTheNumbers(t *testing.T) {
	b := buildBundle(t, 1, "")
	if err := b.Verify(); err != nil {
		t.Fatalf("an honest bundle failed verification: %v", err)
	}
	got, err := b.Recompute()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("recomputed interval [%.4f, %.4f] over %d games, %d fit matches",
		got.LowerBound, got.UpperBound, got.Games, b.Certificate.FitMatches)
	if got.UpperBound < got.LowerBound {
		t.Fatalf("recomputed an inverted interval: [%.4f, %.4f]", got.LowerBound, got.UpperBound)
	}
}

// TestTamperingIsCaught. Every input a publisher could quietly edit to flatter a result.
func TestTamperingIsCaught(t *testing.T) {
	cases := map[string]func(*Bundle){
		"headline lower bound rewritten": func(b *Bundle) { b.Certificate.LowerBound -= 1.5 },
		"upper bound rewritten":          func(b *Bundle) { b.Certificate.UpperBound -= 2.0 },
		"sample size inflated":           func(b *Bundle) { b.Certificate.Games += 64 },
		"std dev massaged":               func(b *Bundle) { b.Certificate.StdDev *= 0.5 },
		"a payoff dropped":               func(b *Bundle) { b.Payoffs = b.Payoffs[:len(b.Payoffs)-1] },
		"a payoff added":                 func(b *Bundle) { b.Payoffs = append(b.Payoffs, 12.0) },
		"a payoff value edited":          func(b *Bundle) { b.Payoffs[3] += 5 },
		"a fit count edited":             func(b *Bundle) { b.FitCounts[0].N += 500 },
		"spec hash swapped":              func(b *Bundle) { b.SpecHash = "0000" },
		"spec params edited":             func(b *Bundle) { b.Spec.Alpha = 0.9 },
		"prober digest faked":            func(b *Bundle) { b.Certificate.ProberDigest = "deadbeef" },
		"unknown version":                func(b *Bundle) { b.Version = 99 },
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			b := buildBundle(t, 2, "")
			if err := b.Verify(); err != nil {
				t.Fatalf("baseline bundle was already invalid: %v", err)
			}
			tamper(&b)
			if err := b.Verify(); err == nil {
				t.Fatal("tampering was not detected")
			}
		})
	}
}

// TestReorderingPayoffsIsHarmlessAndThereforeUndetectable records a property I initially got
// wrong, because the distinction matters for what the bundle claims to protect.
//
// An earlier version of TestTamperingIsCaught expected a reordered payoff list to fail
// verification, on the reasoning that "the sequential bound is read at a prefix, so order is
// load-bearing". Order IS load-bearing while a run is DECIDING where to stop. It is not
// load-bearing afterwards: the bundle publishes only the payoffs the certificate actually
// used, and mean, variance and therefore both bounds are symmetric functions of that set.
//
// So a permutation changes nothing a verifier could object to — and, more importantly, buys
// an adversary nothing, because the statistic it would be trying to move is invariant. The
// attacks that DO matter are changing the multiset (dropping, adding, editing a value), and
// those are caught, as TestTamperingIsCaught shows.
//
// Recorded rather than deleted so nobody re-adds the expectation and "fixes" the bundle to
// detect a non-attack.
func TestReorderingPayoffsIsHarmlessAndThereforeUndetectable(t *testing.T) {
	b := buildBundle(t, 9, "")
	if err := b.Verify(); err != nil {
		t.Fatal(err)
	}
	before, err := b.Recompute()
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(4))
	rng.Shuffle(len(b.Payoffs), func(i, j int) { b.Payoffs[i], b.Payoffs[j] = b.Payoffs[j], b.Payoffs[i] })
	after, err := b.Recompute()
	if err != nil {
		t.Fatal(err)
	}
	if after.LowerBound != before.LowerBound || after.MeanPayoff != before.MeanPayoff {
		t.Fatalf("a permutation changed the certificate (LB %.10f -> %.10f); if the statistic "+
			"is not order-invariant, order IS load-bearing and the bundle must detect a "+
			"reorder", before.LowerBound, after.LowerBound)
	}
	// Changing the multiset, however, must be caught.
	b.Payoffs[0] += 7
	if err := b.Verify(); err == nil {
		t.Fatal("editing a payoff VALUE was not detected")
	}
}

// TestSignatureAndArithmeticAreBothChecked. A valid signature over wrong arithmetic is an
// AUTHENTICATED false claim — worse than an unsigned true one.
func TestSignatureAndArithmeticAreBothChecked(t *testing.T) {
	seed, pub, err := platformsign.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := platformsign.NewSigner(seed)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := platformsign.NewVerifier(pub)
	if err != nil {
		t.Fatal(err)
	}

	b := buildBundle(t, 3, "")
	sg, err := Sign(b, signer, pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySigned(sg, verifier); err != nil {
		t.Fatalf("an honest signed bundle failed: %v", err)
	}

	// Tamper AFTER signing: the signature must fail.
	bad := sg
	bad.Bundle.Certificate.LowerBound -= 1
	if err := VerifySigned(bad, verifier); err == nil {
		t.Fatal("a post-signature edit was accepted")
	}

	// Re-sign the doctored bundle with OUR OWN key: the signature is now valid, so only
	// recomputation can catch it. This is the case a signature-only verifier would pass.
	resigned, err := Sign(bad.Bundle, signer, pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySigned(resigned, verifier); err == nil {
		t.Fatal("a validly-signed but arithmetically false bundle was accepted; " +
			"verification is not recomputing")
	}

	// A different key must not verify.
	_, otherPub, err := platformsign.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	otherV, err := platformsign.NewVerifier(otherPub)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySigned(sg, otherV); err == nil {
		t.Fatal("a bundle verified under the wrong public key")
	}
}

// TestUnsignedBundlesAreRefused. A certificate nobody can attribute is not evidence.
func TestUnsignedBundlesAreRefused(t *testing.T) {
	b := buildBundle(t, 4, "")
	if _, err := Sign(b, nil, ""); err == nil {
		t.Fatal("an unsigned bundle was issued")
	}
}

// TestChainDetectsSilentRevision. This is what the platform's event log lacks: per-message
// signatures with no prev-hash mean deleting or altering a message leaves every remaining
// signature valid.
func TestChainDetectsSilentRevision(t *testing.T) {
	seed, pub, _ := platformsign.GenerateKeypair()
	signer, _ := platformsign.NewSigner(seed)
	verifier, _ := platformsign.NewVerifier(pub)

	var series []Signed
	prev := ""
	for i := int64(1); i <= 3; i++ {
		b := buildBundle(t, i, prev)
		sg, err := Sign(b, signer, pub)
		if err != nil {
			t.Fatal(err)
		}
		series = append(series, sg)
		if prev, err = b.Hash(); err != nil {
			t.Fatal(err)
		}
	}
	if err := VerifyChain(series, verifier); err != nil {
		t.Fatalf("an honest chain failed: %v", err)
	}

	// Remove the middle bundle and re-sign nothing: the link must break.
	dropped := []Signed{series[0], series[2]}
	if err := VerifyChain(dropped, verifier); err == nil {
		t.Fatal("dropping a published certificate from the series was not detected")
	}

	// Reorder: also breaks.
	swapped := []Signed{series[1], series[0], series[2]}
	if err := VerifyChain(swapped, verifier); err == nil {
		t.Fatal("reordering the series was not detected")
	}
}

// TestCanonicalBytesAreStable. The hash is the chain link, so it must not depend on map
// iteration or on the order rows came back from SQL.
func TestCanonicalBytesAreStable(t *testing.T) {
	b := buildBundle(t, 5, "")
	h0, err := b.Hash()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		h, err := b.Hash()
		if err != nil {
			t.Fatal(err)
		}
		if h != h0 {
			t.Fatalf("hash is not stable: %s vs %s", h, h0)
		}
	}
	// Shuffling the counts must NOT change the hash — they are evidence, not a sequence.
	shuffled := buildBundle(t, 5, "")
	rng := rand.New(rand.NewSource(1))
	rng.Shuffle(len(shuffled.FitCounts), func(i, j int) {
		shuffled.FitCounts[i], shuffled.FitCounts[j] = shuffled.FitCounts[j], shuffled.FitCounts[i]
	})
	h2, err := shuffled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if h2 != h0 {
		t.Fatal("reordering the fit counts changed the hash; canonicalisation is not working")
	}
}

// TestSpoofCostIsControlledByTheVerifier. The defensible claim against the fingerprint-
// spoofing attack: faking a result requires a computed strategy object whose size WE choose,
// while detection cost grows only with sample size.
func TestSpoofCostIsControlledByTheVerifier(t *testing.T) {
	small := ladder.DefaultSpec()
	small.PrizeOrders = small.PrizeOrders[:1]
	big := ladder.DefaultSpec()

	sc, err := Spoof(small)
	if err != nil {
		t.Fatal(err)
	}
	bc, err := Spoof(big)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("1 board : %d infosets -> %d strategy entries to fake", sc.InfosetsPerBoard, sc.Entries)
	t.Logf("%d boards: %d infosets -> %d strategy entries to fake",
		bc.Boards, bc.InfosetsPerBoard, bc.Entries)

	if bc.Entries <= sc.Entries {
		t.Fatalf("adding boards did not raise the spoofing cost (%d -> %d)", sc.Entries, bc.Entries)
	}
	if !bc.Adaptive {
		t.Fatal("the prober is adaptive; the cost floor must say so")
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────────────────

func simMatch(cfg gops.Config, a, b gops.Policy, rng *rand.Rand) (float64, []exploit.Observation) {
	n := gops.Node{Me: cfg.FullHand(), Opp: cfg.FullHand()}
	total := 0.0
	var obs []exploit.Observation
	pick := func(mix []float64) int {
		u, acc := rng.Float64(), 0.0
		for i, p := range mix {
			if acc += p; u <= acc {
				return i + 1
			}
		}
		for i := len(mix) - 1; i >= 0; i-- {
			if mix[i] > 0 {
				return i + 1
			}
		}
		return 0
	}
	for n.Me != 0 {
		pot := cfg.Pot(n)
		bn := gops.Node{Me: n.Opp, Opp: n.Me, Carry: n.Carry}
		ca, cb := pick(a.Step(cfg, n)), pick(b.Step(cfg, bn))
		obs = append(obs, exploit.Observation{MatchID: "x", Node: bn, Card: cb})
		carry := 0
		switch {
		case ca > cb:
			total += float64(pot)
		case ca < cb:
			total -= float64(pot)
		default:
			carry = pot
		}
		n = gops.Node{Me: n.Me &^ (1 << (ca - 1)), Opp: n.Opp &^ (1 << (cb - 1)), Carry: carry}
	}
	return total, obs
}

func skewed(cfg gops.Config, seed int64) gops.Policy {
	rng := rand.New(rand.NewSource(seed))
	p := make(gops.Policy)
	var walk func(n gops.Node)
	seen := map[gops.Node]bool{}
	walk = func(n gops.Node) {
		if n.Me == 0 || seen[n] {
			return
		}
		seen[n] = true
		w := make([]float64, cfg.N)
		for c := 1; c <= cfg.N; c++ {
			if n.Me&(1<<(c-1)) != 0 {
				w[c-1] = rng.Float64()*rng.Float64() + 0.01
			}
		}
		p[n] = w
		pot := cfg.Pot(n)
		for a := 1; a <= cfg.N; a++ {
			if n.Me&(1<<(a-1)) == 0 {
				continue
			}
			for b := 1; b <= cfg.N; b++ {
				if n.Opp&(1<<(b-1)) == 0 {
					continue
				}
				carry := 0
				if a == b {
					carry = pot
				}
				walk(gops.Node{Me: n.Me &^ (1 << (a - 1)), Opp: n.Opp &^ (1 << (b - 1)), Carry: carry})
			}
		}
	}
	root := gops.Node{Me: cfg.FullHand(), Opp: cfg.FullHand()}
	walk(root)
	for n := range seen {
		m := gops.Node{Me: n.Opp, Opp: n.Me, Carry: n.Carry}
		if _, ok := p[m]; !ok && m.Me != 0 {
			w := make([]float64, cfg.N)
			for c := 1; c <= cfg.N; c++ {
				if m.Me&(1<<(c-1)) != 0 {
					w[c-1] = rng.Float64()*rng.Float64() + 0.01
				}
			}
			p[m] = w
		}
	}
	return p
}

var _ = context.Background
