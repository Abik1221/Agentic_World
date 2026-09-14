// Package cloudflare fetches zone HTTP analytics for Super-Admin Growth visitors.
// Optional: empty token/zone ⇒ Enabled() false and callers degrade honestly.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const graphqlURL = "https://api.cloudflare.com/client/v4/graphql"

func New(token, zoneID string) *Client {
	return &Client{
		token:  strings.TrimSpace(token),
		zoneID: strings.TrimSpace(zoneID),
		http:   &http.Client{Timeout: 15 * time.Second},
		now:    time.Now,
		apiURL: graphqlURL,
	}
}

// NewForTest builds a client pointed at a fake GraphQL endpoint (unit tests).
func NewForTest(token, zoneID, apiURL string, now time.Time) *Client {
	c := New(token, zoneID)
	c.apiURL = apiURL
	c.now = func() time.Time { return now }
	return c
}

// Client talks to Cloudflare's GraphQL Analytics API.
type Client struct {
	token  string
	zoneID string
	http   *http.Client
	now    func() time.Time
	apiURL string
}

func (c *Client) Enabled() bool {
	return c != nil && c.token != "" && c.zoneID != ""
}

// DayRow is one calendar day of site traffic for the configured zone.
type DayRow struct {
	Date      string `json:"date"`
	Uniques   int64  `json:"uniques"`
	Requests  int64  `json:"requests"`
	PageViews int64  `json:"page_views"`
}

// Visitors is the Growth-facing visitor payload.
type Visitors struct {
	Available bool     `json:"available"`
	Source    string   `json:"source,omitempty"`
	Label     string   `json:"label,omitempty"`
	Note      string   `json:"note,omitempty"`
	Uniques   int64    `json:"uniques,omitempty"`
	Requests  int64    `json:"requests,omitempty"`
	PageViews int64    `json:"page_views,omitempty"`
	Series    []DayRow `json:"series,omitempty"`
}

func Unavailable(note string) Visitors {
	return Visitors{
		Available: false,
		Label:     "Not configured",
		Note:      note,
	}
}

// Summary returns unique visitors / requests for the lookback window plus a daily series.
func (c *Client) Summary(ctx context.Context, rangeKey string) (Visitors, error) {
	if !c.Enabled() {
		return Unavailable("Set CLOUDFLARE_API_TOKEN + CLOUDFLARE_ZONE_ID on Agentic_World (Analytics:Read on the pyyol.com zone) to show site visitors."), nil
	}
	days := 30
	switch strings.ToLower(strings.TrimSpace(rangeKey)) {
	case "7d":
		days = 7
	case "90d":
		days = 90
	}
	now := c.now().UTC()
	end := now.Format("2006-01-02")
	start := now.AddDate(0, 0, -days+1).Format("2006-01-02")

	series, err := c.httpRequests1d(ctx, start, end)
	if err != nil {
		return Visitors{}, err
	}
	var uniques, requests, pageViews int64
	for _, row := range series {
		uniques += row.Uniques
		requests += row.Requests
		pageViews += row.PageViews
	}
	return Visitors{
		Available: true,
		Source:    "cloudflare",
		Label:     "Cloudflare Analytics",
		Note:      "Unique visitors and page views for the zone (pyyol.com). Served by the arena — not inventing numbers.",
		Uniques:   uniques,
		Requests:  requests,
		PageViews: pageViews,
		Series:    series,
	}, nil
}

func (c *Client) httpRequests1d(ctx context.Context, start, end string) ([]DayRow, error) {
	const q = `
query ($zoneTag: string!, $start: Date!, $end: Date!) {
  viewer {
    zones(filter: { zoneTag: $zoneTag }) {
      httpRequests1dGroups(
        orderBy: [date_ASC]
        limit: 100
        filter: { date_geq: $start, date_leq: $end }
      ) {
        dimensions { date }
        sum { requests pageViews }
        uniq { uniques }
      }
    }
  }
}`
	body := map[string]any{
		"query": q,
		"variables": map[string]string{
			"zoneTag": c.zoneID,
			"start":   start,
			"end":     end,
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("cloudflare graphql status %d", res.StatusCode)
	}

	var parsed struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Data struct {
			Viewer struct {
				Zones []struct {
					Groups []struct {
						Dimensions struct {
							Date string `json:"date"`
						} `json:"dimensions"`
						Sum struct {
							Requests  int64 `json:"requests"`
							PageViews int64 `json:"pageViews"`
						} `json:"sum"`
						Uniq struct {
							Uniques int64 `json:"uniques"`
						} `json:"uniq"`
					} `json:"httpRequests1dGroups"`
				} `json:"zones"`
			} `json:"viewer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Errors) > 0 {
		return nil, fmt.Errorf("cloudflare graphql: %s", parsed.Errors[0].Message)
	}
	if len(parsed.Data.Viewer.Zones) == 0 {
		return nil, fmt.Errorf("cloudflare: zone not found or token lacks Analytics:Read")
	}
	groups := parsed.Data.Viewer.Zones[0].Groups
	out := make([]DayRow, 0, len(groups))
	for _, g := range groups {
		out = append(out, DayRow{
			Date:      g.Dimensions.Date,
			Uniques:   g.Uniq.Uniques,
			Requests:  g.Sum.Requests,
			PageViews: g.Sum.PageViews,
		})
	}
	if out == nil {
		out = []DayRow{}
	}
	return out, nil
}
