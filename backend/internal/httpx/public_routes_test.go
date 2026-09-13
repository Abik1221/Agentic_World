package httpx

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The public route surface, pinned.
//
// # Why a golden list and not a rule
//
// Every entry here answers a request from the open internet with no credential. That is correct
// for logging in, for a spectator watching a match, and for a webhook that authenticates itself
// by signature instead. It is catastrophic for anything that moves coins or reads private state.
//
// There is no rule that separates the two — "does /v1/agents/{id}/manifest/public leak anything"
// is a judgement, not a pattern. So the list is the judgement, written down. Adding a public
// route means editing this file, which is exactly the moment to ask whether it should be public.
//
// This is the same shape as the /metrics guard next door, which exists because that endpoint
// once served the entire route table and coins_staked_total to anyone who asked.
//
// # What a failure means
//
// EXTRA (in code, not in this list): a route was made public. If that was deliberate, add it
// here and say why in the commit. If it was not, the middleware is missing.
//
// MISSING (in this list, not in code): a route was removed or is now authenticated. Delete the
// line. This direction is not a security problem, but a stale list quietly loses its meaning.
var publicRoutes = []string{
	"Get /docs",
	"Get /healthz",
	"Get /openapi.yaml",
	"Get /ping",
	"Get /readyz",
	"Get /v1/agent/{id}/profile",
	"Get /v1/agents/{agent_id}/followers",
	"Get /v1/agents/{agent_id}/manifest/public",
	"Get /v1/arenas",
	"Get /v1/auth/magic-link/verify",
	// Public deliberately, and the guard made me say so. Both are read-only aggregates over
	// FINISHED matches: the roles they score against are already revealed at the end of a game,
	// so nothing here leaks a live table's secret. The methodology is public for the same reason
	// the model board's is — a conduct claim published without its method is not defensible, and
	// the chance baseline is the part that stops the number being read backwards.
	"Get /v1/benchmark/deception",
	"Get /v1/benchmark/deception/methodology",
	"Get /v1/benchmark/developers",
	// The PLATFORM harness board: the same fit over the platform's own benchmark matches,
	// published deliberately. Public because a benchmark nobody can read is not a benchmark,
	// and separate from /modelboard because the two must never be confused for one another.
	"Get /v1/benchmark/harness",
	// The matches behind the harness numbers. Public for the same reason the board is:
	// a benchmark that cannot be watched is a claim rather than evidence. Carries no
	// developer data — the query admits only harness-kind agents.
	"Get /v1/benchmark/harness/matches",
	"Get /v1/benchmark/harness/models",
	"Get /v1/benchmark/harness/history",
	"Get /v1/benchmark/harness/methodology",
	"Get /v1/benchmark/model",
	"Get /v1/benchmark/modelboard",
	"Get /v1/benchmark/modelboard/history",
	"Get /v1/benchmark/modelboard/methodology",
	"Get /v1/benchmark/models",
	"Get /v1/clips/trending",
	"Get /v1/developers",
	"Get /v1/developers/spotlight",
	"Get /v1/developers/username-available",
	"Get /v1/developers/{handle}",
	"Get /v1/developers/{handle}/followers",
	"Get /v1/developers/{handle}/following",
	"Get /v1/developers/{handle}/matches",
	"Get /v1/developers/{handle}/pindex",
	"Get /v1/docs",
	"Get /v1/docs/*",
	"Get /v1/docs/versions",
	"Get /v1/games",
	"Get /v1/games/{game}/stakes",
	"Get /v1/leaderboard",
	"Get /v1/leaderboard/developers",
	"Get /v1/mafia/live",
	"Get /v1/mafia/{id}/economy",
	"Get /v1/mafia/{id}/replay",
	"Get /v1/mafia/{id}/roster",
	"Get /v1/mafia/{id}/watch",
	"Get /v1/match/{id}/replay",
	"Get /v1/match/{id}/roster",
	"Get /v1/match/{id}/watch",
	"Get /v1/matches/live",
	"Get /v1/media/avatars/{name}",
	// The engine's own board table (prices, rent tiers, mortgage). Public because the
	// rules of a staked game have to be readable before you stake on them, and it is
	// match-independent static data — no seat, no state, nothing to redact.
	"Get /v1/payments/flows",
	"Get /v1/pindex/methodology",
	"Get /v1/rankings/standing",
	"Get /v1/register/verify",
	"Get /v1/seasons/champion",
	"Get /v1/seasons/current",
	"Get /v1/stats/live",
	"Get /v1/tournaments",
	"Get /v1/tournaments/{id}",
	// Login exchanges: the credential is the provider token itself (ID token / OAuth
	// code), not a dashboard JWT. Public for the same reason email/password login is.
	"Post /v1/auth/apple",
	"Post /v1/auth/github",
	"Post /v1/auth/google",
	"Post /v1/auth/login",
	"Post /v1/auth/logout",
	"Post /v1/auth/magic-link",
	"Post /v1/auth/privy",
	"Post /v1/auth/refresh",
	"Post /v1/auth/signup",
	"Post /v1/register",
	"Post /v1/telemetry/install",
	"Post /v1/webhooks/stripe",
}

