package docs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeAdminStore struct {
	pages    map[string]map[string]Page // version -> slug -> page
	latest   string
	cloneErr bool
}

func newFakeAdmin() *fakeAdminStore {
	return &fakeAdminStore{pages: map[string]map[string]Page{"v1": {}}, latest: "v1"}
}

func (f *fakeAdminStore) ListPages(_ context.Context, v string) ([]Page, error) {
	return f.ListFull(nil, v)
}
func (f *fakeAdminStore) GetPage(_ context.Context, v, slug string) (Page, bool, error) {
	p, ok := f.pages[v][slug]
	return p, ok, nil
}
func (f *fakeAdminStore) Versions(_ context.Context) ([]string, string, error) {
	vs := make([]string, 0, len(f.pages))
	for v := range f.pages {
		vs = append(vs, v)
	}
	return vs, f.latest, nil
}
func (f *fakeAdminStore) UpsertPage(_ context.Context, v string, p Page) error {
	if f.pages[v] == nil {
		f.pages[v] = map[string]Page{}
	}
	f.pages[v][p.Slug] = p
	return nil
}
func (f *fakeAdminStore) DeletePage(_ context.Context, v, slug string) (bool, error) {
	if _, ok := f.pages[v][slug]; ok {
		delete(f.pages[v], slug)
		return true, nil
	}
	return false, nil
}
func (f *fakeAdminStore) CloneVersion(_ context.Context, from, to string) (int, error) {
	if f.cloneErr {
		return 0, context.DeadlineExceeded
	}
	if f.pages[to] == nil {
		f.pages[to] = map[string]Page{}
	}
	n := 0
	for slug, p := range f.pages[from] {
		f.pages[to][slug] = p
		n++
	}
	return n, nil
}
func (f *fakeAdminStore) ListFull(_ context.Context, v string) ([]Page, error) {
	var out []Page
	for _, p := range f.pages[v] {
		out = append(out, p)
	}
	return out, nil
}

func adminReq(method, target, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	return r
}

func TestAdmin_UpsertPage(t *testing.T) {
	st := newFakeAdmin()
	h := &AdminHandler{store: st}
	rec := httptest.NewRecorder()
	h.upsertPage(rec, adminReq("PUT", "/v1/admin/docs/pages",
		`{"version":"v2","slug":"games/x","title":"X","section":"Games","body":"# X"}`))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if p, ok := st.pages["v2"]["games/x"]; !ok || p.Title != "X" || p.Body != "# X" {
		t.Errorf("page not stored: %+v", st.pages["v2"])
	}
}

func TestAdmin_UpsertValidation(t *testing.T) {
	h := &AdminHandler{store: newFakeAdmin()}
	// missing slug
	rec := httptest.NewRecorder()
	h.upsertPage(rec, adminReq("PUT", "/v1/admin/docs/pages", `{"version":"v2","title":"X"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing slug: code=%d want 400", rec.Code)
	}
	// bad json
	rec = httptest.NewRecorder()
	h.upsertPage(rec, adminReq("PUT", "/v1/admin/docs/pages", `not json`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: code=%d want 400", rec.Code)
	}
}

func TestAdmin_DeletePage(t *testing.T) {
	st := newFakeAdmin()
	st.pages["v1"]["games/x"] = Page{Slug: "games/x"}
	h := &AdminHandler{store: st}

	rec := httptest.NewRecorder()
	h.deletePage(rec, adminReq("DELETE", "/v1/admin/docs/pages?version=v1&slug=games/x", ""))
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	var out struct{ Deleted bool }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.Deleted {
		t.Error("expected deleted=true")
	}
	// missing params
	rec = httptest.NewRecorder()
	h.deletePage(rec, adminReq("DELETE", "/v1/admin/docs/pages?version=v1", ""))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing slug: code=%d want 400", rec.Code)
	}
}

func TestAdmin_CreateVersionClone(t *testing.T) {
	st := newFakeAdmin()
	st.pages["v1"]["a"] = Page{Slug: "a"}
	st.pages["v1"]["b"] = Page{Slug: "b"}
	h := &AdminHandler{store: st}

	rec := httptest.NewRecorder()
	h.createVersion(rec, adminReq("POST", "/v1/admin/docs/versions", `{"version":"v2","from":"v1"}`))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Version string `json:"version"`
		Pages   int    `json:"pages"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Version != "v2" || out.Pages != 2 {
		t.Errorf("clone result wrong: %+v", out)
	}
	if len(st.pages["v2"]) != 2 {
		t.Errorf("v2 should have 2 pages, got %d", len(st.pages["v2"]))
	}

	// missing version → 400
	rec = httptest.NewRecorder()
	h.createVersion(rec, adminReq("POST", "/v1/admin/docs/versions", `{"from":"v1"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing version: code=%d want 400", rec.Code)
	}
}
