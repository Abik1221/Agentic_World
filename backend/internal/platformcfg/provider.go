package platformcfg

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/agent-arena/arena/internal/platformsign"
)

// Source is the transport the provider pulls snapshots from and listens on for
// change signals. Implemented in internal/store over Redis; the signal follows
// the store/notifier.go idiom (pure wake-up, re-read the authoritative key).
type Source interface {
	// LoadSnapshot returns the raw JSON snapshot bytes and its detached Ed25519
	// signature, or (nil, "", nil) when none has been published yet. A transport
	// error is returned as-is.
	LoadSnapshot(ctx context.Context) (data []byte, sig string, err error)
	// SubscribeChanges returns a channel that fires once per change signal and a
	// cancel func that releases the subscription. Sends are coalescing.
	SubscribeChanges(ctx context.Context) (<-chan struct{}, func())
}

// Provider holds the atomically-swappable current snapshot and keeps it fresh.
// Get is safe for concurrent use and never returns nil.
type Provider struct {
	src      Source
	defaults *Snapshot
	verifier *platformsign.Verifier // Admin's public key; nil => verification disabled
	log      *slog.Logger
	interval time.Duration // periodic re-pull backstop (missed-signal / Redis recovery)
	cur      atomic.Pointer[Snapshot]
}

// New builds a provider seeded with defaults so Get works before Run starts (and
// forever, if a snapshot is never published). interval<=0 defaults to 60s. src
// may be nil in tests/degraded builds — the provider then just serves defaults.
// verifier may be nil to disable signature checks (dev/local only).
func New(src Source, defaults *Snapshot, verifier *platformsign.Verifier, log *slog.Logger, interval time.Duration) *Provider {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	p := &Provider{src: src, defaults: defaults, verifier: verifier, log: log, interval: interval}
	p.cur.Store(defaults)
	return p
}

// Get returns the current snapshot. Never nil; callers read fields directly.
func (p *Provider) Get() *Snapshot { return p.cur.Load() }

// Run performs an initial load, then refreshes on every change signal and on a
// periodic backstop ticker until ctx is cancelled. It never returns an error:
// a failed load simply leaves the last-known-good (or default) snapshot in place.
func (p *Provider) Run(ctx context.Context) {
	if p.src == nil {
		p.log.Warn("platformcfg: no source configured; serving env defaults only")
		<-ctx.Done()
		return
	}

	p.refresh(ctx) // best-effort initial load

	ch, cancel := p.src.SubscribeChanges(ctx)
	defer cancel()

	t := time.NewTicker(p.interval)
	defer t.Stop()

	p.log.Info("platformcfg provider started", "refresh_interval", p.interval.String())
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ch:
			if !ok {
				// Subscription dropped (e.g. Redis blip); the ticker keeps us
				// eventually-consistent and re-establishing costs nothing here.
				continue
			}
			p.refresh(ctx)
		case <-t.C:
			p.refresh(ctx)
		}
	}
}

// refresh pulls and adopts a snapshot. On any error or empty result it keeps the
// current one — config is never allowed to regress to broken/empty at runtime.
func (p *Provider) refresh(ctx context.Context) {
	loadCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	raw, sig, err := p.src.LoadSnapshot(loadCtx)
	if err != nil {
		p.log.Warn("platformcfg: snapshot load failed; keeping last-known-good", "error", err)
		return
	}
	if len(raw) == 0 {
		return // nothing published yet — stay on defaults
	}
	// Reject config that isn't signed by the Super Admin's key. Config drives
	// money/fees/rewards, so an unverifiable snapshot must never be adopted — we
	// keep the last-known-good instead.
	if !p.verifier.Verify(raw, sig) {
		p.log.Error("platformcfg: snapshot signature invalid; REJECTED (keeping last-known-good)")
		return
	}

	snap, err := p.parse(raw)
	if err != nil {
		p.log.Warn("platformcfg: snapshot parse failed; keeping last-known-good", "error", err)
		return
	}
	prev := p.cur.Load()
	if prev != nil && snap.Version < prev.Version {
		// A late/duplicated delivery of an older snapshot — ignore it.
		return
	}
	p.cur.Store(snap)
	if prev == nil || snap.Version != prev.Version {
		p.log.Info("platformcfg: adopted config snapshot", "version", snap.Version,
			"ranked_allowed", snap.RankedAllowed())
	}
}

// parse unmarshals raw JSON over a copy of the defaults, so any field absent from
// the wire keeps its default value (see the package doc). The defaults struct is
// never mutated.
func (p *Provider) parse(raw []byte) (*Snapshot, error) {
	s := *p.defaults
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
