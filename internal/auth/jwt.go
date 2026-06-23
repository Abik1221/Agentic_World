package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWT issues and verifies user-scope dashboard tokens (HS256). Agent credentials
// are API keys, not JWTs — only the human owner gets a JWT.
type JWT struct {
	key []byte
	ttl time.Duration
}

func NewJWT(signingKey string, ttl time.Duration) *JWT {
	return &JWT{key: []byte(signingKey), ttl: ttl}
}

type userClaims struct {
	Scope string `json:"scope"`
	jwt.RegisteredClaims
}

// Issue mints a user-scope token for the given user public ID.
func (j *JWT) Issue(userPublicID string) (string, error) {
	now := time.Now()
	claims := userClaims{
		Scope: string(ScopeUser),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userPublicID,
			Issuer:    "agent-arena",
			Audience:  jwt.ClaimStrings{"dashboard"},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(j.ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(j.key)
}

// Parse validates a token and returns the user Principal.
func (j *JWT) Parse(raw string) (*Principal, error) {
	var claims userClaims
	_, err := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return j.key, nil
	}, jwt.WithIssuer("agent-arena"), jwt.WithAudience("dashboard"))
	if err != nil {
		return nil, err
	}
	if claims.Scope != string(ScopeUser) || claims.Subject == "" {
		return nil, errors.New("invalid token claims")
	}
	return &Principal{Scope: ScopeUser, UserPublicID: claims.Subject}, nil
}
