// Package growthstats powers Super-Admin growth analytics: signup rates, conversion
// funnel, auth-provider mix, and country breakdown — all from the arena Postgres as
// the source of truth — plus optional Cloudflare zone visitor time-series.
//
// Honesty rules baked into the API responses:
//   - Visitors: Cloudflare Analytics when CLOUDFLARE_API_TOKEN + ZONE_ID are set on
//     this service; otherwise available=false so the UI never invents a number.
//   - Growth rate: WoW / MoM change in new signups (clear windows, documented).
//   - Built agent: users with ≥1 non-house agent (most accounts get one at signup).
//   - Ran SDK: users whose agent API key was used OR who sat in ≥1 match.
//   - Paying: users with ≥1 successful coin topup (ledger kind=topup, credit > 0).
//   - Country: prefers signup_country (GeoIP at account creation); falls back to
//     self-reported profile country, labeled accordingly.
package growthstats

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/cloudflare"
)

// Range windows the admin UI offers.
const (
	Range7d  = "7d"
	Range30d = "30d"
	Range90d = "90d"
)

// Summary is the investor headline for a selected range.
type Summary struct {
	Range       string            `json:"range"`
	Definitions map[string]string `json:"definitions"`
	Signups     SignupMetrics       `json:"signups"`
	Visitors    cloudflare.Visitors `json:"visitors"`
	Funnel      FunnelCounts        `json:"funnel"`
	Conversion  ConversionRates     `json:"conversion"`
	AuthMix     []AuthCount         `json:"auth_mix"`
	GeneratedAt time.Time           `json:"generated_at"`
}

type SignupMetrics struct {
	InRange      int64   `json:"in_range"`
	PriorRange   int64   `json:"prior_range"` // equal-length window immediately before
	ChangePct    float64 `json:"change_pct"`  // (in_range - prior) / prior * 100; 0 if prior=0
	WoWPct       float64 `json:"wow_pct"`     // last 7d vs previous 7d
	MoMPct       float64 `json:"mom_pct"`     // last 30d vs previous 30d
	TotalUsers   int64   `json:"total_users"`
	Signups7d    int64   `json:"signups_7d"`
	Signups30d   int64   `json:"signups_30d"`
	SignupsPrior7d  int64 `json:"signups_prior_7d"`
	SignupsPrior30d int64 `json:"signups_prior_30d"`
}

type FunnelCounts struct {
	Signups    int64 `json:"signups"`
	BuiltAgent int64 `json:"built_agent"`
	RanSDK     int64 `json:"ran_sdk"`
	Paid       int64 `json:"paid"`
}

type ConversionRates struct {
	SignupToAgentPct float64 `json:"signup_to_agent_pct"`
	SignupToSDKPct   float64 `json:"signup_to_sdk_pct"`
	SignupToPaidPct  float64 `json:"signup_to_paid_pct"`
	AgentToSDKPct    float64 `json:"agent_to_sdk_pct"`
	SDKToPaidPct     float64 `json:"sdk_to_paid_pct"`
}

type AuthCount struct {
	Provider string `json:"provider"`
	Count    int64  `json:"count"`
	Pct      float64 `json:"pct"`
}

// DayRow is one day of the signup / funnel timeseries.
type DayRow struct {
	Date       string `json:"date"`
	Signups    int64  `json:"signups"`
	BuiltAgent int64  `json:"built_agent"`
	RanSDK     int64  `json:"ran_sdk"`
	Paid       int64  `json:"paid"`
}

type CountryCount struct {
	Country string `json:"country"`
	Count   int64  `json:"count"`
	Source  string `json:"source"` // "signup_geo" | "profile" | "mixed" | "unknown"
}

// Store is the persistence port (implemented by store.GrowthStatsRepo).
type Store interface {
	SignupCounts(ctx context.Context, since, until time.Time) (int64, error)
	TotalUsers(ctx context.Context) (int64, error)
	Funnel(ctx context.Context, since, until time.Time) (FunnelCounts, error)
	AuthBreakdown(ctx context.Context, since, until time.Time) ([]AuthCount, error)
	Timeseries(ctx context.Context, since, until time.Time) ([]DayRow, error)
	Countries(ctx context.Context, since, until time.Time, limit, offset int) (rows []CountryCount, total int, err error)
}

