package auth

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// The gate must FAIL CLOSED on an unset expected key. A gate that opens when
// unconfigured is worse than no gate, because it looks like one: an unset key in a new
// environment silently exposes whatever it was meant to protect, and nothing says so.
func TestMatchFailsClosed(t *testing.T) {
	cases := []struct {
		name          string
		expected, got string
		want          bool
	}{
		{"unset expected rejects a guess", "", "anything", false},
		{"unset expected rejects an empty key", "", "", false},
		{"set expected rejects an empty key", "secret", "", false},
		{"set expected rejects a wrong key", "secret", "wrong", false},
		{"set expected rejects a prefix", "secret", "sec", false},
		{"set expected rejects a longer key", "secret", "secretx", false},
		{"correct key is accepted", "secret", "secret", true},
	}
	for _, tc := range cases {
		if got := Match(tc.expected, tc.got); got != tc.want {
			t.Errorf("%s: Match(%q,%q) = %v, want %v", tc.name, tc.expected, tc.got, got, tc.want)
		}
	}
}

// APIKey wires Match into a route group; an unauthenticated request must never reach the
// handler.
func TestAPIKeyMiddleware(t *testing.T) {
	newApp := func(expected string) *fiber.App {
		app := fiber.New()
		g := app.Group("/v1", APIKey(expected))
		g.Get("/thing", func(c *fiber.Ctx) error { return c.SendString("secret data") })
		return app
	}

	t.Run("wrong key is rejected", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/thing", nil)
		req.Header.Set("X-Pyyol-Key", "wrong")
		resp, err := newApp("right").Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != fiber.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("missing key is rejected", func(t *testing.T) {
		resp, err := newApp("right").Test(httptest.NewRequest("GET", "/v1/thing", nil))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != fiber.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})

	// The one that matters most: an unconfigured gate must not become an open door.
	t.Run("unset expected key rejects everything", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/thing", nil)
		req.Header.Set("X-Pyyol-Key", "anything")
		resp, err := newApp("").Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != fiber.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 — an empty expected key must not open the route", resp.StatusCode)
		}
	})

	t.Run("correct key passes through", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/thing", nil)
		req.Header.Set("X-Pyyol-Key", "right")
		resp, err := newApp("right").Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != fiber.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})
}

// ClientIP must key on the hop OUR proxy observed, not on anything the client can set.
// Fiber's own helpers get this wrong for our topology in both directions: c.IP() returns
// nginx (one shared bucket for the whole world) and ProxyHeader mode returns the
// client-supplied left-most entry (a fresh bucket per request).
func TestClientIPIgnoresSpoofedHops(t *testing.T) {
	app := fiber.New()
	var seen []string
	app.Get("/", func(c *fiber.Ctx) error {
		seen = append(seen, ClientIP(c, 1))
		return c.SendStatus(200)
	})

	for _, spoof := range []string{"1.1.1.1", "2.2.2.2", "evil, 8.8.8.8"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Forwarded-For", spoof+", 203.0.113.7")
		if _, err := app.Test(req); err != nil {
			t.Fatal(err)
		}
	}
	for i, got := range seen {
		if got != "203.0.113.7" {
			t.Fatalf("request %d keyed to %q; every spoofed variant must key to the proxy-observed hop", i, got)
		}
	}
}

// A deeper trusted chain (e.g. a CDN in front of nginx) counts further from the right.
func TestClientIPHonoursTrustedHops(t *testing.T) {
	app := fiber.New()
	var got string
	app.Get("/", func(c *fiber.Ctx) error {
		got = ClientIP(c, 2)
		return c.SendStatus(200)
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "1.1.1.1, 203.0.113.9, 10.0.0.2")
	if _, err := app.Test(req); err != nil {
		t.Fatal(err)
	}
	if got != "203.0.113.9" {
		t.Fatalf("got %q, want 203.0.113.9 (two hops from the right)", got)
	}
}

// Health must never be throttled: the web topbar pings it for status chips, and a
// rate-limited probe would report a perfectly healthy service as down.
func TestRateLimitSkipsHealth(t *testing.T) {
	app := fiber.New()
	app.Use(RateLimit(1, 1)) // budget of one request per minute
	app.Get("/health", func(c *fiber.Ctx) error { return c.SendStatus(200) })
	app.Get("/v1/x", func(c *fiber.Ctx) error { return c.SendStatus(200) })

	for i := 0; i < 5; i++ {
		resp, err := app.Test(httptest.NewRequest("GET", "/health", nil))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != fiber.StatusOK {
			t.Fatalf("health probe %d = %d, want 200; health must be exempt", i, resp.StatusCode)
		}
	}

	// A non-health route on the same (exhausted-capable) limiter does get throttled.
	first, err := app.Test(httptest.NewRequest("GET", "/v1/x", nil))
	if err != nil {
		t.Fatal(err)
	}
	if first.StatusCode != fiber.StatusOK {
		t.Fatalf("first /v1 call = %d, want 200", first.StatusCode)
	}
	second, err := app.Test(httptest.NewRequest("GET", "/v1/x", nil))
	if err != nil {
		t.Fatal(err)
	}
	if second.StatusCode != fiber.StatusTooManyRequests {
		t.Fatalf("second /v1 call = %d, want 429", second.StatusCode)
	}
	body, err := io.ReadAll(second.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if !strings.Contains(string(body), "rate limited") {
		t.Fatalf("429 body = %q, want the rate-limited message", body)
	}
}
