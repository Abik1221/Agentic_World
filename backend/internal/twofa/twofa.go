// Package twofa is the platform's free, self-hosted TOTP two-factor: enrollment
// (setup → confirm), status/disable, and a step-up Require() the money paths call
// before a withdrawal or a withdrawal-wallet change. The TOTP secret is stored
// ENCRYPTED at rest (secretbox); nothing is enforced until the user confirms a
// first code. No third-party service and no per-use cost.
package twofa

import (
	"context"
	"net/http"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/secretbox"
	"github.com/agent-arena/arena/internal/totp"
)

var (
	ErrAlreadyEnabled = httpx.NewError(http.StatusConflict, "totp_already_enabled", "Two-factor is already enabled; disable it first to re-enroll.")
	ErrNotEnrolled    = httpx.NewError(http.StatusBadRequest, "totp_not_enrolled", "Start two-factor setup first.")
	ErrInvalidCode    = httpx.NewError(http.StatusBadRequest, "totp_invalid_code", "That authenticator code is not valid.")
	// ErrCodeRequired is returned by Require when 2FA is enabled but the caller
	// supplied no code — the money path must collect one and retry.
	ErrCodeRequired = httpx.NewError(http.StatusUnauthorized, "totp_required", "Enter your authenticator code to continue.")
)

// Repo persists the per-user TOTP secret + enabled flag.
type Repo interface {
	SaveSecret(ctx context.Context, userPublicID string, secretEnc []byte) error // stores secret, enabled=false
	Load(ctx context.Context, userPublicID string) (secretEnc []byte, enabled bool, err error)
	SetEnabled(ctx context.Context, userPublicID string, at time.Time) error
	Clear(ctx context.Context, userPublicID string) error
}

// Service issues + verifies TOTP enrollments and performs step-up checks.
type Service struct {
	repo   Repo
	enc    *secretbox.Cipher
	clock  platform.Clock
	issuer string
	skew   int
}

func New(repo Repo, enc *secretbox.Cipher, clock platform.Clock, issuer string) *Service {
	if issuer == "" {
		issuer = "pyyol"
	}
	return &Service{repo: repo, enc: enc, clock: clock, issuer: issuer, skew: 1}
}

// Setup starts (or restarts) enrollment: it generates a fresh secret, stores it
// encrypted as NOT-yet-enabled, and returns the secret + otpauth URI for the QR.
// Refuses if 2FA is already fully enabled (disable first).
func (s *Service) Setup(ctx context.Context, userPublicID, account string) (secret, uri string, err error) {
	_, enabled, err := s.repo.Load(ctx, userPublicID)
	if err != nil {
		return "", "", err
	}
	if enabled {
		return "", "", ErrAlreadyEnabled
	}
	secret, err = totp.GenerateSecret()
	if err != nil {
		return "", "", err
	}
	enc, err := s.enc.Seal([]byte(secret))
	if err != nil {
		return "", "", err
	}
	if err := s.repo.SaveSecret(ctx, userPublicID, enc); err != nil {
		return "", "", err
	}
	return secret, totp.URI(secret, s.issuer, account), nil
}

// Confirm verifies the first code against the pending secret and enables 2FA.
func (s *Service) Confirm(ctx context.Context, userPublicID, code string) error {
	secret, _, err := s.loadSecret(ctx, userPublicID)
	if err != nil {
		return err
	}
	if !totp.Validate(secret, code, s.clock.Now(), s.skew) {
		return ErrInvalidCode
	}
	return s.repo.SetEnabled(ctx, userPublicID, s.clock.Now())
}

// Enabled reports whether the user has 2FA fully enabled.
func (s *Service) Enabled(ctx context.Context, userPublicID string) (bool, error) {
	_, enabled, err := s.repo.Load(ctx, userPublicID)
	return enabled, err
}

// Require is the step-up gate for money paths: a no-op when 2FA is disabled; when
// enabled it demands a valid code (ErrCodeRequired if empty, ErrInvalidCode if wrong).
func (s *Service) Require(ctx context.Context, userPublicID, code string) error {
	secret, enabled, err := s.loadSecret(ctx, userPublicID)
	if err != nil {
		if err == ErrNotEnrolled {
			return nil // never enrolled ⇒ 2FA not in force
		}
		return err
	}
	if !enabled {
		return nil
	}
	if code == "" {
		return ErrCodeRequired
	}
	if !totp.Validate(secret, code, s.clock.Now(), s.skew) {
		return ErrInvalidCode
	}
	return nil
}

// Disable turns 2FA off after verifying a current code (so a hijacked session
// can't silently strip the second factor).
func (s *Service) Disable(ctx context.Context, userPublicID, code string) error {
	secret, enabled, err := s.loadSecret(ctx, userPublicID)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrNotEnrolled
	}
	if !totp.Validate(secret, code, s.clock.Now(), s.skew) {
		return ErrInvalidCode
	}
	return s.repo.Clear(ctx, userPublicID)
}

// loadSecret decrypts the stored secret; ErrNotEnrolled when none is set.
func (s *Service) loadSecret(ctx context.Context, userPublicID string) (secret string, enabled bool, err error) {
	enc, enabled, err := s.repo.Load(ctx, userPublicID)
	if err != nil {
		return "", false, err
	}
	if len(enc) == 0 {
		return "", false, ErrNotEnrolled
	}
	pt, err := s.enc.Open(enc)
	if err != nil {
		return "", false, err
	}
	return string(pt), enabled, nil
}
