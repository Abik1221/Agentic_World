package match

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The replay document is the heaviest thing the arena serves publicly — a whole event
// log per request — and it is the entire read path for the published harness clips. How
// it is cached is therefore a cost question AND a disclosure question, so both halves
// are pinned here.
//
// The asymmetry is the point: a finished log is immutable and may be cached hard; an
// unfinished one is REDACTED as it streams and must never be written down.

func TestFinishedReplayIsCacheableAndRevalidates(t *testing.T) {
	doc := ReplayDoc{MatchID: "m_1", Status: StatusFinished, ReplayHash: "abc123"}
	h := &Handler{svc: nil}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/match/m_1/replay", nil)
	h.writeReplay(rec, req, doc)

	if got := rec.Header().Get("ETag"); got != `"abc123"` {
		t.Fatalf("ETag = %q, want %q — the replay hash is the only validator that changes "+
			"exactly when the bytes do", got, `"abc123"`)
	}
	cc := rec.Header().Get("Cache-Control")
	for _, want := range []string{"public", "max-age=3600", "immutable"} {
		if !strings.Contains(cc, want) {
			t.Fatalf("Cache-Control = %q, missing %q — an immutable log re-sent on every "+
				"scrub is the cost this exists to remove", cc, want)
		}
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	// A client that already holds it must get 304 and NO body.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/v1/match/m_1/replay", nil)
	req2.Header.Set("If-None-Match", `"abc123"`)
	h.writeReplay(rec2, req2, doc)
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304 for a matching If-None-Match", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Fatalf("304 carried a %d-byte body; revalidation must cost nothing", rec2.Body.Len())
	}
}

// An in-progress match is redacted as it streams, so a cache holding one would serve a
// stale view of a live game — and could serve a mid-match snapshot after the hidden
// information stopped being secret. It must not be stored at all.
func TestUnfinishedReplayIsNeverStored(t *testing.T) {
	h := &Handler{svc: nil}
	for _, status := range []string{"live", "pending", "cancelled", ""} {
		doc := ReplayDoc{MatchID: "m_2", Status: status, ReplayHash: "def456"}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/match/m_2/replay", nil)
		h.writeReplay(rec, req, doc)

		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Fatalf("status %q: Cache-Control = %q, want no-store — a redacted live log "+
				"must not be written down by any cache", status, cc)
		}
		if et := rec.Header().Get("ETag"); et != "" {
			t.Fatalf("status %q: served an ETag %q, which invites revalidation against a "+
				"document that is only correct for the instant it was made", status, et)
		}
	}
}

// A finished match with no hash cannot be validated, so it must fall back to not being
// cached rather than being cached under an empty tag that every entity would match.
func TestFinishedReplayWithoutAHashIsNotCached(t *testing.T) {
	h := &Handler{svc: nil}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/match/m_3/replay", nil)
	h.writeReplay(rec, req, ReplayDoc{MatchID: "m_3", Status: StatusFinished})
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store when there is no validator", cc)
	}
}

// The header's real shape, not just the easy case. A weak or listed tag that failed to
// match would re-send the entire event log — a cache miss wearing a cache's clothes.
func TestIfNoneMatchHandlesListsWildcardsAndWeakTags(t *testing.T) {
	const etag = `"abc123"`
	for _, tc := range []struct {
		header string
		want   bool
	}{
		{`"abc123"`, true},
		{`W/"abc123"`, true},
		{`"zzz", "abc123"`, true},
		{`*`, true},
		{``, false},
		{`"zzz"`, false},
		{`"abc123x"`, false},
	} {
		if got := ifNoneMatch(tc.header, etag); got != tc.want {
			t.Fatalf("ifNoneMatch(%q, %q) = %v, want %v", tc.header, etag, got, tc.want)
		}
	}
}
