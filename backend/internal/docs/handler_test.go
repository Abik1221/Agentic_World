package docs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeStore struct {
	pages   []Page
	version string
}

func (f *fakeStore) ListPages(_ context.Context, version string) ([]Page, error) {
	if version != f.version {
		return nil, nil
	}
	return f.pages, nil
}
func (f *fakeStore) GetPage(_ context.Context, version, slug string) (Page, bool, error) {
	if version != f.version {
		return Page{}, false, nil
	}
	for _, p := range f.pages {
		if p.Slug == slug {
			return p, true, nil
		}
	}
	return Page{}, false, nil
}
func (f *fakeStore) Versions(_ context.Context) ([]string, string, error) {
	return []string{f.version}, f.version, nil
}

func newRouter(store Store) http.Handler {
	r := chi.NewRouter()
	NewHandler(store).Register(r)
	return r
}

func testStore() *fakeStore {
	return &fakeStore{
		version: "2026-07-24",
		pages: []Page{
			{Slug: "getting-started/index", Title: "Welcome", Section: "Getting Started", Order: 1},
			{Slug: "games/goofspiel", Title: "Goofspiel", Section: "Games", Game: "goofspiel", Order: 1, Body: "# Goofspiel\n"},
		},
	}
}

func TestTree_GroupsBySectionAndResolvesLatest(t *testing.T) {
	rec := httptest.NewRecorder()
	newRouter(testStore()).ServeHTTP(rec, httptest.NewRequest("GET", "/v1/docs", nil)) // no version → latest
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	var out struct {
		Version  string `json:"version"`
		Sections []struct {
			Section string `json:"section"`
			Pages   []struct {
				Slug string `json:"slug"`
			} `json:"pages"`
		} `json:"sections"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Version != "2026-07-24" {
		t.Errorf("version = %q, want latest", out.Version)
	}
	if len(out.Sections) != 2 || out.Sections[0].Section != "Getting Started" {
		t.Fatalf("sections wrong: %+v", out.Sections)
	}
}

func TestPage_NestedSlug(t *testing.T) {
	rec := httptest.NewRecorder()
	newRouter(testStore()).ServeHTTP(rec, httptest.NewRequest("GET", "/v1/docs/games/goofspiel", nil))
	if rec.Code != 200 {
		t.Fatalf("code = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Slug, Body string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Slug != "games/goofspiel" || out.Body == "" {
		t.Errorf("nested slug page wrong: %+v", out)
	}
}

func TestPage_NotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	newRouter(testStore()).ServeHTTP(rec, httptest.NewRequest("GET", "/v1/docs/nope/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
}

func TestVersions(t *testing.T) {
	rec := httptest.NewRecorder()
	newRouter(testStore()).ServeHTTP(rec, httptest.NewRequest("GET", "/v1/docs/versions", nil))
	var out struct {
		Versions []string `json:"versions"`
		Latest   string   `json:"latest"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Latest != "2026-07-24" || len(out.Versions) != 1 {
		t.Errorf("versions wrong: %+v", out)
	}
}
