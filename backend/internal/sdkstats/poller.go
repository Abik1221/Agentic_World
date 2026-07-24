package sdkstats

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Poller pulls DAILY download totals from npm + PyPI and upserts them. Registry data
// is daily (never real-time) and 404s until the package is published — both handled
// gracefully (a 404 just logs once and moves on). Runs on start, then every Interval.
type Poller struct {
	store    Store
	http     *http.Client
	log      *slog.Logger
	pkg      string        // package name on both registries (default "pyyol")
	interval time.Duration // default 12h (registry data updates ~daily)
	npmBase  string        // override for tests
	pypiBase string        // override for tests
}

// NewPoller builds the registry poller. pkg defaults to "pyyol"; interval to 12h.
func NewPoller(store Store, log *slog.Logger, pkg string, interval time.Duration) *Poller {
	if pkg == "" {
		pkg = "pyyol"
	}
	if interval <= 0 {
		interval = 12 * time.Hour
	}
	if log == nil {
		log = slog.Default()
	}
	return &Poller{
		store:    store,
		http:     &http.Client{Timeout: 15 * time.Second},
		log:      log,
		pkg:      pkg,
		interval: interval,
		npmBase:  "https://api.npmjs.org",
		pypiBase: "https://pypistats.org",
	}
}

// Run polls immediately, then every Interval, until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	p.pollOnce(ctx)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.pollOnce(ctx)
		}
	}
}

func (p *Poller) pollOnce(ctx context.Context) {
	if n, err := p.pollNPM(ctx); err != nil {
		p.log.Info("sdkstats: npm poll skipped", "err", err)
	} else if n > 0 {
		p.log.Info("sdkstats: npm downloads synced", "days", n)
	}
	if n, err := p.pollPyPI(ctx); err != nil {
		p.log.Info("sdkstats: pypi poll skipped", "err", err)
	} else if n > 0 {
		p.log.Info("sdkstats: pypi downloads synced", "days", n)
	}
}

// pollNPM: GET /downloads/range/last-month/<pkg> → {downloads:[{day,downloads}]}.
func (p *Poller) pollNPM(ctx context.Context) (int, error) {
	var body struct {
		Downloads []struct {
			Day       string `json:"day"`
			Downloads int64  `json:"downloads"`
		} `json:"downloads"`
	}
	if err := p.getJSON(ctx, p.npmBase+"/downloads/range/last-month/"+p.pkg, &body); err != nil {
		return 0, err
	}
	n := 0
	for _, d := range body.Downloads {
		day, err := time.Parse("2006-01-02", d.Day)
		if err != nil {
			continue
		}
		if err := p.store.UpsertRegistryDay(ctx, SourceNPM, day, d.Downloads); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// pollPyPI: GET /api/packages/<pkg>/overall?mirrors=false → {data:[{date,downloads}]}.
func (p *Poller) pollPyPI(ctx context.Context) (int, error) {
	var body struct {
		Data []struct {
			Category  string `json:"category"`
			Date      string `json:"date"`
			Downloads int64  `json:"downloads"`
		} `json:"data"`
	}
	if err := p.getJSON(ctx, p.pypiBase+"/api/packages/"+p.pkg+"/overall?mirrors=false", &body); err != nil {
		return 0, err
	}
	// One row per date (category "without_mirrors").
	perDay := map[string]int64{}
	for _, d := range body.Data {
		perDay[d.Date] += d.Downloads
	}
	n := 0
	for date, dl := range perDay {
		day, err := time.Parse("2006-01-02", date)
		if err != nil {
			continue
		}
		if err := p.store.UpsertRegistryDay(ctx, SourcePyPI, day, dl); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (p *Poller) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("package %q not found (not published yet?)", p.pkg)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("registry returned %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
