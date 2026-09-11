package manifest

import (
	"context"
	"github.com/agent-arena/arena/internal/platform/telemetry"
	"net/http"
	"strings"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/httpx"
)

// EndpointProbe is the outbound-HTTP port used to verify a developer endpoint.
// internal/agentclient.Client satisfies it; tests use a fake.
type EndpointProbe interface {
	Health(ctx context.Context, t agentclient.Target) (agentclient.HealthResult, error)
	Handshake(ctx context.Context, t agentclient.Target) (agentclient.HandshakeResult, error)
}

// Sealer is the encryption port for the endpoint bearer token at rest.
// internal/secretbox.Cipher satisfies it.
type Sealer interface {
	Seal(plaintext []byte) ([]byte, error)
	Open(ciphertext []byte) ([]byte, error)
}

// VerificationReport is the developer-facing result of an endpoint verification.
type VerificationReport struct {
	ManifestID     string   `json:"manifest_id"`
	Verified       bool     `json:"verified"`
	HealthOK       bool     `json:"health_ok"`
	HealthLatency  int      `json:"health_latency_ms"`
	HandshakeOK    bool     `json:"handshake_ok"`
	SDKVersion     string   `json:"handshake_sdk_version,omitempty"`
	SupportedGames []string `json:"handshake_supported_games,omitempty"`
	HandshakeAgent string   `json:"handshake_agent_id,omitempty"`
	IdentityOK     bool     `json:"identity_ok"`
	GamesCovered   bool     `json:"games_covered"`
	Reason         string   `json:"reason,omitempty"`
}

var (
	// ErrVerificationUnavailable is returned when the platform is not configured
	// to perform endpoint verification (no probe / no sealer wired).
	ErrVerificationUnavailable = httpx.NewError(http.StatusServiceUnavailable, "verification_unavailable",
		"Endpoint verification is not enabled on this platform.")
	// ErrEndpointSecretRequired is returned when a bearer-token endpoint has no
	// stored secret yet.
	ErrEndpointSecretRequired = httpx.NewError(http.StatusBadRequest, "endpoint_secret_required",
		"Set the endpoint bearer secret before verifying (POST .../endpoint-secret).")
)

// SetEndpointSecret stores (encrypted) the bearer token the platform will present
// to the agent's endpoint. The plaintext is never persisted or returned.
func (s *Service) SetEndpointSecret(ctx context.Context, ownerPublicID, agentPublicID, manifestPublicID, token string) error {
	if s.sealer == nil {
		return ErrVerificationUnavailable
	}
	if token == "" {
		return httpx.NewError(http.StatusBadRequest, "invalid_request", "token is required")
	}
	if err := s.assertOwned(ctx, agentPublicID, ownerPublicID); err != nil {
		return err
	}
	if _, found, err := s.repo.GetManifest(ctx, agentPublicID, manifestPublicID); err != nil {
		return err
	} else if !found {
		return ErrNoManifest
	}
	sealed, err := s.sealer.Seal([]byte(token))
	if err != nil {
		return err
	}
	return s.repo.SetEndpointToken(ctx, agentPublicID, manifestPublicID, sealed)
}