// Service holds window math; SQL lives in Store. Cloudflare is optional.
type Service struct {
	store Store
	cf    *cloudflare.Client
	now   func() time.Time
}

func New(store Store, cf *cloudflare.Client) *Service {
	return &Service{store: store, cf: cf, now: time.Now}
}

func ParseRange(r string) (label string, lookback time.Duration) {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case Range7d:
		return Range7d, 7 * 24 * time.Hour
	case Range90d:
		return Range90d, 90 * 24 * time.Hour
	default:
		return Range30d, 30 * 24 * time.Hour
	}
}

func pctChange(cur, prev int64) float64 {
	if prev <= 0 {
		if cur > 0 {
			return 100
		}
		return 0
	}
	return float64(cur-prev) / float64(prev) * 100
}

func rate(num, den int64) float64 {
	if den <= 0 {
		return 0
	}
	return float64(num) / float64(den) * 100
}

func definitions() map[string]string {
	return map[string]string{
		"growth_rate": "Change in new signups vs the prior equal-length window. WoW = latest 7d vs previous 7d; MoM = latest 30d vs previous 30d.",
		"visitors":    "Unique site visitors from Cloudflare zone Analytics (sum of daily uniques). Configured only on the arena via CLOUDFLARE_API_TOKEN + CLOUDFLARE_ZONE_ID.",
		"signups":     "New user accounts created in the selected window (users.created_at).",
		"built_agent": "Signups in-window who own ≥1 non-house agent. Most accounts receive an agent at signup.",
		"ran_sdk":     "Signups in-window whose agent API key was used (last_used_at) or who sat in ≥1 match.",
		"paid":        "Signups in-window with ≥1 successful coin deposit (ledger topup credit).",
		"auth":        "Primary auth method on the account: Google / Apple / GitHub / email-password / Privy / other. A user may later link more; we count the first identity present.",
		"country":     "Prefers signup_country (GeoIP/CDN at account creation). Falls back to self-reported profile country. Never stores raw IP.",
	}
}

func (s *Service) visitors(ctx context.Context, rangeKey string) cloudflare.Visitors {
	if s.cf == nil || !s.cf.Enabled() {
		return cloudflare.Unavailable("Set CLOUDFLARE_API_TOKEN + CLOUDFLARE_ZONE_ID on Agentic_World (Analytics:Read on the pyyol.com zone) to show site visitors.")
	}
	v, err := s.cf.Summary(ctx, rangeKey)
	if err != nil {
		slog.Warn("growthstats: cloudflare visitors unavailable", "err", err)
		return cloudflare.Visitors{
			Available: false,
			Label:     "Cloudflare error",
			Note:      "Could not load zone analytics — signup funnel below is still from arena Postgres.",
		}
	}
	return v
}

// Visitors is the dedicated visitors endpoint payload.
func (s *Service) Visitors(ctx context.Context, rangeKey string) cloudflare.Visitors {
	return s.visitors(ctx, rangeKey)
}

