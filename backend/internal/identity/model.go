// Package identity owns users, agents, scoped API keys, and the X-claim
// onboarding flow. It implements auth.KeyResolver so the auth layer can resolve
// agent keys without importing this package's internals.
package identity

import "time"

// The agent kinds. `kind` is what an agent IS, and it decides which surfaces its
// results may reach — see migration 0094 for why harness is a third kind rather than a
// reuse of house.
//
// DECIDED AT INSERT, NEVER REPAIRED AFTERWARDS. The public sinks filter on an ALLOWLIST
// of KindExternal (migration 0093 and its siblings, 32 sites), so a new kind is invisible
// to them by default. That property only holds if the kind is correct the moment the agent
// is created: matches written while an agent was still `external` stay attributed to the
// developer board no matter what the column says later.
const (
	// KindExternal is a developer's agent — the only kind on public boards.
	KindExternal = "external"
	// KindHarness is a platform benchmark agent. LLM-backed and certified exactly like a
	// developer's agent, because its decisions ARE the measurement; excluded from every
	// user-facing surface, because its results are the platform's, not a developer's.
	KindHarness = "harness"
	// KindHouse is a deterministic table-filling bot. Present for completeness; it is
	// deliberately not creatable through the API — see ValidateCreatableKind.
	KindHouse = "house"
)

// ValidateCreatableKind reports whether a caller may CREATE an agent of this kind.
//
// KindHouse is refused on purpose. A house bot's certification exemption is granted by an
// EXPLICIT id list built at boot (SetHouseRoster), never by this column, so an API-minted
// house agent would look house-shaped to every query while holding no exemption — and the
// obvious "fix" for that is to let the roster match on kind, which is precisely the
// pattern-matching the roster exists to prevent. House bots are seeded, not signed up.
func ValidateCreatableKind(kind string) error {
	switch kind {
	case KindExternal, KindHarness:
		return nil
	}
	return errInvalid(`kind must be "` + KindExternal + `" or "` + KindHarness + `"`)
}

// Limits are the seven server-enforced spending limits. They live on the agent
// and are mutable ONLY by the owner (user scope) — never by an agent key.
type Limits struct {
	CoinLimitPerMatch    int64
	DailyLossLimit       int64
	SessionLossLimit     int64
	MinWalletBalance     int64
	MaxBid               int64
	MaxConcurrentMatches int
	CooldownLosses       int
	CooldownSeconds      int
	AutoJoin             bool
}

// DefaultLimits are what a brand-new agent is created with.
//
// THIS is the value that matters, not the column default — every INSERT passes these
// explicitly, so migration 0070's `ALTER COLUMN … SET DEFAULT` alone changed nothing for a
// real signup. The two are kept in step because the schema default is the safety net for any
// future writer that omits a column.
//
// A NEW AGENT MUST BE ABLE TO ENTER THE CHEAPEST RANKED TABLE. It could not: the per-match
// limit was 100 while the cheapest paid stake is 500 coins (migration 0038 seeds Low at 100,
// and the $5 minimum-stake floor — gamestakes.DefaultMinStakeUSDCents at a 1¢ peg — lifts
// every paid tier to at least 500). So the very first ranked join every developer attempted
// was refused with "Bid 500 exceeds the per-match limit of 100". Nobody could compete for
// real out of the box. The floor is deliberate economic policy; these numbers were simply
// set before it existed and never revisited.
//
// Derived from that floor rather than picked, so the relationship is visible:
//   - CoinLimitPerMatch 500: exactly one minimum-stake table.
//   - MaxBid 500: the same, so the two cannot contradict each other.
//   - DailyLossLimit 2000: four losses at the minimum stake before the day stops.
//   - SessionLossLimit 1000: two, so a bad session halts sooner than a bad day.
//
// MinWalletBalance stays 50 as a soft UI floor (wallet views). Sit eligibility is
// balance ≥ stake only — this field is not stacked on the bid at CheckJoin.
//
// See migration 0070 for why EXISTING agents are deliberately left alone: a stored 100
// cannot be distinguished from a deliberate 100, and widening someone's risk limit without
// being asked is the one direction that is never safe.
func DefaultLimits() Limits {
	return Limits{
		CoinLimitPerMatch: 500, DailyLossLimit: 2000, SessionLossLimit: 1000,
		MinWalletBalance: 50, MaxBid: 500, MaxConcurrentMatches: 1,
		CooldownLosses: 3, CooldownSeconds: 300, AutoJoin: false,
	}
}

