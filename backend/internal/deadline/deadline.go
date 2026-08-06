// Package deadline computes how long the platform waits for one agent decision.
//
// # Why this is not a constant
//
// Every window on the platform was a hardcoded guess — Goofspiel 45s, Monopoly 60s, Mafia
// 30s at night. A single number has to satisfy two demands that pull apart:
//
//   - GENEROUS enough for a local llama.cpp model on consumer hardware, or a cloud
//     reasoning model with extended thinking. Both run 30–120s routinely.
//   - TIGHT enough that one dead agent does not stall a 12-seat Mafia table for the full
//     window on every single phase.
//
// No constant satisfies both. Pick high and a crashed seat taxes everyone; pick low and
// honest slow agents lose rounds they were in the middle of winning.
//
// # The two mechanisms
//
// ADAPTIVE BASE. The window is derived from what the agent has actually demonstrated:
// roughly p95 of its own recent decision latencies, with headroom, clamped to a floor and
// a ceiling. This is the standard treatment for adaptive RPC deadlines — the same shape
// as latency-aware load balancing (EWMA/p2c) and adaptive timeouts in mature RPC stacks.
// A fast agent gets a tight window and a slow one gets room, without an operator tuning
// per-agent numbers by hand.
//
// LIVENESS GATE. At the deadline the platform probes /health, which is free and involves
// no inference. Alive means it is genuinely still thinking, so it earns an extension.
// Gone means stop waiting NOW rather than burning the rest of the window on a process
// that will never answer. This is what breaks the tradeoff: we no longer have to guess
// which case we are in.
//
// # What this deliberately does NOT do
//
// It does not change who wins. An extension only ever buys an agent the chance to make
// its OWN move instead of having a deterministic fallback played for it — the board, the
// scoring and the settlement are untouched. Waiting longer must never buy a better result,
// only an authentic one.
//
// The cost of being slow is paid in the P-Index instead, where latency is a scored signal.
// That is the right place for it: slowness is a property of the developer's engineering,
// not of the game, so it should move their reputation and not their winnings.
package deadline

import (
	"sort"
	"time"
)

// Policy is the per-game shape of the window.
type Policy struct {
	// Base is the window for an agent with no history yet. Every agent starts here.
	Base time.Duration
	// Floor is the shortest window ever granted. Protects a fast agent from being cut
	// off by one unlucky blip, and stops the adaptive term collapsing toward zero.
	Floor time.Duration
	// Ceiling is the longest window ever granted, extensions included. A hung but
	// responsive endpoint must not be able to stall a table indefinitely.
	Ceiling time.Duration
	// Headroom multiplies the observed p95. 1.0 would cut off ~5% of an agent's honest
	// decisions; above 1 leaves room for the tail that a percentile by definition excludes.
	Headroom float64
	// Extension is granted each time the agent is confirmed alive at its deadline.
	Extension time.Duration
	// MaxExtensions bounds how many times that can happen, so Ceiling is reached in
	// bounded steps rather than by an unbounded loop.
	MaxExtensions int
}

// DefaultPolicy returns the shipped policy for a game.
//
// Bases match what the platform used before this package existed, so adopting it changes
// nothing for an agent with no history — except that Mafia's night and voting phases are
// raised from 30s, which was measurably too tight for a reasoning model deciding who to
// kill, and was silently converting real decisions into abstains.
func DefaultPolicy(game string) Policy {
	switch game {
	case "monopoly":
		return Policy{
			Base: 60 * time.Second, Floor: 15 * time.Second, Ceiling: 3 * time.Minute,
			Headroom: 1.5, Extension: 30 * time.Second, MaxExtensions: 3,
		}
	case "mafia":
		return Policy{
			Base: 60 * time.Second, Floor: 15 * time.Second, Ceiling: 2 * time.Minute,
			Headroom: 1.5, Extension: 20 * time.Second, MaxExtensions: 2,
		}
	default: // goofspiel
		return Policy{
			Base: 45 * time.Second, Floor: 10 * time.Second, Ceiling: 3 * time.Minute,
			Headroom: 1.5, Extension: 30 * time.Second, MaxExtensions: 3,
		}
	}
}

// MinSamples is how many past decisions an agent needs before its own latency is trusted
// over the policy base.
//
// Below this the sample is too small for a percentile to mean anything: three fast
// decisions would hand a genuinely slow agent a tight window and start failing it. Small
// enough that an agent settles onto its real window inside its first match or two.
const MinSamples = 8

// For computes the window for one decision.
//
// samples are the agent's recent decision latencies for this game, in milliseconds, in any
// order. Pass what you have; fewer than MinSamples means the policy base is used.
//
// Deterministic: same inputs, same window. A deadline that varies run to run cannot be
// explained to a developer who missed one.
func For(p Policy, samples []int64) time.Duration {
	p = p.withDefaults()
	if len(samples) < MinSamples {
		return clamp(p.Base, p.Floor, p.Ceiling)
	}
	tail := percentile(samples, 0.95)
	adaptive := time.Duration(float64(tail)*p.Headroom) * time.Millisecond
	// Never shorter than the policy base. The adaptive term exists to give a SLOW agent
	// room, not to punish a fast one by shrinking its window to its own p95 — a fast agent
	// that has one genuinely hard turn would otherwise be cut off by its own good record.
	if adaptive < p.Base {
		adaptive = p.Base
	}
	return clamp(adaptive, p.Floor, p.Ceiling)
}

// Extend returns the next deadline for an agent confirmed ALIVE at its current one, and
// whether an extension was actually granted.
//
// granted is false once the ceiling or the extension count is reached, which is the
// caller's signal to stop waiting and apply the fallback. Callers must not loop on this
// without checking, or a responsive-but-hung endpoint stalls the table forever.
func Extend(p Policy, elapsed time.Duration, extensionsSoFar int) (time.Duration, bool) {
	p = p.withDefaults()
	if extensionsSoFar >= p.MaxExtensions {
		return 0, false
	}
	next := elapsed + p.Extension
	if next > p.Ceiling {
		// Grant the remainder up to the ceiling rather than nothing, so the ceiling is a
		// real bound and not a cliff that silently drops the last few seconds.
		if elapsed >= p.Ceiling {
			return 0, false
		}
		return p.Ceiling - elapsed, true
	}
	return p.Extension, true
}

func (p Policy) withDefaults() Policy {
	if p.Base <= 0 {
		p.Base = 45 * time.Second
	}
	if p.Floor <= 0 {
		p.Floor = 10 * time.Second
	}
	if p.Ceiling <= 0 || p.Ceiling < p.Floor {
		p.Ceiling = 3 * time.Minute
	}
	if p.Headroom <= 0 {
		p.Headroom = 1.5
	}
	if p.Extension <= 0 {
		p.Extension = 30 * time.Second
	}
	if p.MaxExtensions < 0 {
		p.MaxExtensions = 0
	}
	return p
}

// percentile returns the q-th percentile of samples using nearest-rank, which needs no
// interpolation and is exact on the data actually observed.
func percentile(samples []int64, q float64) int64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]int64(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(q * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func clamp(v, lo, hi time.Duration) time.Duration {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
