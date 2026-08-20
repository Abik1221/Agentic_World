package labdriver

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/exploit"
	"github.com/agent-arena/arena/internal/gops"
	"github.com/agent-arena/arena/internal/ladder"
	"github.com/agent-arena/arena/internal/remoteplay"
)

// scriptedAgent stands in for a model: a fixed, skewed, reproducible policy so its true
// exploitability can be computed exactly and compared against what the pipeline certifies.
// scriptedAgent stands in for a model. It must be BOARD-AWARE: a run cycles prize orders, and
// an agent that reconstructs the node against the wrong board silently falls back to its
// lowest card. That is not a hypothetical — it made an earlier version of the end-to-end test
// look like a coverage violation, when in fact the prober was simply facing a much weaker
// policy than the one the ground truth had been computed for.
// STATELESS by construction, per the AgentSource contract. An earlier version shared one
// *rand.Rand across matches; the race detector caught it, and the deeper problem was that a
// shared RNG makes a parallel run draw in a different order than a sequential one, so the two
// measure different things. Sampling from a hash of the POSITION instead is thread-safe,
// order-independent, and a better model of a real agent — which also decides from what it is
// shown rather than from how many matches it has played.
type scriptedAgent struct {
	cfg  gops.Config   // the board the policy was built for
	cfgs []gops.Config // every board this run may deal; nil means single-board
	pol  gops.Policy
	salt int64
}

// boardFor finds the config whose prize order matches the view, so the node is reconstructed
// against the game actually being played.
func (a *scriptedAgent) boardFor(v remoteplay.GoofspielView) (gops.Config, bool) {
	for _, c := range a.cfgs {
		if _, ok := nodeFromView(c, v); ok {
			return c, true
		}
	}
	return a.cfg, false
}

func (a *scriptedAgent) Decide(_ context.Context, v remoteplay.GoofspielView) (int, error) {
	cfg := a.cfg
	if len(a.cfgs) > 0 {
		if c, ok := a.boardFor(v); ok {
			cfg = c
		}
	}
	node, ok := nodeFromView(cfg, v)
	if !ok {
		return lowest(v.LegalActions), nil
	}
	mix := a.pol.Step(cfg, node)
	u, acc := positionRand(a.salt, v.MatchID, node, cfg.Order), 0.0
	for i, p := range mix {
		if acc += p; u <= acc {
			return i + 1, nil
		}
	}
	return lowest(v.LegalActions), nil
}

type fixedSource struct{ d remoteplay.Decider }

func (f fixedSource) Decider(context.Context, string, ladder.Spec) (remoteplay.Decider, error) {
	return f.d, nil
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
				w[c-1] = math.Pow(rng.Float64(), 3) + 0.01
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
					w[c-1] = math.Pow(rng.Float64(), 3) + 0.01
				}
			}
			p[m] = w
		}
	}
	return p
}

// TestViewShapeMatchesTheBridge. The driver builds the seat view and the estimator parses it;
// if the two ever disagree about a field name, every decision silently becomes unusable and
// the census reports a malformed view rather than a bug.
func TestViewShapeMatchesTheBridge(t *testing.T) {
	spec := ladder.DefaultSpec()
	gcfg, err := spec.Game()
	if err != nil {
		t.Fatal(err)
	}
	ecfg, err := engineConfigFor(spec, gcfg.Order)
	if err != nil {
		t.Fatal(err)
	}
	agent := &scriptedAgent{cfg: gcfg, pol: skewed(gcfg, 1), salt: 1}
	r, err := play(context.Background(), ecfg, gcfg, "m", agent, remoteplay.NearestPool{},
		matchSeed("ag", "fit", 0))
	if err != nil {
		t.Fatal(err)
	}
	if r.Census.Kept != spec.N {
		t.Fatalf("kept %d of %d decisions; dropped: %v", r.Census.Kept, spec.N, r.Census.Dropped)
	}
	if len(r.Census.Dropped) != 0 {
		t.Fatalf("the driver produced views its own bridge rejects: %v", r.Census.Dropped)
	}
}