// harnessMaxConcurrentMatches is the only guardrail a harness agent is created with
// differently from a developer's.
//
// # Why it has to differ at all
//
// max_concurrent_matches is a THROUGHPUT limit (wallet.CheckConcurrency): it protects the
// OWNER's provider bill and rate limits by refusing to seat an agent at several tables at
// once. A harness agent has no such owner. The platform runs it, and the pacing control is
// gamelab's runBatch, which starts matches strictly one after another precisely so a
// rate-limited free tier does not turn a batch into a wall of 429s.
//
// # Why it is not simply unlimited
//
// CheckConcurrency reads <= 0 as unbounded, but agents.max_concurrent_matches has carried
// CHECK (max_concurrent_matches >= 1) since migration 0002. The value has to be a real
// number, so it is chosen rather than disabled.
//
// # What the number actually has to clear
//
// Not concurrency — SLOTS NOT YET GIVEN BACK. ActiveMatchCount counts matches with
// status='active', and a match holds its seat's slot until it finalizes. Two things make
// that outlast the match itself:
//
//   - Finalize runs AFTER the seats receive /game-end, which is what the batch counts as
//     "finished". So the tail of match i overlaps the start of match i+1. At a limit of 1
//     that alone is fatal: `-matches 14` produced 4 and then refused every start with
//     "Already in 1 active matches (limit 1)".
//   - A match that stalls holds its slot until the liveness worker forfeits it. Not
//     hypothetical: the lab database currently carries 434 matches still marked active.
//
// Neither is bounded by the batch's own sequencing, so the ceiling has to clear a whole
// run's worth of leaked slots. gamelab onboards fresh agents per invocation, so that is
// one run — 30–50 matches per pairing is what it takes for an interval to exclude 50%.
// 64 clears it with room, and still bounds a runaway to something an operator notices.
const harnessMaxConcurrentMatches = 64

// HarnessLimits are what a PLATFORM harness agent is created with: the developer defaults
// in every respect except throughput, because a harness agent is not protecting anyone's
// bill but the platform's own.
//
// The money limits are deliberately left AT the developer values. A harness agent stakes
// real coins on real tables — that is what makes its matches the same object the developer
// board measures — so loosening the loss limits would buy a benchmark that no longer runs
// under the constraints it claims to be measuring under.
func HarnessLimits() Limits {
	l := DefaultLimits()
	l.MaxConcurrentMatches = harnessMaxConcurrentMatches
	return l
}

// LimitsForKind picks the creation-time guardrails for an agent kind, so the choice lives
// in one place rather than at each call site that happens to create an agent.
func LimitsForKind(kind string) Limits {
	if kind == KindHarness {
		return HarnessLimits()
	}
	return DefaultLimits()
}

// Validate guards against nonsensical limit values before they reach the DB.
func (l Limits) Validate() error {
	switch {
	case l.CoinLimitPerMatch <= 0:
		return errInvalid("coin_limit_per_match must be > 0")
	case l.MaxBid <= 0:
		return errInvalid("max_bid must be > 0")
	case l.MaxBid > l.CoinLimitPerMatch:
		return errInvalid("max_bid cannot exceed coin_limit_per_match")
	case l.MinWalletBalance < 0:
		return errInvalid("min_wallet_balance must be >= 0")
	case l.MaxConcurrentMatches < 1:
		return errInvalid("max_concurrent_matches must be >= 1")
	case l.CooldownLosses < 0 || l.CooldownSeconds < 0:
		return errInvalid("cooldown values must be >= 0")
	}
	return nil
}

// User is the accountable human owner.
type User struct {
	PublicID string
	XHandle  string
	XUserID  string
}

// Agent is a competitor owned by a user.
type Agent struct {
	PublicID          string
	OwnerPublicID     string
	Name              string
	Slug              string
	Description       string
	Framework         string
	Status            string
	VerificationLevel string
	Limits            Limits
}

// Claim is an in-flight X-claim onboarding token.
type Claim struct {
	Token       string
	AgentName   string
	Description string
	Status      string
	ExpiresAt   time.Time
}

// KeyRecord is the data needed to authenticate a presented API key. Returned by
// the repo (implemented in internal/store), hence exported.
type KeyRecord struct {
	AgentPublicID string
	OwnerPublicID string
	Hash          string
}

// KeyInfo is one agent key for the management UI. The secret is never included —
// only the public prefix, ownership, label, and audit timestamps.
//
// Label is the machine or deployment holding the key ("macbook-pro", "ci-runner",
// "fly-io"). It is always present in the JSON, empty for keys minted before
// migration 0071 — the UI must render "" as unnamed rather than hiding the row,
// because an unnamed key is still a live credential the owner may want to revoke.
type KeyInfo struct {
	Prefix        string     `json:"prefix"`
	AgentPublicID string     `json:"agent"`
	Label         string     `json:"label"`
	CreatedAt     time.Time  `json:"created_at"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
}

// AgentProfile is an agent's public display identity (migration 0019:
// agents.display_name / bio / avatar_url), mutable by the owner (user scope).
type AgentProfile struct {
	AgentPublicID string
	DisplayName   string
	Bio           string
	AvatarURL     string
}

// MagicLink is the owner (and their agent) resolved by consuming a single-use
// passwordless sign-in token.
type MagicLink struct {
	UserPublicID  string
	AgentPublicID string // empty if the owner has no agent yet
}
