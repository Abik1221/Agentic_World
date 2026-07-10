package config

import (
	"strings"
	"testing"
)

func TestLoad_HappyPath(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("JWT_SIGNING_KEY", "this-is-a-sufficiently-long-dev-signing-key!")
	t.Setenv("API_KEY_PEPPER", "pepper")
	t.Setenv("PORT", "9090")
	t.Setenv("RAKE_PCT", "5")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if cfg.Port != 9090 {
		t.Errorf("Port = %d, want 9090", cfg.Port)
	}
	if cfg.DefaultRounds != 13 {
		t.Errorf("DefaultRounds default = %d, want 13", cfg.DefaultRounds)
	}
	if cfg.MoveWindow.Seconds() != 20 {
		t.Errorf("MoveWindow default = %s, want 20s", cfg.MoveWindow)
	}
}

func TestValidate(t *testing.T) {
	base := func() *Config {
		return &Config{
			Env: "local", Port: 8080, LogLevel: "info",
			RakePct: 5, DefaultRounds: 13, CoinCents: 1,
			JWTSigningKey: "x", APIKeyPepper: "y",
		}
	}
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"valid", func(*Config) {}, ""},
		{"bad port", func(c *Config) { c.Port = 0 }, "PORT"},
		{"bad log level", func(c *Config) { c.LogLevel = "verbose" }, "LOG_LEVEL"},
		{"bad rake", func(c *Config) { c.RakePct = 99 }, "RAKE_PCT"},
		{"bad rounds", func(c *Config) { c.DefaultRounds = 0 }, "DEFAULT_ROUNDS"},
		{"coin cents non-divisor", func(c *Config) { c.CoinCents = 3 }, "COIN_CENTS"},
		{"coin cents zero", func(c *Config) { c.CoinCents = 0 }, "COIN_CENTS"},
		{"coin cents over 100", func(c *Config) { c.CoinCents = 200 }, "COIN_CENTS"},
		{"prod placeholder jwt", func(c *Config) {
			c.Env = "prod"
			c.JWTSigningKey = "dev-only-change-me-................"
			c.APIKeyPepper = "real-pepper"
		}, "JWT_SIGNING_KEY"},
		{"prod short jwt", func(c *Config) {
			c.Env = "prod"
			c.JWTSigningKey = "short"
			c.APIKeyPepper = "real-pepper"
		}, "JWT_SIGNING_KEY"},
		{"prod requires platform admin key", func(c *Config) {
			c.Env = "prod"
			c.JWTSigningKey = "a-sufficiently-long-prod-signing-key!!"
			c.APIKeyPepper = "real-pepper"
			c.HCaptchaSecret = "hc-secret"
			// PlatformAdminPublicKey left empty -> must be rejected (fail closed).
		}, "PLATFORM_ADMIN_PUBLIC_KEY"},
		{"prod requires captcha secret", func(c *Config) {
			c.Env = "prod"
			c.JWTSigningKey = "a-sufficiently-long-prod-signing-key!!"
			c.APIKeyPepper = "real-pepper"
			c.PlatformAdminPublicKey = "some-key"
			// HCaptchaSecret left empty -> must be rejected (fail closed).
		}, "HCAPTCHA_SECRET"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base()
			tt.mutate(c)
			err := c.validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}
