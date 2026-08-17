package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Rotating refresh tokens for dashboard (user) sessions.
//
// The access token is a short-lived HS256 JWT (see JWT). This adds a long-lived,
// SINGLE-USE, ROTATING refresh token with a sliding idle window:
//   - every rotation mints a fresh access JWT + a fresh refresh token in the SAME
//     family and pushes the expiry to now+ttl, so an ACTIVE user is never logged
//     out; an idle user past the window is.
//   - the raw secret is NEVER stored — only its SHA-256. The token is "<id>.<secret>".
//   - reuse of an already-rotated token means the token leaked: we revoke the
//     entire family (theft response), forcing a fresh sign-in.
//
// # The reuse INTERVAL, and why single-use alone was unusable
//
// Strict single-use is correct against theft and wrong against concurrency. One dashboard
// navigation fires several requests at once — the page, its RSC payload, prefetches — and
// each passes through the edge middleware. With the access JWT expired they ALL present the
// same refresh token: the first rotates it, and every other one arrives at a token already
// marked used. Read strictly that is theft, so the family was revoked and the user was
// signed out of every device — by clicking a sidebar link.
//
// So a short grace window after rotation treats a repeat presentation as the race it almost
// always is: a fresh token is minted in the same family and nothing is revoked. Past the
// window the theft response stands. This is the same control Auth0 ships as its refresh
// token "reuse interval" and Okta as rotation leeway; the window is deliberately seconds,
// not minutes, because it is exactly the span in which a legitimate client can still be
// holding the old token in flight.

var (
	ErrRefreshInvalid = errors.New("invalid refresh token")
	ErrRefreshExpired = errors.New("refresh token expired")
	ErrRefreshReused  = errors.New("refresh token reuse detected")
)

// RefreshRow is one stored refresh token; the secret is not stored, only TokenHash.
type RefreshRow struct {
	ID           string
	UserPublicID string
	FamilyID     string
	TokenHash    string
	ExpiresAt    time.Time
	UsedAt       *time.Time
	RevokedAt    *time.Time
}

// RefreshRepo persists refresh tokens. Get returns ErrRefreshInvalid when absent.
type RefreshRepo interface {
	Create(ctx context.Context, r RefreshRow) error
	Get(ctx context.Context, id string) (RefreshRow, error)
	MarkUsed(ctx context.Context, id string, at time.Time) error
	RevokeFamily(ctx context.Context, familyID string, at time.Time) error
}

// RefreshService issues + rotates refresh tokens and mints the paired access JWT.
type RefreshService struct {
	repo RefreshRepo
	jwt  *JWT
	ttl  time.Duration
	// reuseGrace is how long after rotation a repeat presentation is treated as a benign
	// concurrent request rather than as theft. Seconds, not minutes: see the note above.
	reuseGrace time.Duration
	now  func() time.Time
}

// DefaultReuseGrace is the window in which a re-presented refresh token is a race rather
// than a breach. Long enough to cover the concurrent requests of one navigation, short
// enough that a stolen token is still caught almost immediately.
const DefaultReuseGrace = 15 * time.Second

func NewRefreshService(repo RefreshRepo, jwt *JWT, ttl time.Duration) *RefreshService {
	return &RefreshService{repo: repo, jwt: jwt, ttl: ttl, reuseGrace: DefaultReuseGrace, now: time.Now}
}

// TTL is the sliding idle window for a refresh token.
func (s *RefreshService) TTL() time.Duration { return s.ttl }

func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func splitToken(raw string) (id, secret string, ok bool) {
	i := strings.IndexByte(raw, '.')
	if i <= 0 || i >= len(raw)-1 {
		return "", "", false
	}
	return raw[:i], raw[i+1:], true
}

// Issue starts a brand-new refresh family for a user (called at login/signup).
func (s *RefreshService) Issue(ctx context.Context, userPublicID string) (string, error) {
	return s.mint(ctx, userPublicID, "rf_"+randToken(12))
}

func (s *RefreshService) mint(ctx context.Context, userPublicID, familyID string) (string, error) {
	id := "rt_" + randToken(12)
	secret := randToken(32)
	if err := s.repo.Create(ctx, RefreshRow{
		ID:           id,
		UserPublicID: userPublicID,
		FamilyID:     familyID,
		TokenHash:    hashSecret(secret),
		ExpiresAt:    s.now().Add(s.ttl),
	}); err != nil {
		return "", err
	}
	return id + "." + secret, nil
}

// Rotate validates a refresh token, single-uses it, and returns a fresh access
// JWT + a new refresh token in the same family (sliding the idle window). Reuse of
// an already-rotated token revokes the whole family.
func (s *RefreshService) Rotate(ctx context.Context, raw string) (access, newRefresh, userPublicID string, err error) {
	id, secret, ok := splitToken(raw)
	if !ok {
		return "", "", "", ErrRefreshInvalid
	}
	row, err := s.repo.Get(ctx, id)
	if err != nil {
		return "", "", "", ErrRefreshInvalid
	}
	if subtle.ConstantTimeCompare([]byte(row.TokenHash), []byte(hashSecret(secret))) != 1 {
		return "", "", "", ErrRefreshInvalid
	}
	now := s.now()
	if row.RevokedAt != nil {
		return "", "", "", ErrRefreshInvalid
	}
	if row.UsedAt != nil {
		// Presented again. Within the grace window this is the concurrency race described
		// above, not a breach: mint a fresh token in the SAME family and revoke nothing. The
		// row stays marked used — we are not re-rotating it, we are answering a second
		// caller who never saw the first response.
		if now.Sub(*row.UsedAt) <= s.reuseGrace {
			if now.After(row.ExpiresAt) {
				return "", "", "", ErrRefreshExpired
			}
			nr, mErr := s.mint(ctx, row.UserPublicID, row.FamilyID)
			if mErr != nil {
				return "", "", "", mErr
			}
			acc, aErr := s.jwt.Issue(row.UserPublicID)
			if aErr != nil {
				return "", "", "", aErr
			}
			return acc, nr, row.UserPublicID, nil
		}
		// Past the window → the token leaked. Revoke the whole family.
		_ = s.repo.RevokeFamily(ctx, row.FamilyID, now)
		return "", "", "", ErrRefreshReused
	}
	if now.After(row.ExpiresAt) {
		return "", "", "", ErrRefreshExpired
	}
	if err := s.repo.MarkUsed(ctx, row.ID, now); err != nil {
		return "", "", "", err
	}
	nr, err := s.mint(ctx, row.UserPublicID, row.FamilyID)
	if err != nil {
		return "", "", "", err
	}
	acc, err := s.jwt.Issue(row.UserPublicID)
	if err != nil {
		return "", "", "", err
	}
	return acc, nr, row.UserPublicID, nil
}

// Revoke kills the whole family behind a refresh token (logout / sign-out).
func (s *RefreshService) Revoke(ctx context.Context, raw string) error {
	id, _, ok := splitToken(raw)
	if !ok {
		return nil
	}
	row, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil // unknown token → nothing to revoke
	}
	return s.repo.RevokeFamily(ctx, row.FamilyID, s.now())
}
