// Package sdkstats powers the admin "SDK downloads" analytics: a hybrid of two
// sources, because neither is complete on its own.
//
//   - Registry totals (authoritative, coarse): a daily poller pulls download counts
//     from npm (api.npmjs.org) and PyPI (pypistats.org). These are the "official"
//     numbers but are DAILY (never real-time) and carry NO geography — npm exposes
//     none, and PyPI country data only exists in a separate BigQuery batch dataset.
//   - Install pings (near-real-time, geo): the SDK pings the arena on first run; we
//     GeoIP the request to a COUNTRY (we store the country code, never the raw IP)
//     and count it. This is what gives the per-country table + fresh numbers.
//
// The admin sees both: authoritative registry totals + a self-tracked per-country /
// near-real-time view. See docs/... and SDK_DOWNLOAD_ANALYTICS_PLAN.md.
package sdkstats

import (
	"context"
	"strings"
	"time"
)

// SDKs we track. Install pings carry one of these; anything else is rejected.
const (
	SDKPython = "python"
	SDKJS     = "js"
)

// Registry sources for the poller / stored daily totals.
const (
	SourcePyPI = "pypi"
	SourceNPM  = "npm"
)

// UnknownCountry is stored when we can't resolve a country (no CF header / GeoIP).
const UnknownCountry = "XX"

// TimeseriesRow is one period bucket for the x/y chart: install pings split by SDK,
// plus registry downloads split by source.
type TimeseriesRow struct {
	Period        string `json:"period"` // ISO date of the bucket start (day/week/month)
	PythonPings   int64  `json:"python_pings"`
	JSPings       int64  `json:"js_pings"`
	NPMDownloads  int64  `json:"npm_downloads"`
	PyPIDownloads int64  `json:"pypi_downloads"`
}

// CountryCount is one row of the paginated per-country install table.
type CountryCount struct {
	Country string `json:"country"`
	Count   int64  `json:"count"`
}

// Summary is the headline tiles.
type Summary struct {
	PythonPings   int64 `json:"python_pings"`
	JSPings       int64 `json:"js_pings"`
	Countries     int64 `json:"countries"`
	NPMDownloads  int64 `json:"npm_downloads"`  // registry lifetime total (from stored days)
	PyPIDownloads int64 `json:"pypi_downloads"` // registry lifetime total
}

// Store is the persistence port (implemented by store.SDKStatsRepo).
type Store interface {
	RecordInstall(ctx context.Context, sdk, version, country string) error
	Timeseries(ctx context.Context, granularity string, since time.Time) ([]TimeseriesRow, error)
	Countries(ctx context.Context, limit, offset int) (rows []CountryCount, total int, err error)
	Summary(ctx context.Context) (Summary, error)
	UpsertRegistryDay(ctx context.Context, source string, day time.Time, downloads int64) error
}

// Service holds the analytics logic (validation + windows); the SQL lives in Store.
type Service struct {
	store Store
	now   func() time.Time
}

func New(store Store) *Service { return &Service{store: store, now: time.Now} }

// validGranularity + its lookback window. Unknown → "day".
func window(granularity string) (string, time.Duration) {
	switch granularity {
	case "week":
		return "week", 52 * 7 * 24 * time.Hour // ~1 year of weeks
	case "month":
		return "month", 24 * 30 * 24 * time.Hour // ~2 years of months
	default:
		return "day", 90 * 24 * time.Hour // 90 days
	}
}

// RecordInstall validates + stores one first-run ping. Unknown SDKs are rejected so
// the data stays clean; country is normalized (2-letter upper, else XX).
func (s *Service) RecordInstall(ctx context.Context, sdk, version, country string) error {
	sdk = strings.ToLower(strings.TrimSpace(sdk))
	if sdk != SDKPython && sdk != SDKJS {
		return ErrBadSDK
	}
	return s.store.RecordInstall(ctx, sdk, strings.TrimSpace(version), NormalizeCountry(country))
}

// Timeseries returns per-period buckets for the chart over the granularity's window.
func (s *Service) Timeseries(ctx context.Context, granularity string) ([]TimeseriesRow, error) {
	gran, dur := window(granularity)
	return s.store.Timeseries(ctx, gran, s.now().Add(-dur))
}

// Countries returns the paginated per-country install table (most installs first).
func (s *Service) Countries(ctx context.Context, page, pageSize int) (rows []CountryCount, total, resolvedPage, resolvedSize int, err error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 25
	}
	rows, total, err = s.store.Countries(ctx, pageSize, (page-1)*pageSize)
	return rows, total, page, pageSize, err
}

func (s *Service) Summary(ctx context.Context) (Summary, error) { return s.store.Summary(ctx) }

// NormalizeCountry upper-cases and validates a 2-letter ISO country code; anything
// else becomes UnknownCountry ("XX"). Keeps the per-country table clean.
func NormalizeCountry(c string) string {
	c = strings.ToUpper(strings.TrimSpace(c))
	if len(c) != 2 {
		return UnknownCountry
	}
	for i := 0; i < 2; i++ {
		if c[i] < 'A' || c[i] > 'Z' {
			return UnknownCountry
		}
	}
	return c
}

// ErrBadSDK is returned for an install ping with an unrecognized sdk.
var ErrBadSDK = errBadSDK{}

type errBadSDK struct{}

func (errBadSDK) Error() string { return "unknown sdk (want python|js)" }
