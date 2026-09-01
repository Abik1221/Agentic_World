package ladder

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/agent-arena/arena/internal/exploit"
	"github.com/agent-arena/arena/internal/gops"
)

// ── in-memory repo, matching the schema's idempotency contract ──────────────────────────

type memRepo struct {
	spec     Spec
	run      Run
	counts   map[int]map[gops.Node]map[int]int
	payoffs  map[string]MatchPayoff
	cert     *Certificate
	pinCalls int
}

func newMemRepo(s Spec) *memRepo {
	return &memRepo{spec: s, run: Run{ID: 1, AgentID: "ag", SpecVersion: s.Version, Phase: PhaseFit},
		counts: map[int]map[gops.Node]map[int]int{}, payoffs: map[string]MatchPayoff{}}
}

func (m *memRepo) ActiveSpec(context.Context) (Spec, error) { return m.spec, nil }
func (m *memRepo) OpenRun(context.Context, string, int) (Run, error) {
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
	m.pinCalls++
	m.run.Phase, m.run.ProberDigest = PhaseCertify, d
	return nil
}
func (m *memRepo) AddPayoffs(_ context.Context, _ int64, p []MatchPayoff) error {
	for _, x := range p {
		if _, dup := m.payoffs[x.MatchID]; dup {
			return fmt.Errorf("duplicate payoff for match %s", x.MatchID) // mirrors the PK
		}
		m.payoffs[x.MatchID] = x
	}
	m.run.CertifyDone = len(m.payoffs)
	return nil
}
func (m *memRepo) Payoffs(context.Context, int64) ([]float64, error) {
	out := make([]float64, len(m.payoffs))
	for _, p := range m.payoffs {
		out[p.Seq-1] = p.Payoff // seq is 1-based and unique, per the schema
	}
	return out, nil
}
func (m *memRepo) SaveCertificate(_ context.Context, c Certificate) error {
	m.cert = &c
	m.run.Phase = PhaseDone
	return nil
}

// ── a driver that plays the real game against a real agent policy ───────────────────────

type simDriver struct {
	cfg       gops.Config
	agent     gops.Policy
	rng       *rand.Rand
	seq       int
	fitPlayed int
	bPlayed   int
}

func (d *simDriver) PlayFit(_ context.Context, _ string, _ Spec, n, _ int) ([]exploit.Observation, exploit.Census, error) {
	var obs []exploit.Observation
	for i := 0; i < n; i++ {
		d.fitPlayed++
		_, o := simMatch(d.cfg, gops.Uniform(), d.agent, d.rng)
		obs = append(obs, o...)
	}
	return obs, exploit.Census{Kept: len(obs), Dropped: map[exploit.RejectReason]int{}}, nil
}

func (d *simDriver) PlayCertify(_ context.Context, _ string, _ Spec, prober exploit.MultiProber, n, _ int) ([]MatchPayoff, error) {
	p := gops.PureFrom(d.cfg, prober.Per[0].Members[0])
	out := make([]MatchPayoff, 0, n)
	for i := 0; i < n; i++ {
		d.bPlayed++
		d.seq++
		v, _ := simMatch(d.cfg, p, d.agent, d.rng)
		out = append(out, MatchPayoff{MatchID: fmt.Sprintf("m%d", d.seq), Seq: d.seq, Payoff: v})
	}
	return out, nil
}

// simMatch plays one match; returns A's differential and B's decisions at B's own nodes.
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

