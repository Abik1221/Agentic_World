package verification

import (
	"context"
	"errors"
)

// ErrAgentNotFound is returned when a review names an agent that does not exist. It is a
// distinct error rather than a silent no-op because the two outcomes look identical to an
// operator and mean opposite things: one released a flagged agent, the other mistyped an
// id and released nobody.
var ErrAgentNotFound = errors.New("verification: no such agent")

// Verification levels / badges (scaffold; promotion logic deepens in Stage 9).
const (
	BadgeNew         = "new"              // < 10 matches
	BadgeVerifiedBot = "verified_bot"     // consistent bot timing over enough matches
	BadgeTournament  = "tournament_ready" // verified + clean record (Stage 9 gates funded play)
)

// Thresholds for v1 eligibility. Conservative: only flag once there is enough
// evidence, to avoid false positives on a sparse history.
const (
	minSamplesToJudge   = 20
	humanLikelihoodFlag = 0.80
	verifiedBotMatches  = 50
	verifiedBotMaxHuman = 0.30
)

// Repo is verification's persistence port (implemented in internal/store).
type Repo interface {
	// InsertSample records one action's response time. matchPublicID may be nil
	// in contexts without a match (it is set from Stage 3 onward).
	InsertSample(ctx context.Context, agentPublicID string, matchPublicID *string, responseMs int) error
	// RecentSamples returns up to `limit` most-recent response times (ms), counting only
	// what the agent has done SINCE its last review (see Review).
	RecentSamples(ctx context.Context, agentPublicID string, limit int) ([]int, error)
	// Review records an operator's decision to judge this agent afresh, and is what makes
	// the "flagged for review" refusal reviewable at all.
	//
	// It clears nothing and exempts nothing — it marks an instant, after which the timing
	// detector considers only newer samples. That distinction is the whole design: an
	// exemption would be a hole in a fraud control, while a clean slate leaves the control
	// in force and simply stops judging an agent forever on evidence it can no longer
	// outrun. A flagged agent cannot play (not ranked, not a room, not even sandbox) and
	// so cannot produce the faster samples that would clear it; without a review the
	// refusal is permanent, including when it is wrong.
	Review(ctx context.Context, agentPublicID, reviewedBy, reason string) error
}

// ProvenShare is how much of an agent's play is cryptographically proven LLM-backed.
//
// A narrow port so this package depends on the fact and not on the gateway. Satisfied by
// *store.LLMGatewayRepo.
type ProvenShare interface {
	// ProvenShare returns bound decisions and total decisions across the agent's history.
	ProvenShare(ctx context.Context, agentPublicID string) (bound, decisions int, err error)
}

// provenOverridesTiming is the share of an agent's decisions that must be PROVEN before a
// cryptographic proof is allowed to outrank the timing guess.
//
// Deliberately high, and deliberately not "any proof at all". The model board learned this
// expensively: resolving a tier from the BEST evidence ever seen let one verified call in ten
// thousand decisions label a whole row verified, which rewards routing 1% of your calls and
// making the rest elsewhere. The same shape here would be worse — a single bound call would
// buy permanent exemption from a fraud control.
//
// Matches rating.VerifiedCoverageThreshold. Restated rather than imported because this package
// deliberately does not depend on rating; if one moves, the other should follow.
const provenOverridesTiming = 0.90

// Service computes timing profiles, eligibility, and badges.
type Service struct {
	repo   Repo
	proven ProvenShare
}

func New(repo Repo) *Service { return &Service{repo: repo} }

// SetProvenShare installs the completion-binding evidence, so a PROOF can outrank the timing
// guess. Optional: without it, eligibility is decided exactly as it was before.
func (s *Service) SetProvenShare(p ProvenShare) { s.proven = p }

// Record captures a single action's response time (best-effort; non-fatal).
func (s *Service) Record(ctx context.Context, agentPublicID string, matchPublicID *string, responseMs int) error {
	if responseMs < 0 {
		responseMs = 0
	}
	return s.repo.InsertSample(ctx, agentPublicID, matchPublicID, responseMs)
}