func (s *Service) Summary(ctx context.Context, rangeKey string) (Summary, error) {
	label, lookback := ParseRange(rangeKey)
	now := s.now().UTC()
	since := now.Add(-lookback)
	priorSince := since.Add(-lookback)

	inRange, err := s.store.SignupCounts(ctx, since, now)
	if err != nil {
		return Summary{}, err
	}
	prior, err := s.store.SignupCounts(ctx, priorSince, since)
	if err != nil {
		return Summary{}, err
	}
	s7, err := s.store.SignupCounts(ctx, now.Add(-7*24*time.Hour), now)
	if err != nil {
		return Summary{}, err
	}
	s7p, err := s.store.SignupCounts(ctx, now.Add(-14*24*time.Hour), now.Add(-7*24*time.Hour))
	if err != nil {
		return Summary{}, err
	}
	s30, err := s.store.SignupCounts(ctx, now.Add(-30*24*time.Hour), now)
	if err != nil {
		return Summary{}, err
	}
	s30p, err := s.store.SignupCounts(ctx, now.Add(-60*24*time.Hour), now.Add(-30*24*time.Hour))
	if err != nil {
		return Summary{}, err
	}
	total, err := s.store.TotalUsers(ctx)
	if err != nil {
		return Summary{}, err
	}
	funnel, err := s.store.Funnel(ctx, since, now)
	if err != nil {
		return Summary{}, err
	}
	auth, err := s.store.AuthBreakdown(ctx, since, now)
	if err != nil {
		return Summary{}, err
	}

	return Summary{
		Range:       label,
		Definitions: definitions(),
		Signups: SignupMetrics{
			InRange:         inRange,
			PriorRange:      prior,
			ChangePct:       pctChange(inRange, prior),
			WoWPct:          pctChange(s7, s7p),
			MoMPct:          pctChange(s30, s30p),
			TotalUsers:      total,
			Signups7d:       s7,
			Signups30d:      s30,
			SignupsPrior7d:  s7p,
			SignupsPrior30d: s30p,
		},
		Visitors: s.visitors(ctx, label),
		Funnel:   funnel,
		Conversion: ConversionRates{
			SignupToAgentPct: rate(funnel.BuiltAgent, funnel.Signups),
			SignupToSDKPct:   rate(funnel.RanSDK, funnel.Signups),
			SignupToPaidPct:  rate(funnel.Paid, funnel.Signups),
			AgentToSDKPct:    rate(funnel.RanSDK, funnel.BuiltAgent),
			SDKToPaidPct:     rate(funnel.Paid, funnel.RanSDK),
		},
		AuthMix:     auth,
		GeneratedAt: now,
	}, nil
}

func (s *Service) Timeseries(ctx context.Context, rangeKey string) (string, []DayRow, error) {
	label, lookback := ParseRange(rangeKey)
	now := s.now().UTC()
	rows, err := s.store.Timeseries(ctx, now.Add(-lookback), now)
	if err != nil {
		return label, nil, err
	}
	if rows == nil {
		rows = []DayRow{}
	}
	return label, rows, nil
}

func (s *Service) Funnel(ctx context.Context, rangeKey string) (string, FunnelCounts, ConversionRates, error) {
	label, lookback := ParseRange(rangeKey)
	now := s.now().UTC()
	f, err := s.store.Funnel(ctx, now.Add(-lookback), now)
	if err != nil {
		return label, FunnelCounts{}, ConversionRates{}, err
	}
	return label, f, ConversionRates{
		SignupToAgentPct: rate(f.BuiltAgent, f.Signups),
		SignupToSDKPct:   rate(f.RanSDK, f.Signups),
		SignupToPaidPct:  rate(f.Paid, f.Signups),
		AgentToSDKPct:    rate(f.RanSDK, f.BuiltAgent),
		SDKToPaidPct:     rate(f.Paid, f.RanSDK),
	}, nil
}

func (s *Service) Auth(ctx context.Context, rangeKey string) (string, []AuthCount, error) {
	label, lookback := ParseRange(rangeKey)
	now := s.now().UTC()
	rows, err := s.store.AuthBreakdown(ctx, now.Add(-lookback), now)
	if err != nil {
		return label, nil, err
	}
	if rows == nil {
		rows = []AuthCount{}
	}
	return label, rows, nil
}

func (s *Service) Countries(ctx context.Context, rangeKey string, page, pageSize int) (string, []CountryCount, int, int, int, error) {
	label, lookback := ParseRange(rangeKey)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 25
	}
	now := s.now().UTC()
	rows, total, err := s.store.Countries(ctx, now.Add(-lookback), now, pageSize, (page-1)*pageSize)
	if err != nil {
		return label, nil, 0, page, pageSize, err
	}
	if rows == nil {
		rows = []CountryCount{}
	}
	return label, rows, total, page, pageSize, nil
}
