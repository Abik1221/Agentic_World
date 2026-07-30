package manifest

import "testing"

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
