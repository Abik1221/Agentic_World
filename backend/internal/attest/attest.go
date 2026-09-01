// Package attest turns a certificate into something a stranger can check.
//
// # The problem this solves
//
// Every LLM benchmark in the field publishes numbers. None of them publishes anything a
// reader can independently verify: you take the leaderboard on trust, because the inputs are
// gone and the computation happened somewhere you cannot see. The strongest of them release
// transcripts, which lets you audit what happened but not recompute what was claimed.
//
// The platform's existing verified-inference stack has the same shape one level down. Its
// bind receipts are HMAC under a server-only secret, so they are tamper-evident TO US and
// verifiable by nobody else. A third party — a lab, a reviewer, a competitor — cannot check a
// single thing we assert.
//
// # The design rule
//
// A BUNDLE CARRIES ITS INPUTS, NOT JUST ITS OUTPUTS. Verification is recomputation plus a
// signature check, never a signature check alone. A signature over a number you cannot
// recompute proves only that we said it, which is exactly the trust we are trying not to ask
// for.
//
// So a bundle holds the pinned spec, the aggregated Phase A counts, the Phase B payoffs in
// play order, and the published certificate. From those four a verifier re-derives the
// prober, its digest, the lower bound, the upper bound and the interval, and compares. If our
// solver changed, if a count was edited, if a payoff was dropped, or if the numbers were
// simply typed in, the recomputation disagrees and the bundle fails — before the signature is
// even considered.
//
// # Why Ed25519 and not the existing HMAC
//
// The bind receipts cannot leave the building: a symmetric MAC proves authorship only to
// whoever holds the secret, and handing out the secret would let anyone forge. Ed25519 splits
// that — we sign with a private key, everyone verifies with the public one. It is the same
// primitive internal/platformsign already uses for the config and event planes, reused rather
// than reinvented so there is one signing story on the platform.
//
// # The chain
//
// Bundles link by hash. The audit found the platform's event log signs each message with no
// prev-hash and no sequence number, so deleting, reordering or withholding events leaves every
// remaining signature valid — that is a set of signed messages, not a chain. Here each bundle
// commits to its predecessor, so a published series cannot be silently revised: removing or
// altering an earlier certificate breaks every later link.
//
// # Purity
//
// No clock and no RNG. IssuedAt is supplied by the caller, because a bundle that stamped
// itself would not be reproducible and reproducibility is the entire product.
package attest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/agent-arena/arena/internal/exploit"
	"github.com/agent-arena/arena/internal/ladder"
	"github.com/agent-arena/arena/internal/platformsign"
)

// BundleVersion is the artifact format. Bumped when the canonical bytes change, so an old
// bundle is rejected rather than silently re-interpreted under new rules.
const BundleVersion = 1

// tolerance for comparing a recomputed float against a published one.
//
// Loose enough to absorb the last bits of floating-point summation order, tight enough that a
// genuinely different number cannot pass. It is NOT a fudge factor for a disagreeing
// computation: anything above this is a failed verification, by design.
const tolerance = 1e-9

// Bundle is everything needed to recompute a certificate from scratch.
type Bundle struct {
	Version int `json:"version"`
	// Spec is the full pinned specification, not just its hash, so a verifier does not have
	// to trust us for the parameters the measurement was made under.
	Spec     ladder.Spec `json:"spec"`
	SpecHash string      `json:"spec_hash"`
	// FitCounts are the aggregated Phase A tallies, per prize order. The prober is a
	// deterministic function of these, which is why the digest is checkable.
	FitCounts []exploit.OrderedCount `json:"fit_counts"`
	// Payoffs are the Phase B results the certificate actually used, in play order.
	//
	// Play order is recorded for replay, but it is NOT what the bundle protects: mean and
	// variance are symmetric over the published set, so a permutation changes nothing and
	// buys an adversary nothing. What IS protected is the multiset — dropping, adding or
	// editing a value all fail verification. See
	// TestReorderingPayoffsIsHarmlessAndThereforeUndetectable.
	Payoffs []float64 `json:"payoffs"`
	// Certificate is what we published.
	Certificate ladder.Certificate `json:"certificate"`
	// PrevHash chains this bundle to its predecessor. Empty for the first.
	PrevHash string `json:"prev_hash"`
	// IssuedAt is caller-supplied, never read from a clock.
	IssuedAt string `json:"issued_at"`
}

