package platformcfg

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/agent-arena/arena/internal/platformsign"
)

// fakeSource feeds the provider canned bytes/signatures.
type fakeSource struct {
	data []byte
	sig  string
	err  error
}

func (f *fakeSource) LoadSnapshot(context.Context) ([]byte, string, error) {
	return f.data, f.sig, f.err
}
func (f *fakeSource) SubscribeChanges(context.Context) (<-chan struct{}, func()) {
	return make(chan struct{}), func() {}
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testDefaults() *Snapshot {
	return &Snapshot{
		Points:     Points{Win: 10, Loss: 2, Draw: 5},
		MatchRules: MatchRules{TurnTimeoutSec: 20, CertificationRequired: true},
		Economy:    Economy{PlatformCommissionPct: 5},
		Flags:      map[string]FeatureFlag{},
	}
}

func mustJSON(t *testing.T, s *Snapshot) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestServesDefaultsUntilPublished(t *testing.T) {
	p := New(&fakeSource{}, testDefaults(), nil, discardLog(), 0)
	p.refresh(context.Background())
	if got := p.Get().Points.Win; got != 10 {
		t.Fatalf("want default win=10, got %d", got)
	}
	if p.Get().RankedAllowed() {
		t.Fatal("no season published => ranked must be disallowed")
	}
}

func TestMergeOverDefaults(t *testing.T) {
	// Partial wire snapshot (raw JSON that OMITS most keys): only points.win and
	// an active season are present. Fields absent from the wire must retain their
	// defaults — this is the forward-compat contract (a newer engine field an
	// older publisher doesn't send keeps the engine default).
	raw := []byte(`{"version":1,"active_season":{"status":"live","ranked_enabled":true},"points":{"win":25}}`)
	src := &fakeSource{data: raw, sig: ""} // unsigned; verifier nil
	p := New(src, testDefaults(), nil, discardLog(), 0)
	p.refresh(context.Background())

	snap := p.Get()
	if snap.Points.Win != 25 {
		t.Fatalf("win should be overridden to 25, got %d", snap.Points.Win)
	}
	if snap.Points.Loss != 2 || snap.Points.Draw != 5 {
		t.Fatalf("unset point fields should keep defaults, got loss=%d draw=%d", snap.Points.Loss, snap.Points.Draw)
	}
	if snap.MatchRules.TurnTimeoutSec != 20 || !snap.MatchRules.CertificationRequired {
		t.Fatal("match_rules absent from wire should keep defaults")
	}
	if !snap.RankedAllowed() {
		t.Fatal("live season with ranked enabled should allow ranked")
	}
}

func TestRejectsBadSignature(t *testing.T) {
	seed, pub, _ := platformsign.GenerateKeypair()
	signer, _ := platformsign.NewSigner(seed)
	verifier, _ := platformsign.NewVerifier(pub)

	good := mustJSON(t, &Snapshot{Version: 7, Points: Points{Win: 99}})

	// A correctly-signed snapshot is adopted.
	okSrc := &fakeSource{data: good, sig: signer.Sign(good)}
	p := New(okSrc, testDefaults(), verifier, discardLog(), 0)
	p.refresh(context.Background())
	if p.Get().Points.Win != 99 {
		t.Fatal("correctly-signed snapshot should be adopted")
	}

	// A tampered signature is rejected; the last-known-good stays.
	badSrc := &fakeSource{data: mustJSON(t, &Snapshot{Version: 8, Points: Points{Win: 5}}), sig: "AAAA"}
	p.src = badSrc
	p.refresh(context.Background())
	if p.Get().Points.Win != 99 {
		t.Fatalf("forged snapshot must be rejected; want win=99, got %d", p.Get().Points.Win)
	}
	if p.Get().Version != 7 {
		t.Fatalf("version should still be 7 after rejecting forged v8, got %d", p.Get().Version)
	}
}

func TestIgnoresOlderVersion(t *testing.T) {
	newer := mustJSON(t, &Snapshot{Version: 5, Points: Points{Win: 50}})
	p := New(&fakeSource{data: newer}, testDefaults(), nil, discardLog(), 0)
	p.refresh(context.Background())
	if p.Get().Version != 5 {
		t.Fatalf("want v5, got %d", p.Get().Version)
	}
	// A late delivery of an older version must be ignored.
	p.src = &fakeSource{data: mustJSON(t, &Snapshot{Version: 3, Points: Points{Win: 3}})}
	p.refresh(context.Background())
	if p.Get().Version != 5 || p.Get().Points.Win != 50 {
		t.Fatalf("older version should be ignored; got v%d win=%d", p.Get().Version, p.Get().Points.Win)
	}
}

func TestKeepsLastGoodOnLoadError(t *testing.T) {
	p := New(&fakeSource{data: mustJSON(t, &Snapshot{Version: 2, Points: Points{Win: 42}})}, testDefaults(), nil, discardLog(), 0)
	p.refresh(context.Background())

	p.src = &fakeSource{err: context.DeadlineExceeded}
	p.refresh(context.Background())
	if p.Get().Points.Win != 42 {
		t.Fatal("a load error must not regress the cached snapshot")
	}

	// Garbage JSON also keeps last-known-good.
	p.src = &fakeSource{data: []byte("{not json")}
	p.refresh(context.Background())
	if p.Get().Points.Win != 42 {
		t.Fatal("a parse error must not regress the cached snapshot")
	}
}

func TestSnapshotHelpers(t *testing.T) {
	s := &Snapshot{
		Season:    &Season{Status: "live", RankedEnabled: false},
		Flags:     map[string]FeatureFlag{"beta": {Enabled: true}},
		SDK:       SDKRequirements{SupportedManifestVersions: []string{"1.1"}},
		Suspended: []string{"agt_bad"},
	}
	if s.RankedAllowed() {
		t.Fatal("ranked disabled on the season => not allowed")
	}
	if !s.HasActiveSeason() {
		t.Fatal("live season should be active")
	}
	if !s.FlagEnabled("beta", false) || s.FlagEnabled("missing", false) {
		t.Fatal("flag lookup wrong")
	}
	if !s.ManifestVersionSupported("1.1") || s.ManifestVersionSupported("2.0") {
		t.Fatal("manifest version gate wrong")
	}
	if !s.IsSuspended("agt_bad") || s.IsSuspended("agt_ok") {
		t.Fatal("suspension lookup wrong")
	}
	// Empty supported list => gate disabled (accept anything).
	open := &Snapshot{}
	if !open.ManifestVersionSupported("anything") {
		t.Fatal("empty supported list should accept any version")
	}
}
