package manifest

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/platform"
)

// Repo is manifest's persistence port. The pgx implementation lives in
// internal/store (the only package that touches the DB driver).
type Repo interface {
	// AgentOwned reports whether agentPublicID exists and is owned by ownerPublicID.
	AgentOwned(ctx context.Context, agentPublicID, ownerPublicID string) (bool, error)

	// InsertManifest persists a new immutable manifest version plus its games in
	// one transaction. It returns ErrVersionExists if (agent, agent_version) was
	// already submitted. rawDoc is the exact bytes the developer sent; normalized
	// is the validated JSON form.
	InsertManifest(ctx context.Context, m Manifest, rawDoc string, normalized []byte) error

	// ActiveManifest returns the agent's active (endpoint-verified) manifest.
	// found is false when the agent has no active manifest yet.
	ActiveManifest(ctx context.Context, agentPublicID string) (m Manifest, found bool, err error)

	// ActiveAgentIDs returns the public id of every agent with an active manifest.
	ActiveAgentIDs(ctx context.Context) ([]string, error)

	// LatestManifest returns the most recently submitted manifest (any status),
	// used to surface a just-submitted, not-yet-verified manifest.
	LatestManifest(ctx context.Context, agentPublicID string) (m Manifest, found bool, err error)

	// ListVersions returns all submitted manifest versions, newest first.
	ListVersions(ctx context.Context, agentPublicID string) ([]Manifest, error)

	// GetManifest returns a specific manifest version owned by the agent.
	GetManifest(ctx context.Context, agentPublicID, manifestPublicID string) (m Manifest, found bool, err error)

	// SetEndpointToken stores the sealed (encrypted) endpoint bearer token for a
	// manifest the agent owns.
	SetEndpointToken(ctx context.Context, agentPublicID, manifestPublicID string, sealed []byte) error

	// EndpointToken returns the sealed endpoint token for a manifest, if set.
	EndpointToken(ctx context.Context, manifestPublicID string) (sealed []byte, found bool, err error)

	// RecordVerification appends one endpoint-verification attempt (audit trail).
	RecordVerification(ctx context.Context, a VerificationAttempt) error

	// MarkVerifiedAndActivate marks a manifest verified and sets it as the agent's
	// active manifest, atomically.
	MarkVerifiedAndActivate(ctx context.Context, agentPublicID, manifestPublicID string) error
}

// Service is the manifest application service. probe and sealer are optional:
// when nil, endpoint verification is disabled (metadata-only operation).
type Service struct {
	repo   Repo
	probe  EndpointProbe
	sealer Sealer
}

// New builds the service. Pass a non-nil probe and sealer to enable endpoint
// verification (M2); pass nil for both for metadata-only operation.
func New(repo Repo, probe EndpointProbe, sealer Sealer) *Service {
	return &Service{repo: repo, probe: probe, sealer: sealer}
}

// Domain errors, expressed as *httpx.APIError so handlers return them directly.
var (
	ErrForbiddenOwner = httpx.NewError(http.StatusForbidden, "forbidden", "You do not own this agent.")
	ErrVersionExists  = httpx.NewError(http.StatusConflict, "manifest_version_exists", "That agent version already has a manifest; bump agent.version to submit a new one.")
	ErrNoManifest     = httpx.NewError(http.StatusNotFound, "manifest_not_found", "This agent has no manifest yet.")
	ErrNotCertified   = httpx.NewError(http.StatusForbidden, "agent_not_certified", "Verify your agent's endpoint before entering ranked play.")
)

// RequireCertified is the ranked-entry gate: it returns nil when the agent has an
// active, endpoint-verified manifest (its certification), else ErrNotCertified.
// Sandbox/practice paths do not call this, so a developer can always practice
// before certifying.
func (s *Service) RequireCertified(ctx context.Context, agentPublicID string) error {
	m, found, err := s.repo.ActiveManifest(ctx, agentPublicID)
	if err != nil {
		return err
	}
	if !found || m.Status != StatusVerified {
		return ErrNotCertified
	}
	return nil
}