// TestDriverAgreesWithTheSolverExactly pins the payoff sign AND the driver's fidelity to the
// solver's model of the game, in one deterministic comparison.
//
// An earlier version of this test asserted that a seat always playing its LOWEST card must
// lose. That is false, and the engine said so: under an ASCENDING open prize order, playing
// low early saves high cards for the high prizes, while "always highest" squanders them on
// prize 1. Hand-computed, always-lowest beats always-highest 12-3. The premise was wrong, not
// the code — recorded here because an intuition about a game is not a specification of it.
//
// The right ground truth is the solver. Against a PURE agent strategy the best response is
// exact and the whole match is deterministic, so the realised board must reproduce the solved
// value to the last point. If the tie rule, the carry, the prize order or the seat mapping
// differed by anything at all between engine and solver, this fails.
func TestDriverAgreesWithTheSolverExactly(t *testing.T) {
	spec := ladder.DefaultSpec()
	gcfg, err := spec.Game()
	if err != nil {
		t.Fatal(err)
	}
	ecfg, err := engineConfigFor(spec, gcfg.Order)
	if err != nil {
		t.Fatal(err)
	}

	// A pure agent: always the lowest card it holds.
	agentPol := make(gops.Policy)
	var walk func(n gops.Node)
	seen := map[gops.Node]bool{}
	walk = func(n gops.Node) {
		if n.Me == 0 || seen[n] {
			return
		}
		seen[n] = true
		w := make([]float64, gcfg.N)
		w[lowestSet(n.Me)-1] = 1
		agentPol[n] = w
		pot := gcfg.Pot(n)
		for a := 1; a <= gcfg.N; a++ {
			if n.Me&(1<<(a-1)) == 0 {
				continue
			}
			for b := 1; b <= gcfg.N; b++ {
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
	root := gops.Node{Me: gcfg.FullHand(), Opp: gcfg.FullHand()}
	walk(root)
	for n := range seen {
		m := gops.Node{Me: n.Opp, Opp: n.Me, Carry: n.Carry}
		if _, ok := agentPol[m]; !ok && m.Me != 0 {
			w := make([]float64, gcfg.N)
			w[lowestSet(m.Me)-1] = 1
			agentPol[m] = w
		}
	}

	solved, moves, err := gops.BestResponse(gcfg, agentPol)
	if err != nil {
		t.Fatal(err)
	}
	if solved <= 0 {
		t.Fatalf("best response to a pure strategy scored %v; it must extract value", solved)
	}

	agent := deciderFunc(func(v remoteplay.GoofspielView) int { return lowest(v.LegalActions) })
	r, err := play(context.Background(), ecfg, gcfg,
		"m", agent, proberDecider{cfg: gcfg, moves: moves}, matchSeed("ag", "x", 0))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(r.OppMinusAgent-solved) > 1e-9 {
		t.Fatalf("engine gave the prober %v, solver says %v — the driver and the solver do not "+
			"agree about this game", r.OppMinusAgent, solved)
	}
	t.Logf("prober extracted %.0f points, exactly matching the solved best-response value", solved)
}

// positionRand is a deterministic uniform draw in [0,1) from the MATCH and the position.
//
// The match id is what lets a stateless agent MIX. Seeding from the position alone makes the
// same node always produce the same card, i.e. a pure strategy — which is far more
// exploitable than the mixed policy it is meant to realise, and made an earlier version of
// TestFullCertificationOnTheRealEngine look like a coverage violation when the bound was
// fine and the double had changed underneath it.
func positionRand(salt int64, matchID string, n gops.Node, order []int) float64 {
	h := fnv1a(uint64(salt))
	for _, c := range []byte(matchID) {
		h = fnv1a(h ^ uint64(c))
	}
	h = fnv1a(h ^ uint64(n.Me))
	h = fnv1a(h ^ uint64(n.Opp))
	h = fnv1a(h ^ uint64(n.Carry))
	for _, p := range order {
		h = fnv1a(h ^ uint64(p))
	}
	return float64(h%(1<<53)) / float64(uint64(1)<<53)
}

func fnv1a(x uint64) uint64 {
	h := uint64(1469598103934665603)
	for i := 0; i < 8; i++ {
		h ^= (x >> (8 * i)) & 0xff
		h *= 1099511628211
	}
	return h
}

func lowestSet(mask uint16) int {
	for c := 1; c <= 16; c++ {
		if mask&(1<<(c-1)) != 0 {
			return c
		}
	}
	return 0
}

type deciderFunc func(remoteplay.GoofspielView) int

func (f deciderFunc) Decide(_ context.Context, v remoteplay.GoofspielView) (int, error) {
	return f(v), nil
}

// TestBatchesDoNotCollide is the regression guard for a bug found before this test existed:
// PlayCertify restarted its match ids at zero on every batch, AddPayoffs deduped them away,
// certify_done never grew, and the run would have paid for matches forever without advancing.
func TestBatchesDoNotCollide(t *testing.T) {
	spec := ladder.DefaultSpec()
	gcfg, _ := spec.Game()
	agent := &scriptedAgent{cfg: gcfg, pol: skewed(gcfg, 2), salt: 2}
	d := New(fixedSource{agent})
	cfgs, err := spec.Games()
	if err != nil {
		t.Fatal(err)
	}
	mix, err := exploit.BootstrapMultiProbers(cfgs, nil, DefaultAlphaForTest, 4, 1)
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	played := 0
	for _, batch := range []int{32, 32, 64} {
		out, err := d.PlayCertify(context.Background(), "ag", spec, mix, batch, played)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range out {
			if seen[p.MatchID] {
				t.Fatalf("match id %q reused across batches — payoffs would be deduped away", p.MatchID)
			}
			seen[p.MatchID] = true
		}
		played += len(out)
	}
	if len(seen) != 128 {
		t.Fatalf("%d unique match ids across 128 matches", len(seen))
	}

	// Fit batches must not replay the same board either.
	fitSeeds := map[string]bool{}
	for _, s := range []struct{ n, start int }{{30, 0}, {30, 30}} {
		obs, _, err := d.PlayFit(context.Background(), "ag", spec, s.n, s.start)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range obs {
			fitSeeds[o.MatchID] = true
		}
	}
	if len(fitSeeds) != 60 {
		t.Fatalf("%d unique fit match ids across 60 matches — a resumed fit replayed boards",
			len(fitSeeds))
	}
}

// TestDeterminism. Two identical runs must produce identical matches, or a certificate cannot
// be re-derived by anyone.
func TestDeterminism(t *testing.T) {
	spec := ladder.DefaultSpec()
	gcfg, _ := spec.Game()
	run := func() []exploit.Observation {
		agent := &scriptedAgent{cfg: gcfg, pol: skewed(gcfg, 3), salt: 9}
		obs, _, err := New(fixedSource{agent}).PlayFit(context.Background(), "ag", spec, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		return obs
	}
	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatalf("%d vs %d observations", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("observation %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
}

// TestFullCertificationOnTheRealEngine is the end of the pipeline.
//
// A scripted agent whose TRUE exploitability the solver computes exactly is driven through
// spec -> real Goofspiel engine -> fit -> solve -> pin -> probe -> sequential stop ->
// certificate, and the published bound is checked against the truth.
func TestFullCertificationOnTheRealEngine(t *testing.T) {
	spec := ladder.DefaultSpec()
	gcfg, err := spec.Game()
	if err != nil {
		t.Fatal(err)
	}
	cfgs, err := spec.Games()
	if err != nil {
		t.Fatal(err)
	}
	pol := skewed(gcfg, 21)
	// Ground truth is the MEAN over boards: the estimand is expected exploitability over the
	// order set, not exploitability on board 0. Comparing against one board was what made an
	// earlier version of this test read as a coverage violation.
	trueEps := 0.0
	for _, c := range cfgs {
		e, _, err := gops.BestResponse(c, pol)
		if err != nil {
			t.Fatal(err)
		}
		trueEps += e
	}
	trueEps /= float64(len(cfgs))
	agent := &scriptedAgent{cfg: gcfg, cfgs: cfgs, pol: pol, salt: 21}

	repo := newMemRepo(spec)
	svc := ladder.NewService(repo, New(fixedSource{agent}))

	var last ladder.Step
	for i := 0; i < 500; i++ {
		last, err = svc.Advance(context.Background(), "ag_real")
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if last.Done {
			break
		}
	}
	if last.Certificate == nil {
		t.Fatalf("no certificate produced: %+v", last)
	}
	c := *last.Certificate
	t.Logf("REAL-ENGINE CERTIFICATE: lower_bound=%.4f mean=%.4f games=%d fit=%d kept=%.3f "+
		"stop=%s | true exploitability %.4f", c.LowerBound, c.MeanPayoff, c.Games,
		c.FitMatches, c.KeptFraction, c.StopReason, trueEps)

	if c.LowerBound > trueEps+1e-9 {
		t.Errorf("published bound %.4f exceeds true exploitability %.4f", c.LowerBound, trueEps)
	}
	if c.KeptFraction < 0.999 {
		t.Errorf("kept fraction %.3f — the driver produced views the estimator could not use",
			c.KeptFraction)
	}
	if !c.Informative {
		t.Errorf("a skewed agent produced an uninformative certificate")
	}
}

// ── minimal in-memory repo (the SQL one is tested against Postgres separately) ──────────

type memRepo struct {
	spec    ladder.Spec
	run     ladder.Run
	counts  map[int]map[gops.Node]map[int]int
	payoffs map[string]ladder.MatchPayoff
	order   []string
}

func newMemRepo(s ladder.Spec) *memRepo {
	return &memRepo{spec: s, run: ladder.Run{ID: 1, AgentID: "ag_real", SpecVersion: s.Version,
		Phase: ladder.PhaseFit}, counts: map[int]map[gops.Node]map[int]int{},
		payoffs: map[string]ladder.MatchPayoff{}}
}

func (m *memRepo) ActiveSpec(context.Context) (ladder.Spec, error) { return m.spec, nil }
func (m *memRepo) OpenRun(context.Context, string, int) (ladder.Run, error) {
	return m.run, nil
}
func (m *memRepo) AddFitCounts(_ context.Context, _ int64, c []exploit.OrderedCount, played int, cs exploit.Census) error {
	for _, x := range c {
		if m.counts[x.Order] == nil {
			m.counts[x.Order] = map[gops.Node]map[int]int{}
		}
		if m.counts[x.Order][x.Node] == nil {
			m.counts[x.Order][x.Node] = map[int]int{}
		}
		m.counts[x.Order][x.Node][x.Card] += x.N
	}
	m.run.FitDone += played
	m.run.FitKept += cs.Kept
	m.run.FitOffered += cs.Total()
	return nil
}
func (m *memRepo) FitCounts(context.Context, int64) ([]exploit.OrderedCount, error) {
	var out []exploit.OrderedCount
	for ord, nodes := range m.counts {
		for n, cards := range nodes {
			for card, k := range cards {
				out = append(out, exploit.OrderedCount{Order: ord,
					Count: exploit.Count{Node: n, Card: card, N: k}})
			}
		}
	}
	return out, nil
}
func (m *memRepo) PinProber(_ context.Context, _ int64, d string) error {
	m.run.Phase, m.run.ProberDigest = ladder.PhaseCertify, d
	return nil
}
func (m *memRepo) AddPayoffs(_ context.Context, _ int64, p []ladder.MatchPayoff) error {
	for _, x := range p {
		if _, dup := m.payoffs[x.MatchID]; dup {
			continue
		}
		m.payoffs[x.MatchID] = x
		m.order = append(m.order, x.MatchID)
	}
	m.run.CertifyDone = len(m.payoffs)
	return nil
}
func (m *memRepo) Payoffs(context.Context, int64) ([]float64, error) {
	out := make([]float64, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.payoffs[id].Payoff)
	}
	return out, nil
}
func (m *memRepo) SaveCertificate(_ context.Context, c ladder.Certificate) error {
	m.run.Phase = ladder.PhaseDone
	return nil
}

var _ = json.Marshal

// DefaultAlphaForTest mirrors the spec's smoothing so test mixtures match production.
const DefaultAlphaForTest = exploit.DefaultAlpha

// TestConcurrentMatchesAreIdentical is the property the whole optimisation rests on.
//
// A certificate is worthless if it cannot be replayed, so running matches in parallel must not
// change WHAT is measured — only how long it takes. Two things make that true: every match's
// seed comes from (agent, phase, index) rather than execution order, and each result is
// written back at its own index so the returned order is play order, not finish order.
//
// If either breaks, the payoff sequence a certificate is computed from differs between a
// sequential and a parallel run, and two operators certifying the same model get different
// numbers. That is the failure this test exists to prevent.
func TestConcurrentMatchesAreIdentical(t *testing.T) {
	spec := ladder.DefaultSpec()
	gcfg, err := spec.Game()
	if err != nil {
		t.Fatal(err)
	}
	pol := skewed(gcfg, 4242)
	cfgs, err := spec.Games()
	if err != nil {
		t.Fatal(err)
	}
	mix, err := exploit.BootstrapMultiProbers(cfgs, nil, spec.Alpha, 4, 9)
	if err != nil {
		t.Fatal(err)
	}

	newAgent := func() remoteplay.Decider {
		return &scriptedAgent{cfg: gcfg, cfgs: cfgs, pol: pol, salt: 7}
	}

	seqFit, seqCensus, err := New(fixedSource{newAgent()}).
		PlayFit(context.Background(), "ag", spec, 24, 0)
	if err != nil {
		t.Fatal(err)
	}
	parFit, parCensus, err := NewPaced(fixedSource{newAgent()}, Pace{Workers: 8}).
		PlayFit(context.Background(), "ag", spec, 24, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(seqFit) != len(parFit) {
		t.Fatalf("fit observation count differs: %d sequential vs %d parallel", len(seqFit), len(parFit))
	}
	for i := range seqFit {
		if seqFit[i] != parFit[i] {
			t.Fatalf("fit observation %d differs: %+v vs %+v", i, seqFit[i], parFit[i])
		}
	}
	if seqCensus.Kept != parCensus.Kept || seqCensus.Total() != parCensus.Total() {
		t.Fatalf("census differs: %+v vs %+v", seqCensus, parCensus)
	}

	seqPay, err := New(fixedSource{newAgent()}).
		PlayCertify(context.Background(), "ag", spec, mix, 32, 0)
	if err != nil {
		t.Fatal(err)
	}
	parPay, err := NewPaced(fixedSource{newAgent()}, Pace{Workers: 8}).
		PlayCertify(context.Background(), "ag", spec, mix, 32, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(seqPay) != len(parPay) {
		t.Fatalf("payoff count differs: %d vs %d", len(seqPay), len(parPay))
	}
	for i := range seqPay {
		if seqPay[i] != parPay[i] {
			t.Fatalf("payoff %d differs: %+v vs %+v — play ORDER is not preserved, so a "+
				"parallel run would certify a different sample", i, seqPay[i], parPay[i])
		}
	}
}

// TestPacingSpacesStarts. Free tiers rate-limit hard — one probe returned 429 under no load
// at all — and every 429 becomes a fallback play the certificate reports as the model
// choosing badly. Pacing is what keeps "faster" from becoming "wrong".
func TestPacingSpacesStarts(t *testing.T) {
	const n, gap = 8, 20 * time.Millisecond
	start := time.Now()
	_, err := runMatches(context.Background(), Pace{Workers: 2, MinInterval: gap}, n,
		func(context.Context, int) (int, error) { return 0, nil })
	if err != nil {
		t.Fatal(err)
	}
	// n starts spaced by gap; the last start is at least (n-1)*gap after the first.
	if elapsed := time.Since(start); elapsed < time.Duration(n-1)*gap {
		t.Fatalf("ran %d matches in %v; pacing of %v between starts was not applied",
			n, elapsed, gap)
	}
}

// TestFirstErrorStopsTheRun. A provider outage must not burn the rest of the budget against a
// dead endpoint.
//
// NOTE ON A DEADLOCK I WROTE HERE FIRST. The original version had every non-failing match
// block on <-ctx.Done() and the failure at k==3. With Workers=2 the two goroutines holding
// the semaphore waited forever for a cancellation that only k==3 could trigger, and k==3
// could never acquire a slot. That is the standard worker-pool deadlock and it was the test's
// fault, not the pool's — production play() always returns, bounded by the HTTP client's own
// timeout. Failing FIRST and letting the rest do bounded work is both correct and closer to
// what an outage looks like.
func TestFirstErrorStopsTheRun(t *testing.T) {
	var mu sync.Mutex
	entered := 0
	_, err := runMatches(context.Background(), Pace{Workers: 2}, 500,
		func(ctx context.Context, k int) (int, error) {
			mu.Lock()
			entered++
			mu.Unlock()
			if k == 0 {
				return 0, errors.New("provider down")
			}
			time.Sleep(2 * time.Millisecond)
			return 0, nil
		})
	if err == nil {
		t.Fatal("a provider failure did not surface")
	}
	mu.Lock()
	ran := entered
	mu.Unlock()
	t.Logf("%d of 500 matches entered before the run stopped", ran)
	if ran >= 500 {
		t.Fatalf("all %d matches ran after a failure; the run did not stop early", ran)
	}
}
