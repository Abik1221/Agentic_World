// Package twofa is the platform's free, self-hosted TOTP two-factor: enrollment
// (setup → confirm), status/disable, and a step-up Require() the money paths call
// before a withdrawal or a withdrawal-wallet change. The TOTP secret is stored
// ENCRYPTED at rest (secretbox); nothing is enforced until the user confirms a
// first code. No third-party service and no per-use cost.
package twofa

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/secretbox"
	"github.com/agent-arena/arena/internal/totp"
)

const recoveryCodeCount = 10

var (
	ErrAlreadyEnabled = httpx.NewError(http.StatusConflict, "totp_already_enabled", "Two-factor is already enabled; disable it first to re-enroll.")
	ErrNotEnrolled    = httpx.NewError(http.StatusBadRequest, "totp_not_enrolled", "Start two-factor setup first.")
	ErrInvalidCode    = httpx.NewError(http.StatusBadRequest, "totp_invalid_code", "That authenticator code is not valid.")
	// ErrCodeRequired is returned by Require when 2FA is enabled but the caller
	// supplied no code — the money path must collect one and retry.
	ErrCodeRequired = httpx.NewError(http.StatusUnauthorized, "totp_required", "Enter your authenticator code to continue.")
)

// Repo persists the per-user TOTP secret + enabled flag + recovery-code hashes.
type Repo interface {
	SaveSecret(ctx context.Context, userPublicID string, secretEnc []byte) error // stores secret, enabled=false
	Load(ctx context.Context, userPublicID string) (secretEnc []byte, enabled bool, err error)
	SetEnabled(ctx context.Context, userPublicID string, at time.Time) error
	Clear(ctx context.Context, userPublicID string) error // clears secret, enabled, AND recovery codes
	// SetRecoveryHashes replaces the stored recovery-code hashes (regenerate/enroll).
	SetRecoveryHashes(ctx context.Context, userPublicID string, hashes []string) error
	// ConsumeRecoveryHash atomically removes one unused hash, returning true iff it
	// was present (one-time use, race-safe).
	ConsumeRecoveryHash(ctx context.Context, userPublicID, hash string) (bool, error)
	// RecoveryRemaining is the count of unused recovery codes.
	RecoveryRemaining(ctx context.Context, userPublicID string) (int, error)
}

// Service issues + verifies TOTP enrollments and performs step-up checks.
type Service struct {
	repo      Repo
	enc       *secretbox.Cipher
	clock     platform.Clock
	issuer    string
	skew      int
	recPepper string // HMAC key for recovery-code hashes (server secret)
}

func New(repo Repo, enc *secretbox.Cipher, clock platform.Clock, issuer, recoveryPepper string) *Service {
	if issuer == "" {
		issuer = "Pyyol"
	}
	return &Service{repo: repo, enc: enc, clock: clock, issuer: issuer, skew: 1, recPepper: recoveryPepper}
}

// PickAccountLabel chooses the authenticator-app account name shown next to the
// issuer. Prefer the public username, then email, then the opaque public id as a
// last resort so enrollment never invents a label the account does not own.
func PickAccountLabel(username, email, userPublicID string) string {
	if u := strings.TrimPrefix(strings.TrimSpace(username), "@"); u != "" {
		return u
	}
	if e := strings.TrimSpace(email); e != "" {
		return e
	}
	return strings.TrimSpace(userPublicID)
}

var recB32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// generateRecoveryCodes returns fresh human-friendly one-time codes plus their
// HMAC hashes (only the hashes are stored). Codes are high-entropy, so a keyed
// hash — not bcrypt — is the right primitive.
func (s *Service) generateRecoveryCodes() (codes, hashes []string, err error) {
	for i := 0; i < recoveryCodeCount; i++ {
		raw := make([]byte, 10)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		// e.g. "K5T2A-9QW7C" — base32, grouped for readability.
		enc := recB32.EncodeToString(raw)
		code := enc[:5] + "-" + enc[5:10]
		codes = append(codes, code)
		hashes = append(hashes, s.hashRecovery(code))
	}
	return codes, hashes, nil
}

// hashRecovery is a deterministic keyed hash so a submitted code can be matched
// against the stored set without ever storing plaintext.
func (s *Service) hashRecovery(code string) string {
	norm := strings.ToUpper(strings.TrimSpace(code))
	mac := hmac.New(sha256.New, []byte(s.recPepper))
	mac.Write([]byte(norm))
	return hex.EncodeToString(mac.Sum(nil))
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

// Confirm verifies the first code against the pending secret, enables 2FA, and
// returns a fresh set of one-time RECOVERY CODES (shown exactly once). Storing only
// their hashes, we can never show them again — the client must surface them now.
func (s *Service) Confirm(ctx context.Context, userPublicID, code string) ([]string, error) {
	secret, _, err := s.loadSecret(ctx, userPublicID)
	if err != nil {
		return nil, err
	}
	if !totp.Validate(secret, code, s.clock.Now(), s.skew) {
		return nil, ErrInvalidCode
	}
	if err := s.repo.SetEnabled(ctx, userPublicID, s.clock.Now()); err != nil {
		return nil, err
	}
	codes, hashes, err := s.generateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if err := s.repo.SetRecoveryHashes(ctx, userPublicID, hashes); err != nil {
		return nil, err
	}
	return codes, nil
}

// RegenerateRecoveryCodes issues a new set (invalidating the old), gated by a valid
// current authenticator code so a hijacked session can't silently mint new codes.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, userPublicID, totpCode string) ([]string, error) {
	secret, enabled, err := s.loadSecret(ctx, userPublicID)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, ErrNotEnrolled
	}
	if !totp.Validate(secret, totpCode, s.clock.Now(), s.skew) {
		return nil, ErrInvalidCode
	}
	codes, hashes, err := s.generateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if err := s.repo.SetRecoveryHashes(ctx, userPublicID, hashes); err != nil {
		return nil, err
	}
	return codes, nil
}

// RecoveryRemaining reports how many unused recovery codes the user has.
func (s *Service) RecoveryRemaining(ctx context.Context, userPublicID string) (int, error) {
	return s.repo.RecoveryRemaining(ctx, userPublicID)
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
	// A live authenticator code is the common path...
	if totp.Validate(secret, code, s.clock.Now(), s.skew) {
		return nil
	}
	// ...otherwise accept a one-time recovery code (lost-device fallback). It's
	// atomically consumed so it can't be replayed. This is what stops a lost phone
	// from becoming a permanent cash-out lockout.
	consumed, err := s.repo.ConsumeRecoveryHash(ctx, userPublicID, s.hashRecovery(code))
	if err != nil {
		return err
	}
	if consumed {
		return nil
	}
	return ErrInvalidCode
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