// invalidManifest builds a 400 carrying the field-level validation errors. This
// is the spec's "detailed validation report".
func invalidManifest(fieldErrs []FieldError) *httpx.APIError {
	details := make([]map[string]string, len(fieldErrs))
	for i, fe := range fieldErrs {
		details[i] = map[string]string{"field": fe.Field, "message": fe.Message}
	}
	return httpx.NewError(http.StatusBadRequest, "manifest_invalid", "The manifest failed validation.").
		WithDetails(map[string]any{"errors": details})
}

// Submit validates and persists a new manifest version for an agent the caller
// owns. It returns the stored Manifest, or a 400 with per-field errors, a 403 if
// the caller does not own the agent, or a 409 if that agent_version already
// exists. The manifest is stored as `validated` — endpoint verification (M2)
// promotes it to `verified` and makes it active.
func (s *Service) Submit(ctx context.Context, ownerPublicID, agentPublicID, contentType string, raw []byte) (Manifest, error) {
	owned, err := s.repo.AgentOwned(ctx, agentPublicID, ownerPublicID)
	if err != nil {
		return Manifest{}, err
	}
	if !owned {
		return Manifest{}, ErrForbiddenOwner
	}

	doc, err := Parse(contentType, raw)
	if err != nil {
		return Manifest{}, httpx.NewError(http.StatusBadRequest, "manifest_parse_error", err.Error())
	}
	if fieldErrs := Validate(doc); len(fieldErrs) > 0 {
		return Manifest{}, invalidManifest(fieldErrs)
	}

	m := doc.toDomain(platform.NewID(platform.PrefixManifest), agentPublicID)

	normalized, err := json.Marshal(normalizedDoc(doc, m))
	if err != nil {
		return Manifest{}, err
	}

	if err := s.repo.InsertManifest(ctx, m, string(raw), normalized); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Active returns the agent's active manifest (403 if not owned, 404 if none).
func (s *Service) Active(ctx context.Context, ownerPublicID, agentPublicID string) (Manifest, error) {
	if err := s.assertOwned(ctx, agentPublicID, ownerPublicID); err != nil {
		return Manifest{}, err
	}
	m, found, err := s.repo.ActiveManifest(ctx, agentPublicID)
	if err != nil {
		return Manifest{}, err
	}
	if !found {
		// Fall back to the latest submitted (not-yet-verified) manifest so a
		// developer can see what they just uploaded before verification lands.
		m, found, err = s.repo.LatestManifest(ctx, agentPublicID)
		if err != nil {
			return Manifest{}, err
		}
		if !found {
			return Manifest{}, ErrNoManifest
		}
	}
	return m, nil
}

// PublicActive returns an agent's active (verified) manifest for public display
// — no ownership check. Only public-visibility manifests are exposed; a private
// or absent manifest is a 404. Callers must render it with publicView (which
// omits the endpoint URL and contact email).
func (s *Service) PublicActive(ctx context.Context, agentPublicID string) (Manifest, error) {
	m, found, err := s.repo.ActiveManifest(ctx, agentPublicID)
	if err != nil {
		return Manifest{}, err
	}
	if !found || m.Visibility == "private" {
		return Manifest{}, ErrNoManifest
	}
	return m, nil
}

// Versions returns all submitted manifest versions for an owned agent, newest
// first.
func (s *Service) Versions(ctx context.Context, ownerPublicID, agentPublicID string) ([]Manifest, error) {
	if err := s.assertOwned(ctx, agentPublicID, ownerPublicID); err != nil {
		return nil, err
	}
	return s.repo.ListVersions(ctx, agentPublicID)
}

func (s *Service) assertOwned(ctx context.Context, agentPublicID, ownerPublicID string) error {
	owned, err := s.repo.AgentOwned(ctx, agentPublicID, ownerPublicID)
	if err != nil {
		return err
	}
	if !owned {
		return ErrForbiddenOwner
	}
	return nil
}

// normalizedDoc returns the cleaned Document persisted as JSONB: the client id is
// stripped, visibility is defaulted, and games are deduped.
func normalizedDoc(d Document, m Manifest) Document {
	d.Agent.ID = "" // platform-assigned; never persist a client value
	if strings.TrimSpace(d.Agent.Visibility) == "" {
		d.Agent.Visibility = "public"
	}
	d.Games = m.Games // deduped
	return d
}
