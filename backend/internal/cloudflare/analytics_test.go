package cloudflare_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/cloudflare"
)

func TestSummary_DisabledWithoutSecrets(t *testing.T) {
	c := cloudflare.New("", "")
	v, err := c.Summary(context.Background(), "30d")
	if err != nil {
		t.Fatal(err)
	}
	if v.Available {
		t.Fatal("expected unavailable without token/zone")
	}
	if v.Note == "" {
		t.Fatal("expected setup note")
	}
}

func TestSummary_ParsesGraphQL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing bearer")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"viewer": map[string]any{
					"zones": []any{
						map[string]any{
							"httpRequests1dGroups": []any{
								map[string]any{
									"dimensions": map[string]any{"date": "2026-09-01"},
									"sum":        map[string]any{"requests": 100, "pageViews": 80},
									"uniq":       map[string]any{"uniques": 40},
								},
								map[string]any{
									"dimensions": map[string]any{"date": "2026-09-02"},
									"sum":        map[string]any{"requests": 50, "pageViews": 45},
									"uniq":       map[string]any{"uniques": 20},
								},
							},
						},
					},
				},
			},
		})
	}))
	defer srv.Close()

	c := cloudflare.NewForTest("test-token", "zone-abc", srv.URL, time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	v, err := c.Summary(context.Background(), "7d")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Available || v.Uniques != 60 || len(v.Series) != 2 {
		t.Fatalf("got %+v", v)
	}
	if v.Source != "cloudflare" {
		t.Fatalf("source=%s", v.Source)
	}
}