// TestEndToEndProducesAValidCertificate is the whole pipeline: spec, fit, solve, pin, probe,
// stop, publish — driven to completion against an agent whose TRUE exploitability is known
// exactly, and checked against it.
//
// This is the test that says the certified number means what the package claims it means.
func TestEndToEndProducesAValidCertificate(t *testing.T) {
	spec := DefaultSpec()
	cfg, err := spec.Game()
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(7))
	agent := skewedPolicy(cfg, 11)
	trueEps, _, err := gops.BestResponse(cfg, agent)
	if err != nil {
		t.Fatal(err)
	}

	repo := newMemRepo(spec)
	drv := &simDriver{cfg: cfg, agent: agent, rng: rng}
	svc := NewService(repo, drv)

	var last Step
	for i := 0; i < 200; i++ {
		last, err = svc.Advance(context.Background(), "ag")
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if last.Done {
			break
		}
	}
	if !last.Done || last.Certificate == nil {
		t.Fatalf("run did not finish: %+v", last)
	}
	c := *last.Certificate
	t.Logf("CERTIFICATE: lower_bound=%.4f mean=%.4f games=%d fit=%d stop=%s "+
		"(true exploitability %.4f) | phase A %d matches, phase B %d matches",
		c.LowerBound, c.MeanPayoff, c.Games, c.FitMatches, c.StopReason, trueEps,
		drv.fitPlayed, drv.bPlayed)

	if c.LowerBound > trueEps+1e-9 {
		t.Errorf("published bound %.4f exceeds true exploitability %.4f — the certificate is false",
			c.LowerBound, trueEps)
	}
	if !c.Informative {
		t.Errorf("a clearly exploitable agent produced an uninformative certificate")
	}
	wantHash, _ := spec.Hash()
	if c.SpecHash != wantHash || c.ProberDigest == "" {
		t.Errorf("certificate lacks its audit trail: %+v", c)
	}
	if c.FitMatches != spec.PhaseAMatches {
		t.Errorf("fit ran %d matches, spec says %d", c.FitMatches, spec.PhaseAMatches)
	}
	if math.Abs(c.KeptFraction-1) > 1e-9 {
		t.Errorf("kept fraction %.3f, want 1 for a clean simulated run", c.KeptFraction)
	}
	if repo.pinCalls != 1 {
		t.Errorf("prober pinned %d times, want exactly once", repo.pinCalls)
	}
	if drv.bPlayed > spec.MaxPhaseB {
		t.Errorf("played %d phase B matches, over the ceiling %d", drv.bPlayed, spec.MaxPhaseB)
	}
}

// TestNoPhaseBMatchesBeforeTheProberIsPinned. Playing the agent before the strategy is fixed
// would fold measurement into the fit and void the split the bound depends on.
func TestNoPhaseBMatchesBeforeTheProberIsPinned(t *testing.T) {
	spec := DefaultSpec()
	cfg, _ := spec.Game()
	repo := newMemRepo(spec)
	drv := &simDriver{cfg: cfg, agent: skewedPolicy(cfg, 3), rng: rand.New(rand.NewSource(1))}
	svc := NewService(repo, drv)

	for i := 0; i < 200; i++ {
		before := repo.run.ProberDigest
		st, err := svc.Advance(context.Background(), "ag")
		if err != nil {
			t.Fatal(err)
		}
		if before == "" && drv.bPlayed > 0 {
			t.Fatalf("step %d played %d phase B matches with no prober pinned", i, drv.bPlayed)
		}
		if st.Done {
			break
		}
	}
	if drv.bPlayed == 0 {
		t.Fatal("the run never reached phase B, so the guard proved nothing")
	}
}

// TestSolverDriftVoidsTheRun. Recomputing the prober and finding a different digest means the
// solver or the stored fit changed mid-run. Continuing would measure the agent against a
// strategy other than the one the certificate names.
func TestSolverDriftVoidsTheRun(t *testing.T) {
	spec := DefaultSpec()
	cfg, _ := spec.Game()
	repo := newMemRepo(spec)
	drv := &simDriver{cfg: cfg, agent: skewedPolicy(cfg, 5), rng: rand.New(rand.NewSource(2))}
	svc := NewService(repo, drv)

	for i := 0; i < 200; i++ {
		st, err := svc.Advance(context.Background(), "ag")
		if err != nil {
			t.Fatal(err)
		}
		if repo.run.ProberDigest != "" {
			break
		}
		if st.Done {
			t.Fatal("run finished before pinning a prober")
		}
	}
	repo.run.ProberDigest = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := svc.Advance(context.Background(), "ag"); err == nil {
		t.Fatal("a mismatched prober digest did not void the run")
	}
}

// skewedPolicy is an agent with real, findable weaknesses.
func skewedPolicy(cfg gops.Config, seed int64) gops.Policy {
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
