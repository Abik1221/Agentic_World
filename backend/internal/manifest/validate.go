package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/agent-arena/arena/internal/devplatform"
	"gopkg.in/yaml.v3"
)

// AllowInsecureEndpoint permits http:// (not just https://) endpoint URLs. The
// spec mandates HTTPS; this escape hatch exists only so local dev/e2e can point
// at a plaintext starter agent. main.go flips it from config; default is false.
var AllowInsecureEndpoint = false

// FieldError is one validation failure, keyed by the offending manifest field.
// The handler returns a list of these so a developer sees every problem at once.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e FieldError) String() string { return e.Field + ": " + e.Message }

var (
	// agentNameRe mirrors identity's public agent-name rule (3–32 chars; letters,
	// digits, _ or -). Kept local so this package does not import identity.
	agentNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{3,32}$`)
	// semverRe accepts MAJOR.MINOR.PATCH with an optional -prerelease / +build.
	semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)
	// emailRe is a pragmatic email check (not full RFC 5322).
	emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// allowedAuthTypes are the endpoint authentication schemes the platform supports.
var allowedAuthTypes = map[string]bool{"bearer-token": true}

// allowedVisibility are the valid agent visibilities.
var allowedVisibility = map[string]bool{"public": true, "private": true}

// supportedGames is the platform's canonical game vocabulary, sourced from the
// engine-agnostic devplatform layer so there is a single source of truth.
var supportedGames = map[string]bool{
	string(devplatform.GameGoofspiel): true,
	string(devplatform.GameMafia):     true,
	string(devplatform.GameMonopoly):  true,
}

// Parse decodes a submitted manifest document. contentType selects the codec:
// application/json (default) or YAML (the spec's example format). Both reject
// unknown fields so a typo'd key surfaces as an error rather than being silently
// dropped.
func Parse(contentType string, raw []byte) (Document, error) {
	switch normalizeContentType(contentType) {
	case "application/json", "":
		var d Document
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&d); err != nil {
			return Document{}, fmt.Errorf("invalid JSON manifest: %w", err)
		}
		return d, nil
	case "application/yaml", "text/yaml", "application/x-yaml", "text/x-yaml":
		var d Document
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true) // reject unknown keys, mirroring JSON strictness
		if err := dec.Decode(&d); err != nil {
			return Document{}, fmt.Errorf("invalid YAML manifest: %w", err)
		}
		return d, nil
	default:
		return Document{}, fmt.Errorf("unsupported content type %q; use application/json or application/yaml", contentType)
	}
}

func normalizeContentType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 { // strip "; charset=..."
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

// Validate returns every problem with a parsed manifest. An empty slice means
// the manifest is valid. Validation is pure (no I/O), so it is fully unit-tested.
func Validate(d Document) []FieldError {
	var errs []FieldError
	add := func(field, msg string) { errs = append(errs, FieldError{Field: field, Message: msg}) }

	if !supportedSpecVersions[d.ManifestVersion] {
		add("manifestVersion", fmt.Sprintf("unsupported manifest version %q (supported: %s)", d.ManifestVersion, SpecVersion))
	}

	// agent
	if !agentNameRe.MatchString(d.Agent.Name) {
		add("agent.name", "must be 3–32 characters: letters, digits, _ or -")
	}
	if !semverRe.MatchString(d.Agent.Version) {
		add("agent.version", "must be semantic version MAJOR.MINOR.PATCH (e.g. 1.0.0)")
	}
	if d.Agent.Visibility == "" {
		d.Agent.Visibility = "public" // tolerated default; not an error
	} else if !allowedVisibility[d.Agent.Visibility] {
		add("agent.visibility", "must be 'public' or 'private'")
	}

	// developer
	if strings.TrimSpace(d.Developer.Name) == "" {
		add("developer.name", "is required")
	}

	// games
	if len(d.Games) == 0 {
		add("games", "at least one supported game is required")
	}
	for i, g := range d.Games {
		if !supportedGames[g] {
			add(fmt.Sprintf("games[%d]", i), fmt.Sprintf("unknown game %q", g))
		}
	}

	// endpoint
	validateEndpoint(d.Endpoint, add)

	// runtime
	if d.Runtime.Timeout <= 0 {
		add("runtime.timeout", "must be a positive number of milliseconds")
	}

	// model — optional; if present, provider + model must be named.
	if d.Model != nil {
		if strings.TrimSpace(d.Model.Provider) == "" {
			add("model.provider", "is required when a model block is present")
		}
		if strings.TrimSpace(d.Model.Model) == "" {
			add("model.model", "is required when a model block is present")
		}
	}

	// sdk
	if strings.TrimSpace(d.SDK.Language) == "" {
		add("sdk.language", "is required")
	}

	// contact
	if !emailRe.MatchString(d.Contact.Email) {
		add("contact.email", "must be a valid email address")
	}

	return errs
}

// validateEndpoint checks a hosted endpoint IF one is declared.
//
// An empty endpoint is valid and means "connected ranked": the agent plays while its
// socket is held open, and the platform drives it there. seatFor already prefers the
// socket and only falls back to an endpoint when the socket is absent, so a connected
// agent never touches its endpoint — the requirement was policy, not necessity.
//
// Requiring one made every developer rent a server, write a Dockerfile and obtain TLS
// before their FIRST ranked match. That cliff is where people quit, and an arena with
// no agents in it has nothing to offer the ones who make it over. Declaring an
// endpoint is now an upgrade: it buys always-on play (auto_join) and lets a staked
// match continue when you are not connected.
func validateEndpoint(e EndpointBlock, add func(field, msg string)) {
	if strings.TrimSpace(e.URL) == "" {
		// No endpoint: connected-ranked. Nothing else in this block is meaningful.
		return
	}
	u, err := url.Parse(e.URL)
	if err != nil || u.Host == "" {
		add("endpoint.url", "must be a valid absolute URL")
		return
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		// ok
	case "http":
		if !AllowInsecureEndpoint {
			add("endpoint.url", "must use https")
		}
	default:
		add("endpoint.url", "must use https")
	}

	if !allowedAuthTypes[e.Authentication] {
		add("endpoint.authentication", "must be 'bearer-token'")
	}
}

// dedupeGames returns the games with duplicates removed, order preserved.
func dedupeGames(games []string) []string {
	seen := make(map[string]bool, len(games))
	out := make([]string, 0, len(games))
	for _, g := range games {
		if seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, g)
	}
	return out
}
