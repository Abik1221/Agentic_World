// Package manifest implements the Agent Manifest contract: the metadata document
// a developer submits during registration to describe their agent (info,
// supported games, the developer-hosted endpoint, runtime hints, optional model
// info, SDK version, contact). It carries NO source code — developers host their
// own AI and the platform communicates with it over a standardized HTTP API.
//
// See docs/agent-manifest-plan.md. This package owns parsing, validation, and the
// persistence port (Repo); the pgx implementation lives in internal/store.
//
// M1 scope: submit + validate + store + read. Endpoint verification (GET /health,
// POST /handshake) and the outbound agentclient land in M2.
package manifest

import "time"

// SpecVersion is the manifest-format version this platform speaks. Submitting a
// manifest whose manifestVersion is not in supportedSpecVersions is rejected.
const SpecVersion = "1.0"

var supportedSpecVersions = map[string]bool{"1.0": true}

// Document is the wire form of a submitted manifest — the shape a developer
// writes (mirrors the spec's YAML example). Tags cover JSON now; the same struct
// is YAML-ready (a yaml decoder can populate it) once that dependency is added.
type Document struct {
	ManifestVersion string        `json:"manifestVersion" yaml:"manifestVersion"`
	Agent           AgentBlock    `json:"agent" yaml:"agent"`
	Developer       DevBlock      `json:"developer" yaml:"developer"`
	Games           []string      `json:"games" yaml:"games"`
	Endpoint        EndpointBlock `json:"endpoint" yaml:"endpoint"`
	Runtime         RuntimeBlock  `json:"runtime" yaml:"runtime"`
	Model           *ModelBlock   `json:"model,omitempty" yaml:"model,omitempty"` // optional
	SDK             SDKBlock      `json:"sdk" yaml:"sdk"`
	Contact         ContactBlock  `json:"contact" yaml:"contact"`
}

// AgentBlock is the "agent:" section. Any client-supplied id is ignored — the
// platform assigns the public id (agent.id: auto-generated in the spec).
type AgentBlock struct {
	ID          string `json:"id,omitempty" yaml:"id,omitempty"` // ignored on input
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
	Version     string `json:"version" yaml:"version"` // developer semver
	Visibility  string `json:"visibility" yaml:"visibility"`
}

// DevBlock is the "developer:" section.
type DevBlock struct {
	Name         string `json:"name" yaml:"name"`
	Organization string `json:"organization" yaml:"organization"`
}

// EndpointBlock is the "endpoint:" section describing the developer-hosted API.
type EndpointBlock struct {
	URL            string `json:"url" yaml:"url"`
	Authentication string `json:"authentication" yaml:"authentication"`
}

// RuntimeBlock is the "runtime:" section (per-move hints).
type RuntimeBlock struct {
	Timeout   int    `json:"timeout" yaml:"timeout"` // milliseconds
	MaxMemory string `json:"maxMemory" yaml:"maxMemory"`
}

// ModelBlock is the optional "model:" section. Purely informational and
// developer-declared — the platform cannot verify a remote API's model.
type ModelBlock struct {
	Provider  string `json:"provider" yaml:"provider"`
	Model     string `json:"model" yaml:"model"`
	Reasoning bool   `json:"reasoning" yaml:"reasoning"`
}

// SDKBlock is the "sdk:" section.
type SDKBlock struct {
	Language string `json:"language" yaml:"language"`
	Version  string `json:"version" yaml:"version"`
}

// ContactBlock is the "contact:" section.
type ContactBlock struct {
	Email string `json:"email" yaml:"email"`
}

// Manifest is the flattened, persisted domain form of a validated manifest. It
// is what the Repo stores and returns and what the API surfaces.
type Manifest struct {
	PublicID      string
	AgentPublicID string

	ManifestVersion string
	AgentVersion    string

	Name        string
	Description string
	Visibility  string

	DeveloperName string
	Organization  string

	Games []string

	EndpointURL string
	AuthType    string

	RuntimeTimeoutMs int
	RuntimeMaxMemory string

	Model *ModelBlock // nil when the developer omitted it

	SDKLanguage  string
	SDKVersion   string
	ContactEmail string

	Status    string
	CreatedAt time.Time
}

// Statuses in the manifest lifecycle.
const (
	StatusSubmitted = "submitted"
	StatusValidated = "validated"
	StatusVerified  = "verified"
	StatusRejected  = "rejected"
)

// toDomain flattens a validated Document into a Manifest, attaching identifiers
// the caller assigns (public ids come from the service, not the client).
func (d Document) toDomain(publicID, agentPublicID string) Manifest {
	m := Manifest{
		PublicID:         publicID,
		AgentPublicID:    agentPublicID,
		ManifestVersion:  d.ManifestVersion,
		AgentVersion:     d.Agent.Version,
		Name:             d.Agent.Name,
		Description:      d.Agent.Description,
		Visibility:       d.Agent.Visibility,
		DeveloperName:    d.Developer.Name,
		Organization:     d.Developer.Organization,
		Games:            dedupeGames(d.Games),
		EndpointURL:      d.Endpoint.URL,
		AuthType:         d.Endpoint.Authentication,
		RuntimeTimeoutMs: d.Runtime.Timeout,
		RuntimeMaxMemory: d.Runtime.MaxMemory,
		Model:            d.Model,
		SDKLanguage:      d.SDK.Language,
		SDKVersion:       d.SDK.Version,
		ContactEmail:     d.Contact.Email,
		Status:           StatusValidated,
	}
	return m
}