// Signed is a bundle plus the signature over its canonical bytes.
type Signed struct {
	Bundle    Bundle `json:"bundle"`
	Signature string `json:"signature"`
	// PublicKey is included so a reader can identify the signer without a side channel. It
	// does NOT establish trust — the reader must already know which key is ours. Shipping
	// it only removes an excuse not to check.
	PublicKey string `json:"public_key"`
}

// Canonical serialises a bundle deterministically: object keys sorted, recursively.
//
// Go emits struct fields in declaration order, which is stable today and would silently
// change every hash the first time somebody reorders a field. Hashing values rather than
// source layout removes that hazard, the same way ladder.Spec.Hash does.
func (b Bundle) Canonical() ([]byte, error) {
	// Sort the counts so two bundles over the same evidence serialise identically. They
	// arrive from a map iteration or a SQL scan, neither of which promises an order.
	sorted := append([]exploit.OrderedCount(nil), b.FitCounts...)
	sort.Slice(sorted, func(i, j int) bool {
		a, c := sorted[i], sorted[j]
		switch {
		case a.Order != c.Order:
			return a.Order < c.Order
		case a.Node.Me != c.Node.Me:
			return a.Node.Me < c.Node.Me
		case a.Node.Opp != c.Node.Opp:
			return a.Node.Opp < c.Node.Opp
		case a.Node.Carry != c.Node.Carry:
			return a.Node.Carry < c.Node.Carry
		default:
			return a.Card < c.Card
		}
	})
	b.FitCounts = sorted

	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return canonicalJSON(v)
}

func canonicalJSON(v any) ([]byte, error) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := []byte{'{'}
		for i, k := range keys {
			if i > 0 {
				out = append(out, ',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			vb, err := canonicalJSON(t[k])
			if err != nil {
				return nil, err
			}
			out = append(out, kb...)
			out = append(out, ':')
			out = append(out, vb...)
		}
		return append(out, '}'), nil
	case []any:
		out := []byte{'['}
		for i, e := range t {
			if i > 0 {
				out = append(out, ',')
			}
			eb, err := canonicalJSON(e)
			if err != nil {
				return nil, err
			}
			out = append(out, eb...)
		}
		return append(out, ']'), nil
	default:
		return json.Marshal(t)
	}
}