// Eligibility is the verdict consulted before allowing an agent into a match.
type Eligibility struct {
	Eligible bool
	Reason   string
	Profile  TimingProfile
	Badge    string
}

// CheckEligibility flags agents that look like humans once there is enough
// evidence. With sparse history it admits by default (innocent until proven).
//
// # A proof outranks a guess
//
// The timing profile is CIRCUMSTANTIAL: it infers "a human is playing this by hand" from how
// response times are distributed. Completion binding is DIRECT — the gateway saw the model
// emit the move, and the match refused to apply anything else. An agent whose decisions are
// bound cannot be a human choosing moves, because a human's move would have been rejected.
//
// So when the proof is present and covers most of the agent's play, it settles the question the
// timing detector was estimating. This is not an exemption cut into a fraud control; it is the
// same question answered by better evidence. The share floor is what keeps it that way — see
// provenOverridesTiming.
//
// An unreadable proof leaves the timing verdict STANDING. That direction is deliberate and
// opposite to movebind.Enforce: there, absence must not reject an honest move; here, absence
// must not excuse a flagged one. A lookup failure that granted eligibility would be an
// exemption reachable by breaking the database.
func (s *Service) CheckEligibility(ctx context.Context, agentPublicID string) (Eligibility, error) {
	samples, err := s.repo.RecentSamples(ctx, agentPublicID, 200)
	if err != nil {
		return Eligibility{}, err
	}
	p := AnalyzeTimingProfile(samples)
	e := Eligibility{Eligible: true, Profile: p, Badge: badgeFor(p)}
	if p.Count >= minSamplesToJudge && p.HumanLikelihood >= humanLikelihoodFlag {
		if s.provenLLMBacked(ctx, agentPublicID) {
			e.Reason = "timing_flag_overridden_by_completion_binding"
			return e, nil
		}
		e.Eligible = false
		e.Reason = "high_human_likelihood"
	}
	return e, nil
}

// provenLLMBacked reports whether completion binding has settled what the timing profile was
// only estimating. False on any doubt, including an unreadable lookup.
func (s *Service) provenLLMBacked(ctx context.Context, agentPublicID string) bool {
	if s.proven == nil {
		return false
	}
	bound, decisions, err := s.proven.ProvenShare(ctx, agentPublicID)
	if err != nil || decisions <= 0 {
		return false
	}
	return float64(bound)/float64(decisions) >= provenOverridesTiming
}

func badgeFor(p TimingProfile) string {
	if p.Count >= verifiedBotMatches && p.HumanLikelihood <= verifiedBotMaxHuman {
		return BadgeVerifiedBot
	}
	return BadgeNew
}

// Review clears an agent's slate: the timing detector will judge it only on what it does
// from this moment, and the samples that flagged it are kept but no longer counted.
//
// This is the missing half of "Agent flagged for review". The refusal names a review that,
// until now, nothing in the system could perform — no endpoint, no command, and no code
// anywhere that deleted a timing sample. That made every flag final, including the ones
// the detector got wrong, and it gets them wrong cheaply: a provider outage, a rate limit
// or an unset API key produces twenty slow, erratic turns, which is exactly the shape it
// looks for.
//
// Deliberately NOT an exemption. An agent released here is judged by the same rule as
// everyone else on its next twenty moves, so a human genuinely playing by hand is flagged
// again almost immediately. Nothing about the detector is softened, and there is no
// per-agent bypass for a later change to widen.
//
// `reviewedBy` and `reason` are required by the caller, not defaulted here: a clean slate
// with no attributed decision is indistinguishable from an accident when someone reads the
// table months later.
func (s *Service) Review(ctx context.Context, agentPublicID, reviewedBy, reason string) error {
	return s.repo.Review(ctx, agentPublicID, reviewedBy, reason)
}
