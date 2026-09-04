package manifest

import (
	"context"
	"testing"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/secretbox"
)

func baseDoc() Document {
	return Document{
		ManifestVersion: "1.0",
		Agent:           AgentBlock{Name: "atlas", Version: "0.1.0", Visibility: "private"},
		Developer:       DevBlock{Name: "you"},
		Games:           []string{"goofspiel"},
		Runtime:         RuntimeBlock{Timeout: 5000},
		SDK:             SDKBlock{Language: "python", Version: "1.5.0"},
		Contact:         ContactBlock{Email: "you@example.com"},
	}
}

func errsFor(d Document) []string {
	var out []string
	for _, e := range Validate(d) {
		out = append(out, e.Field+" "+e.Message)
	}
	return out
}

// CONNECTED RANKED: no endpoint is now valid. Requiring one made every developer rent
// a server before their first ranked match, which is where they quit.
func TestManifestWithNoEndpointIsValid(t *testing.T) {
	if errs := errsFor(baseDoc()); len(errs) != 0 {
		t.Fatalf("an endpoint-less manifest must validate, got: %v", errs)
	}
}

// ALWAYS-ON: if you DO declare one, it still has to be sound. Making it optional must
// not make it unchecked — a broken endpoint is worse than none, because the platform
// would push turns at it and lose the match when they fail.
func TestADeclaredEndpointIsStillValidated(t *testing.T) {
	d := baseDoc()
	d.Endpoint = EndpointBlock{URL: "http://insecure.example.com/turn", Authentication: "bearer-token"}
	if errs := errsFor(d); len(errs) == 0 {
		t.Fatal("plain http was accepted")
	}

	d.Endpoint = EndpointBlock{URL: "https://ok.example.com/turn", Authentication: "basic"}
	if errs := errsFor(d); len(errs) == 0 {
		t.Fatal("an unsupported auth type was accepted")
	}

	d.Endpoint = EndpointBlock{URL: "not-a-url", Authentication: "bearer-token"}
	if errs := errsFor(d); len(errs) == 0 {
		t.Fatal("a malformed URL was accepted")
	}
}

func TestAValidHostedEndpointStillPasses(t *testing.T) {
	d := baseDoc()
	d.Endpoint = EndpointBlock{URL: "https://atlas.example.com/turn", Authentication: "bearer-token"}
	if errs := errsFor(d); len(errs) != 0 {
		t.Fatalf("a good hosted endpoint was rejected: %v", errs)
	}
}

// ── Verification of a connected-ranked manifest ──────────────────────────────
//
// Submitting an endpoint-free manifest was already allowed (above). VERIFYING one was
// not: the flow went straight to probing, handed the client an empty URL, and came back
// `invalid endpoint url ""`. Since ranked entry requires a VERIFIED manifest, the
// documented no-hosting path was impossible end to end — a developer following the SDK's
// own scaffold, which omits the endpoint and prints "certifies you; no endpoint
// required", could never play ranked at all.
//
// Every other part of the platform already handled this shape — PlayTarget returns "no
// target" so the socket drives the seat, and match entry demands the agent be connected
// right now. This gate was the only one that did not, and it is the one that decides
// whether the account may play.

func TestVerifyingAConnectedRankedManifestCertifiesWithoutProbing(t *testing.T) {
	m := baseManifest()
	m.EndpointURL = "" // connected-ranked: nothing to probe
	repo := &fakeRepo{owned: true, manifest: m, found: true}
	// A probe that would FAIL if it were called. Reaching the network at all is the bug.
	probe := &fakeProbe{health: agentclient.HealthResult{OK: false, Err: "should not be probed"}}
	cipher, _ := secretbox.New("key")
	svc := New(repo, probe, cipher)

	rep, err := svc.Verify(context.Background(), "usr_1", "ag_1", "man_1")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Verified {
		t.Fatalf("a connected-ranked manifest was not certified: %+v", rep)
	}
	if !repo.activated {
		t.Fatal("expected the manifest to be activated — ranked entry requires an ACTIVE verified manifest")
	}
	if len(repo.attempts) != 1 {
		t.Fatalf("expected the certification to be recorded for audit, got %d attempts", len(repo.attempts))
	}
}

func TestVerifyingAConnectedRankedManifestNeedsNoEndpointSecret(t *testing.T) {
	// A bearer-token manifest with no stored secret is a hard error on the hosted path,
	// and rightly so. With no endpoint there is nothing to authenticate TO, so demanding
	// a secret would block certification on a credential that can never be used.
	m := baseManifest()
	m.EndpointURL = ""
	m.AuthType = "bearer-token"
	repo := &fakeRepo{owned: true, manifest: m, found: true} // no token stored
	cipher, _ := secretbox.New("key")
	svc := New(repo, &fakeProbe{}, cipher)

	rep, err := svc.Verify(context.Background(), "usr_1", "ag_1", "man_1")
	if err != nil {
		t.Fatalf("certification demanded a secret for an endpoint that does not exist: %v", err)
	}
	if !rep.Verified {
		t.Fatalf("expected certification, got %+v", rep)
	}
}

func TestAHostedManifestIsStillProbed(t *testing.T) {
	// The other half. Skipping the probe whenever it is inconvenient would turn
	// certification into a formality — a declared endpoint must still be shown to answer.
	repo := &fakeRepo{owned: true, manifest: baseManifest(), found: true}
	cipher, _ := secretbox.New("key")
	sealed, _ := cipher.Seal([]byte("tok"))
	repo.token = sealed
	probe := &fakeProbe{health: agentclient.HealthResult{OK: false, Err: "connection refused"}}
	svc := New(repo, probe, cipher)

	rep, err := svc.Verify(context.Background(), "usr_1", "ag_1", "man_1")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verified {
		t.Fatal("a hosted endpoint that refused the connection was certified anyway")
	}
	if repo.activated {
		t.Fatal("a failed verification must not activate the manifest")
	}
}
