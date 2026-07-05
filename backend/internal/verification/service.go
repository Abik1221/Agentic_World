package verification

import "context"

// Verification levels / badges (scaffold; promotion logic deepens in Stage 9).
const (
	BadgeNew            = "new"             // < 10 matches
	BadgeVerifiedBot    = "verified_bot"    // consistent bot timing over enough matches
	BadgeTournament     = "tournament_ready" // verified + clean record (Stage 9 gates funded play)
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
	// RecentSamples returns up to `limit` most-recent response times (ms).
	RecentSamples(ctx context.Context, agentPublicID string, limit int) ([]int, error)
}

// Service computes timing profiles, eligibility, and badges.
type Service struct {
	repo Repo
}

func New(repo Repo) *Service { return &Service{repo: repo} }

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
func (s *Service) CheckEligibility(ctx context.Context, agentPublicID string) (Eligibility, error) {
	samples, err := s.repo.RecentSamples(ctx, agentPublicID, 200)
	if err != nil {
		return Eligibility{}, err
	}
	p := AnalyzeTimingProfile(samples)
	e := Eligibility{Eligible: true, Profile: p, Badge: badgeFor(p)}
	if p.Count >= minSamplesToJudge && p.HumanLikelihood >= humanLikelihoodFlag {
		e.Eligible = false
		e.Reason = "high_human_likelihood"
	}
	return e, nil
}

func badgeFor(p TimingProfile) string {
	if p.Count >= verifiedBotMatches && p.HumanLikelihood <= verifiedBotMaxHuman {
		return BadgeVerifiedBot
	}
	return BadgeNew
}