// Routes registered with no auth middleware, read from source.
//
// Source-reading rather than router-walking on purpose: building the real router needs a
// database, Redis, and thirty collaborators, so the test would be skipped in exactly the
// environments that matter. The registration line is also where a developer adds a route, which
// makes it the honest place to check.
func TestPublicRouteSurfaceIsPinned(t *testing.T) {
	root := ".."
	// r.Get("/x", h) is public; r.With(auth).Get("/x", h) is not.
	re := regexp.MustCompile(`r\.(With\([a-zA-Z]+\)\.)?(Get|Post|Put|Delete|Patch)\("(/[^"]*)"`)

	found := map[string]bool{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path) //nolint:gosec // walking our own tree
		if err != nil {
			return err
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			if m[1] != "" {
				continue // has middleware -> not public
			}
			found[m[2]+" "+m[3]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking source: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("found no route registrations at all — the pattern has drifted and this test is " +
			"now asserting nothing, which is worse than failing")
	}

	pinned := map[string]bool{}
	for _, r := range publicRoutes {
		pinned[r] = true
	}
	var extra, missing []string
	for r := range found {
		if !pinned[r] {
			extra = append(extra, r)
		}
	}
	for r := range pinned {
		if !found[r] {
			missing = append(missing, r)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)

	for _, r := range extra {
		t.Errorf("UNPINNED PUBLIC ROUTE %q — it answers unauthenticated requests from the open "+
			"internet. If that is intended, add it to publicRoutes and say why; if not, it is "+
			"missing its auth middleware.", r)
	}
	for _, r := range missing {
		t.Errorf("STALE ENTRY %q is pinned as public but no longer registered that way — remove "+
			"the line so the list keeps meaning something", r)
	}
}

// The detection logic itself, on synthetic input.
//
// TestPublicRouteSurfaceIsPinned passing tells us the current tree is clean; it does not tell us
// the test would NOTICE a new public route. Proving that by making a route public for real would
// mean editing production source to test it, so the comparison is exercised directly instead.
func TestUnpinnedRouteIsDetected(t *testing.T) {
	pinned := map[string]bool{"Get /v1/public": true, "Post /v1/auth/login": true}
	found := map[string]bool{
		"Get /v1/public":           true,
		"Post /v1/auth/login":      true,
		"Post /v1/wallet/withdraw": true, // newly made public, and never pinned
	}
	var extra []string
	for r := range found {
		if !pinned[r] {
			extra = append(extra, r)
		}
	}
	if len(extra) != 1 || extra[0] != "Post /v1/wallet/withdraw" {
		t.Fatalf("a newly-public route was not detected; extra = %v", extra)
	}

	// And the other direction: a pinned route that is no longer public must be reported, or the
	// list rots into a set of claims nobody is checking.
	delete(found, "Get /v1/public")
	var missing []string
	for r := range pinned {
		if !found[r] {
			missing = append(missing, r)
		}
	}
	if len(missing) != 1 || missing[0] != "Get /v1/public" {
		t.Fatalf("a stale pinned entry was not detected; missing = %v", missing)
	}
}

// The regex must actually distinguish middleware-wrapped registrations from bare ones. If it
// stopped matching `r.With(auth).Get(...)`, every authenticated route would look public and the
// test would fail loudly — but if it stopped matching bare `r.Get(...)`, the surface would look
// EMPTY and the test would pass while checking nothing. The zero-match guard in the main test
// covers that; this pins the discrimination itself.
func TestRoutePatternDistinguishesAuthenticatedRoutes(t *testing.T) {
	re := regexp.MustCompile(`r\.(With\([a-zA-Z]+\)\.)?(Get|Post|Put|Delete|Patch)\("(/[^"]*)"`)
	src := `
		r.Get("/v1/open", h.open)
		r.With(agent).Post("/v1/queue", h.enqueue)
		r.With(admin).Delete("/v1/admin/thing", h.del)
	`
	var public []string
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		if m[1] == "" {
			public = append(public, m[2]+" "+m[3])
		}
	}
	if len(public) != 1 || public[0] != "Get /v1/open" {
		t.Fatalf("pattern mis-classified routes; public = %v (want only the bare r.Get)", public)
	}
}