// Verify runs the endpoint verification flow for a manifest the caller owns:
// GET /health, then POST /handshake, then a games cross-check (the endpoint's
// advertised supportedGames must cover every game the manifest declares). On
// full success the manifest is marked verified and becomes the agent's active
// manifest. Every attempt (success or failure) is recorded for audit.
func (s *Service) Verify(ctx context.Context, ownerPublicID, agentPublicID, manifestPublicID string) (report VerificationReport, err error) {
	// Emit the OUTCOME once, whichever of the four exit paths is taken.
	//
	// A FAILED verification previously produced no telemetry at all — only a DB audit
	// row — so a developer whose endpoint never passed had nothing to look at, and an
	// operator could not see that verifications were failing in aggregate. A deferred
	// emit also means a future early-return cannot silently skip it.
	defer func() {
		if s.tracer == nil || err != nil {
			return // a hard error (not a verification verdict) is surfaced to the caller
		}
		evType := telemetry.EventAgentEndpointFailed
		if report.Verified {
			evType = telemetry.EventAgentEndpointVerified
		}
		s.tracer.EmitAgentLifecycle(evType, telemetry.LifecycleEvent{
			AgentID: agentPublicID,
			Reason:  report.Reason,
			Detail: map[string]any{
				"manifest":        manifestPublicID,
				"health_ok":       report.HealthOK,
				"handshake_ok":    report.HandshakeOK,
				"games_covered":   report.GamesCovered,
				"health_latency":  report.HealthLatency,
				"sdk_version":     report.SDKVersion,
				"supported_games": report.SupportedGames,
			},
		})
	}()

	if s.probe == nil {
		return VerificationReport{}, ErrVerificationUnavailable
	}
	if err := s.assertOwned(ctx, agentPublicID, ownerPublicID); err != nil {
		return VerificationReport{}, err
	}
	m, found, err := s.repo.GetManifest(ctx, agentPublicID, manifestPublicID)
	if err != nil {
		return VerificationReport{}, err
	}
	if !found {
		return VerificationReport{}, ErrNoManifest
	}

	// CONNECTED-RANKED: a manifest that declares no endpoint is certified without a probe.
	//
	// There is nothing to probe. Falling through sent an empty URL to the client, which
	// answered "invalid endpoint url" — so a developer following the SDK's own scaffold
	// (which omits the endpoint and prints "certifies you; no endpoint required") could
	// never get certified, and ranked play was impossible without hosting.
	//
	// Every other part of the platform already handles this shape: PlayTarget returns "no
	// target" and lets the socket drive the seat, and match entry requires the agent to be
	// CONNECTED RIGHT NOW. This gate was the only one that did not, and it is the one that
	// decides whether the account may play at all.
	//
	// Skipping the probe loses nothing. A probe proves an endpoint answered at publish
	// time, which says nothing about whether it answers during a match an hour later — for
	// a connected agent that guarantee is enforced at match entry instead, where it is
	// actually load-bearing. The attempt is still recorded, so the audit trail shows a
	// certification happened and on what basis.
	if strings.TrimSpace(m.EndpointURL) == "" {
		report = VerificationReport{
			ManifestID:   manifestPublicID,
			Verified:     true,
			IdentityOK:   true, // JWT ownership is the proof; there is no host to impersonate
			GamesCovered: true,
			Reason:       "connected-ranked: no endpoint declared, the agent plays over its socket",
		}
		attempt := VerificationAttempt{
			ManifestPublicID: manifestPublicID,
			HandshakeGames:   m.Games,
		}
		if err := s.repo.RecordVerification(ctx, attempt); err != nil {
			return report, err
		}
		if err := s.repo.MarkVerifiedAndActivate(ctx, agentPublicID, manifestPublicID); err != nil {
			return report, err
		}
		return report, nil
	}

	// Resolve the bearer token (required for bearer-token endpoints).
	token, err := s.resolveToken(ctx, m)
	if err != nil {
		return VerificationReport{}, err
	}

	report = VerificationReport{ManifestID: manifestPublicID}
	target := agentclient.Target{EndpointURL: m.EndpointURL, Token: token, AgentID: agentPublicID}
	attempt := VerificationAttempt{ManifestPublicID: manifestPublicID}

	// 1. Health.
	health, herr := s.probe.Health(ctx, target)
	report.HealthOK = health.OK
	report.HealthLatency = health.LatencyMs
	attempt.HealthOK = health.OK
	attempt.HealthLatencyMs = health.LatencyMs
	if herr != nil || !health.OK {
		report.Reason = firstNonEmpty(health.Err, errString(herr), "health check failed")
		attempt.Error = report.Reason
		_ = s.repo.RecordVerification(ctx, attempt)
		return report, nil
	}
	if claimed := strings.TrimSpace(health.Body.Agent); looksLikeAgentPublicID(claimed) && !sameAgentID(claimed, agentPublicID) {
		report.Reason = "endpoint health identified as a different agent"
		attempt.Error = report.Reason
		_ = s.repo.RecordVerification(ctx, attempt)
		return report, nil
	}

	// 2. Handshake.
	hs, hserr := s.probe.Handshake(ctx, target)
	report.HandshakeOK = hs.OK
	report.SDKVersion = hs.SDKVersion
	report.SupportedGames = hs.SupportedGames
	report.HandshakeAgent = hs.AgentID
	attempt.HandshakeOK = hs.OK
	attempt.HandshakeSDKVersion = hs.SDKVersion
	attempt.HandshakeGames = hs.SupportedGames
	if hserr != nil || !hs.OK {
		report.Reason = firstNonEmpty(hs.Err, errString(hserr), "handshake was not accepted")
		attempt.Error = report.Reason
		_ = s.repo.RecordVerification(ctx, attempt)
		return report, nil
	}
	if hs.AgentID != "" && !sameAgentID(hs.AgentID, agentPublicID) {
		report.HandshakeOK = false
		report.Reason = "endpoint identified as a different agent"
		attempt.Error = report.Reason
		_ = s.repo.RecordVerification(ctx, attempt)
		return report, nil
	}
	report.IdentityOK = hs.AgentID == "" || sameAgentID(hs.AgentID, agentPublicID)

	// 3. Games cross-check: the endpoint must support every declared game.
	missing := missingGames(m.Games, hs.SupportedGames)
	report.GamesCovered = len(missing) == 0
	if !report.GamesCovered {
		report.Reason = "endpoint does not support declared game(s): " + joinComma(missing)
		attempt.Error = report.Reason
		_ = s.repo.RecordVerification(ctx, attempt)
		return report, nil
	}

	// Success: record, mark verified, and activate.
	if err := s.repo.RecordVerification(ctx, attempt); err != nil {
		return report, err
	}
	if err := s.repo.MarkVerifiedAndActivate(ctx, agentPublicID, manifestPublicID); err != nil {
		return report, err
	}
	report.Verified = true
	return report, nil
}

