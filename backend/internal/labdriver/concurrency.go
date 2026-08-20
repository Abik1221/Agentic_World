package labdriver

// Bounded concurrency and per-provider rate limiting for match play.
//
// # Why this exists
//
// Measured on a real free-tier endpoint: 104 matches took 9m22s, about 5.4 seconds each,
// essentially all of it waiting on the model. Extrapolated to the shipped spec — 1,600 fit
// matches plus a 2,048 Phase B cap — that is roughly five and a half hours PER MODEL,
// sequential. Comparing four models is most of a day, which makes the benchmark unusable
// however good the mathematics is.
//
// Nothing about the work is inherently serial. Each match is a fresh game with a seed derived
// from its index, so matches within a batch are independent and can run at once.
//
// # Determinism survives, and that is not an accident
//
// A certificate is worthless if it cannot be replayed, so parallelism must not change what is
// measured. Two properties keep that true:
//
//   - every match's seed comes from (agent, phase, INDEX), never from execution order, so a
//     match plays identically whenever it runs;
//   - results are written back into a slice at their own index, so the returned order is the
//     play order regardless of which goroutine finished first.
//
// The payoff sequence a certificate is computed from is therefore byte-identical to the
// sequential run. TestConcurrentMatchesAreIdentical pins exactly that.
//
// # Why a rate limiter and not just a worker pool
//
// Free tiers rate-limit hard — a single probe of one model returned 429 with no load at all.
// Firing sixteen concurrent requests at such an endpoint converts a slow run into a failed
// one, and every 429 becomes a fallback play, which the certificate then reports as the model
// choosing badly. That is a measurement error dressed as a finding.
//
// So concurrency is bounded AND paced: at most N in flight, and at least a minimum interval
// between request starts. The pacing is what makes the difference between "faster" and
// "faster without lying about the model".

import (
	"context"
	"sync"
	"time"
)

// Pace bounds how hard the driver may hit a provider.
//
// Zero values mean "sequential and unpaced", which is the old behaviour exactly — so an
// operator who does not configure this gets what they had before rather than a surprise
// change in load against a paid endpoint.
type Pace struct {
	// Workers is the maximum matches in flight. 0 or 1 is sequential.
	Workers int
	// MinInterval is the minimum gap between STARTING two matches. Spacing starts rather
	// than requests is deliberate: a match makes several calls, so throttling starts is the
	// coarse control that a provider's per-minute quota actually cares about.
	MinInterval time.Duration
}

// DefaultPace is a conservative shape for a rate-limited free tier.
//
// Four workers rather than sixteen, and a 250ms floor between starts. Not tuned — measured
// only against one endpoint, and deliberately timid because the failure mode of being too
// aggressive is silent: 429s become fallback plays, and the certificate reports them as the
// model playing badly rather than as us overloading it.
func DefaultPace() Pace { return Pace{Workers: 4, MinInterval: 250 * time.Millisecond} }

// gate limits in-flight work and paces starts.
type gate struct {
	sem  chan struct{}
	mu   sync.Mutex
	next time.Time
	min  time.Duration
	now  func() time.Time
}

func newGate(p Pace) *gate {
	w := p.Workers
	if w < 1 {
		w = 1
	}
	return &gate{sem: make(chan struct{}, w), min: p.MinInterval, now: time.Now}
}

// enter blocks until this match may start, or the context is done.
func (g *gate) enter(ctx context.Context) error {
	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if g.min <= 0 {
		return nil
	}
	// Reserve the next start slot under the lock, then sleep OUTSIDE it, so pacing does not
	// serialise the workers on the mutex itself.
	g.mu.Lock()
	now := g.now()
	start := now
	if g.next.After(start) {
		start = g.next
	}
	g.next = start.Add(g.min)
	g.mu.Unlock()

	if wait := start.Sub(now); wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			<-g.sem
			return ctx.Err()
		}
	}
	return nil
}

func (g *gate) leave() { <-g.sem }

// runMatches plays n matches concurrently, writing each result at its own index.
//
// The first error wins and the context is cancelled, so a provider outage stops the run
// promptly instead of burning the rest of the budget against a dead endpoint. Results are
// returned even on error, so a caller can see how far it got.
func runMatches[T any](ctx context.Context, p Pace, n int, play func(ctx context.Context, k int) (T, error)) ([]T, error) {
	out := make([]T, n)
	if n == 0 {
		return out, nil
	}
	g := newGate(p)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg      sync.WaitGroup
		errOnce sync.Once
		firstEr error
	)
	for k := 0; k < n; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			if err := g.enter(ctx); err != nil {
				errOnce.Do(func() { firstEr = err; cancel() })
				return
			}
			defer g.leave()
			v, err := play(ctx, k)
			if err != nil {
				errOnce.Do(func() { firstEr = err; cancel() })
				return
			}
			out[k] = v // its OWN index: the returned order is play order, not finish order
		}(k)
	}
	wg.Wait()
	return out, firstEr
}
