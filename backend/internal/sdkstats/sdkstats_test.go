package sdkstats

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

type fakeStore struct {
	installs   [][3]string // sdk, version, country
	registry   map[string]int64
	summary    Summary
	timeseries []TimeseriesRow
	countries  []CountryCount
	total      int
	failNext   bool
}

func newFake() *fakeStore { return &fakeStore{registry: map[string]int64{}} }

func (f *fakeStore) RecordInstall(_ context.Context, sdk, version, country string) error {
	if f.failNext {
		return context.DeadlineExceeded
	}
	f.installs = append(f.installs, [3]string{sdk, version, country})
	return nil
}
func (f *fakeStore) UpsertRegistryDay(_ context.Context, source string, day time.Time, dl int64) error {
	f.registry[source+"|"+day.Format("2006-01-02")] = dl
	return nil
}
func (f *fakeStore) Timeseries(_ context.Context, _ string, _ time.Time) ([]TimeseriesRow, error) {
	return f.timeseries, nil
}
func (f *fakeStore) Countries(_ context.Context, limit, offset int) ([]CountryCount, int, error) {
	return f.countries, f.total, nil
}
func (f *fakeStore) Summary(_ context.Context) (Summary, error) { return f.summary, nil }

func TestNormalizeCountry(t *testing.T) {
	cases := []struct{ in, want string }{
		{"us", "US"}, {"GB", "GB"}, {"  de ", "DE"},
		{"", "XX"}, {"USA", "XX"}, {"1A", "XX"}, {"u1", "XX"},
		{"XX", "XX"}, {"T1", "XX"}, {"A1", "XX"}, {"ZZ", "XX"},
	}
	for _, c := range cases {
		if got := NormalizeCountry(c.in); got != c.want {
			t.Errorf("NormalizeCountry(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRecordInstall_RejectsUnknownSDK(t *testing.T) {
	svc := New(newFake())
	if err := svc.RecordInstall(context.Background(), "ruby", "1.0", "US"); err != ErrBadSDK {
		t.Errorf("want ErrBadSDK, got %v", err)
	}
	if err := svc.RecordInstall(context.Background(), "python", "1.0", "US"); err != nil {
		t.Errorf("python should be accepted, got %v", err)
	}
}

func TestCountries_PaginationClamp(t *testing.T) {
	svc := New(newFake())
	_, _, page, size, _ := svc.Countries(context.Background(), 0, 0)
	if page != 1 || size != 25 {
		t.Errorf("clamp: page=%d size=%d, want 1/25", page, size)
	}
	_, _, _, size2, _ := svc.Countries(context.Background(), 1, 9999)
	if size2 != 25 {
		t.Errorf("oversized pageSize should clamp to 25, got %d", size2)
	}
}

func router(h *Handler) http.Handler {
	r := chi.NewRouter()
	h.Register(r)
	return r
}

func TestIngest_CountryFromCFHeader(t *testing.T) {
	f := newFake()
	h := &Handler{svc: New(f)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/telemetry/install", strings.NewReader(`{"sdk":"python","version":"1.2.0"}`))
	req.Header.Set("CF-IPCountry", "de")
	req.Header.Set("CF-Ray", "8a1b2c3d4e5f6a7b-FRA")
	req.Header.Set("CF-Connecting-IP", "203.0.113.9")
	router(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code=%d", rec.Code)
	}
	if len(f.installs) != 1 || f.installs[0] != [3]string{"python", "1.2.0", "DE"} {
		t.Errorf("install recorded wrong: %v", f.installs)
	}
}

func TestIngest_SpoofedCFHeaderIsIgnored(t *testing.T) {
	f := newFake()
	h := &Handler{svc: New(f)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/telemetry/install", strings.NewReader(`{"sdk":"js","version":"1.0.0"}`))
	req.Header.Set("CF-IPCountry", "de") // client-settable when we are NOT behind Cloudflare
	req.RemoteAddr = "8.8.8.8:44321"
	router(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code=%d", rec.Code)
	}
	if len(f.installs) != 1 {
		t.Fatalf("installs=%v", f.installs)
	}
	if f.installs[0][2] == "DE" {
		t.Fatalf("spoofed CF-IPCountry was trusted without a Cloudflare provenance header: %v", f.installs)
	}
	if f.installs[0][2] != "US" {
		t.Errorf("want GeoIP of 8.8.8.8 → US, got %v", f.installs)
	}
}

func TestIngest_GeoIPOfForwardedClient(t *testing.T) {
	f := newFake()
	h := &Handler{svc: New(f)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/telemetry/install", strings.NewReader(`{"sdk":"js","version":"1.12.1"}`))
	req.RemoteAddr = "10.0.0.4:8080" // nginx
	// 8.8.8.8 is the same probe as TestIngest_SpoofedCFHeaderIsIgnored (iploc → US).
	// 1.1.1.1 is Cloudflare anycast and iploc currently maps it to AU, which made
	// deploy-backend's race tests fail after this GeoIP path landed on main.
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	router(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code=%d", rec.Code)
	}
	if len(f.installs) != 1 || f.installs[0][2] != "US" {
		t.Errorf("want GeoIP of 8.8.8.8 → US (the nginx hop, not the container), got %v", f.installs)
	}
}

func TestIngest_PrivateIPIsUnknown(t *testing.T) {
	f := newFake()
	h := &Handler{svc: New(f)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/telemetry/install", strings.NewReader(`{"sdk":"python","version":"1.0"}`))
	req.RemoteAddr = "10.0.0.9:4000"
	router(h).ServeHTTP(rec, req)
	if len(f.installs) != 1 || f.installs[0][2] != "XX" {
		t.Errorf("private IP should stay XX, got %v", f.installs)
	}
}

func TestIngest_BadSDK400_BadJSON400(t *testing.T) {
	h := &Handler{svc: New(newFake())}
	rec := httptest.NewRecorder()
	router(h).ServeHTTP(rec, httptest.NewRequest("POST", "/v1/telemetry/install", strings.NewReader(`{"sdk":"ruby"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad sdk: code=%d want 400", rec.Code)
	}
	rec = httptest.NewRecorder()
	router(h).ServeHTTP(rec, httptest.NewRequest("POST", "/v1/telemetry/install", strings.NewReader(`nope`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: code=%d want 400", rec.Code)
	}
}

func TestAdminReads(t *testing.T) {
	f := newFake()
	f.summary = Summary{PythonPings: 10, JSPings: 5, Countries: 3, NPMDownloads: 100, PyPIDownloads: 200}
	f.timeseries = []TimeseriesRow{{Period: "2026-07-24", PythonPings: 4, NPMDownloads: 9}}
	f.countries = []CountryCount{{Country: "US", Count: 6}, {Country: "DE", Count: 4}}
	f.total = 3
	h := &Handler{svc: New(f)} // admin methods don't touch authn; call directly

	rec := httptest.NewRecorder()
	h.summary(rec, httptest.NewRequest("GET", "/x", nil))
	var sm Summary
	_ = json.Unmarshal(rec.Body.Bytes(), &sm)
	if sm.PythonPings != 10 || sm.PyPIDownloads != 200 {
		t.Errorf("summary wrong: %+v", sm)
	}

	rec = httptest.NewRecorder()
	h.timeseries(rec, httptest.NewRequest("GET", "/x?granularity=day", nil))
	if !strings.Contains(rec.Body.String(), `"2026-07-24"`) {
		t.Errorf("timeseries missing period: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.countries(rec, httptest.NewRequest("GET", "/x?page=1&page_size=25", nil))
	var cout struct {
		Countries []CountryCount `json:"countries"`
		Total     int            `json:"total"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &cout)
	if cout.Total != 3 || len(cout.Countries) != 2 || cout.Countries[0].Country != "US" {
		t.Errorf("countries wrong: %+v", cout)
	}
}

func TestPoller_ParsesNPMAndPyPI(t *testing.T) {
	npm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"package":"pyyol","downloads":[{"day":"2026-07-22","downloads":3},{"day":"2026-07-23","downloads":7}]}`))
	}))
	defer npm.Close()
	pypi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"category":"without_mirrors","date":"2026-07-22","downloads":5},{"category":"without_mirrors","date":"2026-07-23","downloads":9}]}`))
	}))
	defer pypi.Close()

	f := newFake()
	p := NewPoller(f, nil, "pyyol", time.Hour)
	p.npmBase, p.pypiBase = npm.URL, pypi.URL
	p.pollOnce(context.Background())

	if f.registry["npm|2026-07-23"] != 7 || f.registry["pypi|2026-07-22"] != 5 {
		t.Errorf("registry not upserted: %v", f.registry)
	}
}

func TestPoller_404IsSkippedGracefully(t *testing.T) {
	nf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer nf.Close()
	f := newFake()
	p := NewPoller(f, nil, "pyyol", time.Hour)
	p.npmBase, p.pypiBase = nf.URL, nf.URL
	p.pollOnce(context.Background()) // must not panic
	if len(f.registry) != 0 {
		t.Errorf("404 should upsert nothing, got %v", f.registry)
	}
}