// resolveToken decrypts the stored endpoint token. A bearer-token endpoint with
// no stored secret is a hard error; other auth types tolerate an empty token.
func (s *Service) resolveToken(ctx context.Context, m Manifest) (string, error) {
	enc, found, err := s.repo.EndpointToken(ctx, m.PublicID)
	if err != nil {
		return "", err
	}
	if !found {
		if m.AuthType == "bearer-token" {
			return "", ErrEndpointSecretRequired
		}
		return "", nil
	}
	if s.sealer == nil {
		return "", ErrVerificationUnavailable
	}
	plain, err := s.sealer.Open(enc)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// PlayTarget returns the push-play call target (endpoint URL + resolved bearer
// token) for an agent's ACTIVE, endpoint-verified manifest. found is false when
// the agent has no active manifest, so callers can require registration before
// driving a remote match. It never returns the sealed token — only the plaintext
// the hardened agentclient needs to authenticate the POST.
func (s *Service) PlayTarget(ctx context.Context, agentPublicID string) (agentclient.Target, bool, error) {
	m, found, err := s.repo.ActiveManifest(ctx, agentPublicID)
	if err != nil || !found {
		return agentclient.Target{}, false, err
	}
	// A connected-ranked agent declares no endpoint, so there is nothing to push to.
	// Reporting ok here would hand the driver a seat pointing at "" — it would fail
	// every turn and the engine would play fallbacks for a match the agent could have
	// played perfectly well over its socket. Say "no target" and let seatFor use the
	// socket, which is what it prefers anyway.
	if strings.TrimSpace(m.EndpointURL) == "" {
		return agentclient.Target{}, false, nil
	}
	token, err := s.resolveToken(ctx, m)
	if err != nil {
		return agentclient.Target{}, false, err
	}
	return agentclient.Target{EndpointURL: m.EndpointURL, Token: token}, true, nil
}

// ActiveAgentIDs returns every agent with an active, verified manifest. The
// webhook health monitor uses it to enumerate endpoints to probe.
func (s *Service) ActiveAgentIDs(ctx context.Context) ([]string, error) {
	return s.repo.ActiveAgentIDs(ctx)
}

// VerificationAttempt is one recorded health+handshake attempt (audit trail).
type VerificationAttempt struct {
	ManifestPublicID    string
	HealthOK            bool
	HealthLatencyMs     int
	HandshakeOK         bool
	HandshakeSDKVersion string
	HandshakeGames      []string
	Error               string
}

// --- small helpers ---------------------------------------------------------

// missingGames returns declared games not present in supported.
func missingGames(declared, supported []string) []string {
	have := make(map[string]bool, len(supported))
	for _, g := range supported {
		have[g] = true
	}
	var missing []string
	for _, g := range declared {
		if !have[g] {
			missing = append(missing, g)
		}
	}
	return missing
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func joinComma(vals []string) string {
	out := ""
	for i, v := range vals {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

func looksLikeAgentPublicID(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(s, "ag_") || strings.HasPrefix(s, "agt_")
}

func sameAgentID(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