// Hash is the bundle's identity and the link a successor commits to.
func (b Bundle) Hash() (string, error) {
	raw, err := b.Canonical()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Recompute derives the certificate from the bundle's own inputs.
//
// This is the actual verification. It re-runs the solver over the published counts, rebuilds
// the prober mixture, re-derives its digest, and recomputes both halves of the interval from
// the published payoffs. A caller comparing the result against Bundle.Certificate is checking
// our arithmetic, not our word.
func (b Bundle) Recompute() (ladder.Certificate, error) {
	if b.Version != BundleVersion {
		return ladder.Certificate{}, fmt.Errorf(
			"attest: bundle version %d, this verifier understands %d", b.Version, BundleVersion)
	}
	cfgs, err := b.Spec.Games()
	if err != nil {
		return ladder.Certificate{}, err
	}
	specHash, err := b.Spec.Hash()
	if err != nil {
		return ladder.Certificate{}, err
	}
	if specHash != b.SpecHash {
		return ladder.Certificate{}, fmt.Errorf(
			"attest: spec hash mismatch (bundle says %s, spec computes %s) — the parameters "+
				"were edited after publication", b.SpecHash, specHash)
	}

	// The prober is recomputed, never carried. That is what makes the digest meaningful: a
	// solver change between publication and audit is caught here rather than silently
	// changing what the agent was measured against.
	mix, err := exploit.BootstrapMultiProbers(cfgs, b.FitCounts, b.Spec.Alpha,
		b.Spec.ProberMixture, proberSeed(b.Certificate.RunID, b.Spec.Version))
	if err != nil {
		return ladder.Certificate{}, err
	}
	digest := mix.Digest(cfgs, ladder.ProberDigest)
	if digest != b.Certificate.ProberDigest {
		return ladder.Certificate{}, fmt.Errorf(
			"attest: prober digest mismatch (certificate says %s, recomputed %s) — the "+
				"solver or the stored fit changed; this certificate is void",
			b.Certificate.ProberDigest, digest)
	}

	lower, err := exploit.SequentialCertify(cfgs[0], b.Payoffs, b.Spec.Delta/2,
		b.Spec.FirstCheckpoint)
	if err != nil {
		return ladder.Certificate{}, err
	}
	upper, err := exploit.MultiUpperBound(cfgs, b.FitCounts, b.Spec.Delta/2)
	if err != nil {
		return ladder.Certificate{}, err
	}

	out := b.Certificate
	out.Games = lower.Games
	out.MeanPayoff = lower.MeanPayoff
	out.StdDev = lower.StdDev
	out.LowerBound = lower.LowerBound
	out.UpperBound = upper
	out.Informative = lower.Informative
	out.Separable = (upper - lower.LowerBound) <= exploit.PayoffRange(cfgs[0])/3
	return out, nil
}

// proberSeed mirrors the runner's derivation so an auditor reproduces the same mixture.
// Duplicated deliberately rather than exported from ladder: if the runner's derivation ever
// changes, the verifier must NOT follow it silently — the digest check should fail loudly.
func proberSeed(runID int64, specVersion int) uint64 {
	return uint64(runID)*0x9E3779B97F4A7C15 + uint64(specVersion)
}

// Verify recomputes and compares against what was published.
//
// Every numeric field is checked, not a sample of them. A verifier that checked only the
// headline would miss a doctored sample size or standard deviation, both of which change how
// a reader weighs the number.
func (b Bundle) Verify() error {
	got, err := b.Recompute()
	if err != nil {
		return err
	}
	pub := b.Certificate
	checks := []struct {
		name       string
		got, wants float64
	}{
		{"lower_bound", got.LowerBound, pub.LowerBound},
		{"upper_bound", got.UpperBound, pub.UpperBound},
		{"mean_payoff", got.MeanPayoff, pub.MeanPayoff},
		{"std_dev", got.StdDev, pub.StdDev},
	}
	for _, c := range checks {
		if math.Abs(c.got-c.wants) > tolerance {
			return fmt.Errorf("attest: %s does not reproduce: published %.10f, recomputed %.10f",
				c.name, c.wants, c.got)
		}
	}
	if got.Games != pub.Games {
		return fmt.Errorf("attest: games does not reproduce: published %d, recomputed %d",
			pub.Games, got.Games)
	}
	// The bundle must carry EXACTLY the payoffs the certificate used, no more.
	//
	// Without this, appending payoffs past the checkpoint is silently ignored — the bound is
	// read at the largest checkpoint at or below the count, so the extras never enter the
	// arithmetic and nothing objects. That is harmless to the number but it lets a bundle
	// ship unverified data under a verified signature, which is precisely the ambiguity this
	// artifact exists to remove. Caught by TestTamperingIsCaught/a_payoff_added.
	if len(b.Payoffs) != pub.Games {
		return fmt.Errorf("attest: bundle carries %d payoffs but the certificate used %d; "+
			"a bundle must contain exactly the evidence its number rests on",
			len(b.Payoffs), pub.Games)
	}
	if got.Informative != pub.Informative || got.Separable != pub.Separable {
		return fmt.Errorf("attest: informative/separable flags do not reproduce")
	}
	return nil
}

// Sign produces a signed bundle. The signature covers the canonical bytes, so it commits to
// every input as well as every output.
func Sign(b Bundle, s *platformsign.Signer, publicKeyB64 string) (Signed, error) {
	if !s.Enabled() {
		return Signed{}, fmt.Errorf("attest: refusing to issue an unsigned bundle; a " +
			"certificate nobody can attribute is not evidence")
	}
	raw, err := b.Canonical()
	if err != nil {
		return Signed{}, err
	}
	return Signed{Bundle: b, Signature: s.Sign(raw), PublicKey: publicKeyB64}, nil
}

// VerifySigned checks the signature AND recomputes the numbers.
//
// Both, always, and in that order of importance: a valid signature over wrong arithmetic is
// an authenticated false claim, which is worse than an unsigned true one.
func VerifySigned(sg Signed, v *platformsign.Verifier) error {
	raw, err := sg.Bundle.Canonical()
	if err != nil {
		return err
	}
	if !v.Verify(raw, sg.Signature) {
		return fmt.Errorf("attest: signature does not verify")
	}
	return sg.Bundle.Verify()
}

// VerifyChain checks a published series links correctly and every bundle reproduces.
//
// The chain is what stops a silent revision: altering or removing an earlier certificate
// changes its hash and breaks every later link. Without it a signed series is just a pile of
// separately-signed claims, which is exactly what the audit found the platform's event log to
// be.
func VerifyChain(series []Signed, v *platformsign.Verifier) error {
	prev := ""
	for i, sg := range series {
		if sg.Bundle.PrevHash != prev {
			return fmt.Errorf("attest: bundle %d links to %q but its predecessor hashes to %q",
				i, sg.Bundle.PrevHash, prev)
		}
		if err := VerifySigned(sg, v); err != nil {
			return fmt.Errorf("attest: bundle %d: %w", i, err)
		}
		h, err := sg.Bundle.Hash()
		if err != nil {
			return err
		}
		prev = h
	}
	return nil
}

// SpoofCost reports what an adversary must construct to pass this ladder while serving a
// cheaper model than advertised.
//
// # Why this number exists
//
// Six published methods fingerprint an API to detect model substitution, and a 2026 attack
// defeats all of them: a cheap model plus a lookup table reproduces a token histogram
// exactly, because the probe's answer does not depend on being able to PLAY anything.
//
// An equilibrium-referenced probe is different in kind. To be published at exploitability
// eps, a substitute must actually play at eps — which means either running the advertised
// model, or computing a strategy good enough, and the strategy object it must compute and
// store is Entries large. The verifier controls that size by choosing the deck and the number
// of boards, while detection cost grows only with sample size.
//
// That asymmetry is the defensible claim. This function makes it a number rather than an
// argument.
type SpoofCost struct {
	// InfosetsPerBoard is the reachable decision points on one board.
	InfosetsPerBoard int
	// Boards is how many the spec cycles.
	Boards int
	// Entries is the total size of the strategy object a spoofer must hold: one action per
	// infoset per board. Compare against a token-histogram probe, where the equivalent
	// lookup table is one entry per distinct probe prompt.
	Entries int
	// Adaptive records that the prober is recomputed against the observed policy, so a
	// spoofer must stay unexploitable against a best response rather than match a fixed
	// distribution. This is what makes the table above a floor and not a ceiling.
	Adaptive bool
}

// Spoof computes the cost floor for a spec.
func Spoof(spec ladder.Spec) (SpoofCost, error) {
	cfgs, err := spec.Games()
	if err != nil {
		return SpoofCost{}, err
	}
	per := exploit.ReachableNodeCount(cfgs[0])
	return SpoofCost{
		InfosetsPerBoard: per,
		Boards:           len(cfgs),
		Entries:          per * len(cfgs),
		Adaptive:         true,
	}, nil
}
