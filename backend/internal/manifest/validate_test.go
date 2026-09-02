package manifest

import (
	"strings"
	"testing"
)

// validDoc returns a manifest that passes validation; tests mutate a copy.
func validDoc() Document {
	return Document{
		ManifestVersion: "1.0",
		Agent: AgentBlock{
			Name:        "Atlas",
			Description: "Strategic AI Agent",
			Version:     "1.0.0",
			Visibility:  "public",
		},
		Developer: DevBlock{Name: "John Doe", Organization: "Atlas AI Labs"},
		Games:     []string{"mafia", "goofspiel"},
		Endpoint:  EndpointBlock{URL: "https://agent.example.com/play", Authentication: "bearer-token"},
		Runtime:   RuntimeBlock{Timeout: 5000, MaxMemory: "512MB"},
		Model:     &ModelBlock{Provider: "OpenAI", Model: "GPT-5.5", Reasoning: true},
		SDK:       SDKBlock{Language: "TypeScript", Version: "1.0.0"},
		Contact:   ContactBlock{Email: "developer@example.com"},
	}
}

func TestValidate_Valid(t *testing.T) {
	if errs := Validate(validDoc()); len(errs) != 0 {
		t.Fatalf("expected valid manifest, got errors: %v", errs)
	}
}

func TestValidate_OptionalModelOmitted(t *testing.T) {
	d := validDoc()
	d.Model = nil
	if errs := Validate(d); len(errs) != 0 {
		t.Fatalf("model is optional; expected no errors, got: %v", errs)
	}
}

func TestValidate_Failures(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Document)
		field  string
	}{
		{"bad spec version", func(d *Document) { d.ManifestVersion = "9.9" }, "manifestVersion"},
		{"short name", func(d *Document) { d.Agent.Name = "ab" }, "agent.name"},
		{"bad semver", func(d *Document) { d.Agent.Version = "v1" }, "agent.version"},
		{"bad visibility", func(d *Document) { d.Agent.Visibility = "secret" }, "agent.visibility"},
		{"no developer", func(d *Document) { d.Developer.Name = "" }, "developer.name"},
		{"no games", func(d *Document) { d.Games = nil }, "games"},
		{"unknown game", func(d *Document) { d.Games = []string{"chess"} }, "games[0]"},
		// NOTE: "no endpoint" is deliberately NOT here any more. An empty endpoint is
		// now valid and means connected-ranked — the agent plays over its socket and
		// declares no hosted URL. See TestManifestWithNoEndpointIsValid.
		{"http endpoint", func(d *Document) { d.Endpoint.URL = "http://a.example.com" }, "endpoint.url"},
		{"bad auth", func(d *Document) { d.Endpoint.Authentication = "basic" }, "endpoint.authentication"},
		{"zero timeout", func(d *Document) { d.Runtime.Timeout = 0 }, "runtime.timeout"},
		{"model missing provider", func(d *Document) { d.Model = &ModelBlock{Model: "x"} }, "model.provider"},
		{"no sdk lang", func(d *Document) { d.SDK.Language = "" }, "sdk.language"},
		{"bad email", func(d *Document) { d.Contact.Email = "nope" }, "contact.email"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validDoc()
			tc.mutate(&d)
			errs := Validate(d)
			if !hasField(errs, tc.field) {
				t.Fatalf("expected a validation error on %q, got: %v", tc.field, errs)
			}
		})
	}
}

func TestValidate_AllowInsecureEndpoint(t *testing.T) {
	AllowInsecureEndpoint = true
	defer func() { AllowInsecureEndpoint = false }()
	d := validDoc()
	d.Endpoint.URL = "http://localhost:8080/play"
	if errs := Validate(d); len(errs) != 0 {
		t.Fatalf("http should be allowed when AllowInsecureEndpoint is set, got: %v", errs)
	}
}

func TestParse_JSON(t *testing.T) {
	raw := `{
		"manifestVersion":"1.0",
		"agent":{"name":"Atlas","description":"d","version":"1.0.0","visibility":"public"},
		"developer":{"name":"John","organization":"Labs"},
		"games":["mafia"],
		"endpoint":{"url":"https://a.example.com/play","authentication":"bearer-token"},
		"runtime":{"timeout":5000,"maxMemory":"512MB"},
		"sdk":{"language":"Go","version":"1.0.0"},
		"contact":{"email":"d@example.com"}
	}`
	d, err := Parse("application/json", []byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if d.Agent.Name != "Atlas" || len(d.Games) != 1 || d.Runtime.Timeout != 5000 {
		t.Fatalf("unexpected parse result: %+v", d)
	}
	if errs := Validate(d); len(errs) != 0 {
		t.Fatalf("parsed manifest should validate, got: %v", errs)
	}
}

func TestParse_UnknownFieldRejected(t *testing.T) {
	raw := `{"manifestVersion":"1.0","surprise":true}`
	if _, err := Parse("application/json", []byte(raw)); err == nil {
		t.Fatal("expected unknown-field rejection")
	}
}

func TestParse_YAML(t *testing.T) {
	// Mirrors the spec's example manifest (YAML).
	raw := `
manifestVersion: "1.0"
agent:
  name: Atlas
  description: Strategic AI Agent
  version: 1.0.0
  visibility: public
developer:
  name: John Doe
  organization: Atlas AI Labs
games:
  - mafia
  - goofspiel
endpoint:
  url: https://agent.example.com/play
  authentication: bearer-token
runtime:
  timeout: 5000
  maxMemory: 512MB
model:
  provider: OpenAI
  model: GPT-5.5
  reasoning: true
sdk:
  language: TypeScript
  version: 1.0.0
contact:
  email: developer@example.com
`
	d, err := Parse("application/yaml", []byte(raw))
	if err != nil {
		t.Fatalf("yaml parse: %v", err)
	}
	if d.Agent.Name != "Atlas" || len(d.Games) != 2 || d.Runtime.Timeout != 5000 {
		t.Fatalf("unexpected yaml parse: %+v", d)
	}
	if d.Model == nil || d.Model.Provider != "OpenAI" {
		t.Fatalf("model not parsed: %+v", d.Model)
	}
	if errs := Validate(d); len(errs) != 0 {
		t.Fatalf("spec yaml should validate, got: %v", errs)
	}
}

func TestParse_YAMLUnknownFieldRejected(t *testing.T) {
	raw := "manifestVersion: \"1.0\"\nsurprise: true\n"
	if _, err := Parse("text/yaml", []byte(raw)); err == nil {
		t.Fatal("expected unknown-field rejection in YAML")
	}
}

func TestDedupeGames(t *testing.T) {
	got := dedupeGames([]string{"mafia", "mafia", "goofspiel", "mafia"})
	if len(got) != 2 || got[0] != "mafia" || got[1] != "goofspiel" {
		t.Fatalf("dedupe failed: %v", got)
	}
}

func hasField(errs []FieldError, field string) bool {
	for _, e := range errs {
		if e.Field == field {
			return true
		}
	}
	return false
}

func TestFieldErrorString(t *testing.T) {
	fe := FieldError{Field: "agent.name", Message: "bad"}
	if !strings.Contains(fe.String(), "agent.name") {
		t.Fatalf("unexpected: %s", fe.String())
	}
}
