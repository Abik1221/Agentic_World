// Package config loads and validates all runtime configuration from the
// environment exactly once at boot. It is intentionally dependency-free and
// fail-fast: a process started with missing or invalid configuration refuses to
// run rather than booting into a broken state.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the typed, validated configuration for the whole service. It is
// constructed once in main and injected; nothing reads the environment directly.
type Config struct {
	// Server
	Env           string
	Port          int
	BaseURL       string
	LogLevel      string
	ReadTimeout   time.Duration
	WriteTimeout  time.Duration
	IdleTimeout   time.Duration
	ShutdownGrace time.Duration

	// Data layer
	DatabaseURL string
	DBMaxConns  int32
	RedisURL    string

	// HTTP
	CORSAllowedOrigins []string
	// TrustedProxyCount is how many reverse proxies (edge/LB) sit in front of the
	// app. The client IP is read as the Nth-from-the-right X-Forwarded-For entry
	// (the address our own trusted hops vouch for), so a client can't spoof it by
	// prepending forged entries. Default 1 (a single LB).
	TrustedProxyCount int

	// Auth & onboarding (Stage 1)
	JWTSigningKey     string
	APIKeyPepper      string
	DashboardTokenTTL time.Duration
	ClaimTTL          time.Duration

	// Privy authentication (Beta wallet pipeline P1). Privy is the front door for
	// social/email/wallet login; the backend verifies its ES256 access token and
	// exchanges it for a dashboard JWT. Both empty => Privy login disabled (503),
	// existing email/password + X-claim paths unaffected.
	PrivyAppID           string // Privy application id (JWT audience)
	PrivyVerificationKey string // PEM ECDSA P-256 public key from the Privy dashboard
	HCaptchaSecret       string // optional; empty => dev pass-through captcha
	XBearerToken         string // optional; empty => dev claim verifier (auto-verify)

	// Solana USDC deposits (Beta wallet pipeline P2). Deposits are enabled only
	// when the RPC URL + platform owner + platform ATA are all set (see
	// DepositsEnabled); otherwise /v1/deposits returns 503 and the listener is off.
	// The coin peg is derived from CoinCents (1 USDC = 100¢ = 100/CoinCents coins).
	SolanaRPCURL        string
	SolanaCommitment    string        // finalized (default) | confirmed
	SolanaUSDCMint      string        // SPL mint accepted for deposits (defaults to mainnet USDC)
	SolanaPlatformOwner string        // platform wallet (Solana Pay recipient)
	SolanaPlatformATA   string        // platform USDC token account (deposits must land here)
	DepositSessionTTL   time.Duration // how long a deposit session stays open
	DepositMinUSDC      int64         // minimum deposit in whole USDC (0 = no minimum)
	DepositPollInterval time.Duration // listener cadence

	// Solana withdrawals (Beta wallet pipeline P3). Setting the hot-wallet secret
	// (base58 private key) alongside the deposit config switches the payout rail to
	// Solana USDC (see WithdrawalsSolana). The hot wallet is fee-payer + transfer
	// authority; keep this secret out of logs.
	SolanaHotWalletSecret   string
	WithdrawConfirmInterval time.Duration // confirmation watcher cadence
	WalletReconInterval     time.Duration // wallet reconciliation cadence (drift safety net)

	// Game defaults (consumed from Stage 3)
	MoveWindow    time.Duration
	RakePct       int
	DefaultRounds int
	SSEMaxConns   int // instance-wide SSE spectator connection ceiling (0 = hub default)

	// AutoMigrate applies pending DB migrations in-process on startup (safe for
	// multi-instance: golang-migrate takes an advisory lock). Default true.
	AutoMigrate bool

	// Sandbox practice mode — risk-free matches vs the house agents.
	SandboxEnabled bool

	// Agent manifest / endpoint verification. These two are split so enabling one
	// does not silently enable the other, and BOTH are refused in prod/staging
	// (see validate) so a dev flag copied into a real env can't open an SSRF hole.
	//   AgentVerifyAllowPrivate  — disable the SSRF private/loopback/link-local IP
	//                              guard on the outbound probe (dev/e2e only).
	//   AgentVerifyAllowInsecure — permit plaintext http:// endpoint URLs.
	// AllowPrivate implies AllowInsecure at wiring time (a loopback stub is http),
	// so existing single-flag local/e2e setups keep working.
	AgentVerifyAllowPrivate  bool
	AgentVerifyAllowInsecure bool
	AgentVerifyTimeout       time.Duration // per-attempt deadline
	AgentVerifyMaxTimeout    time.Duration // hard ceiling
	AgentVerifyRetries       int           // retries after the first attempt
	AgentVerifyMaxBodyBytes  int64         // response body cap
	// AgentEndpointSecretKey encrypts the endpoint bearer token at rest. Falls
	// back to APIKeyPepper when unset so a key always exists.
	AgentEndpointSecretKey string

	// Ratings (Stage 7)
	SeasonLength time.Duration // length of one ranked season

	// Engagement (Stage 8)
	ClipCDNBase string // CDN prefix for generated clip assets

	// Cash-out / withdrawals (coin engine)
	CoinCents                int64         // face value of one coin in cents (default 1)
	WithdrawSellFeePct       int           // platform cut on withdrawal
	StripePayoutFeePct       int           // Stripe payout fee %, passed to the user
	StripePayoutFeeFlatCents int64         // flat Stripe payout fee, passed to the user
	WithdrawMinCoins         int64         // minimum withdrawal in coins
	WithdrawClearing         time.Duration // request must age this long before approval

	// Trust & anti-fraud (Stage 9)
	AdminUserIDs      []string      // user public ids allowed to use admin endpoints
	DetectInterval    time.Duration // anti-fraud detection sweep cadence
	CollusionLookback time.Duration // how far back the collusion sweep looks
	CollusionMinGames int           // minimum head-to-head games before flagging

	// Money & limits (Stage 4)
	SessionWindow     time.Duration // trailing window defining a "session" for session-loss
	ReconcileInterval time.Duration // ledger reconciliation cadence
	AllowMint         bool          // enables the non-prod test mint endpoint

	// Payments (Stage 5) — empty StripeSecretKey selects the offline DevGateway.
	StripeSecretKey           string
	StripeWebhookSecret       string
	CheckoutSuccessURL        string
	CheckoutCancelURL         string
	ConnectReturnURL          string
	ConnectRefreshURL         string
	PaymentsReconcileInterval time.Duration

	// Arena Pass subscription (Stripe Billing)
	StripeArenaPassPriceID    string
	SubscriptionSuccessURL    string
	SubscriptionCancelURL     string
	SubscriptionPortalURL     string
	ArenaPassMonthlyCoins     int64
	StripeArenaPassPriceCents int64

	// Demo bots: rule-based agents that fill tables in local/dev (no LLM).
	DemoBots bool

	// Observability
	OTLPEndpoint string

	// Platform bus (cross-service Redis channel with the Super Admin). Ed25519
	// keys authenticate messages: the engine signs the events it publishes with
	// its private key and verifies config against the Admin's public key. Empty
	// keys disable signing/verification (dev/local only). See
	// docs/architecture/platform-config-bus.md.
	PlatformEnginePrivateKey string // base64 32-byte seed; signs outbound events
	PlatformAdminPublicKey   string // base64 32-byte key; verifies inbound config
}

// IsProd reports whether the service runs in a production-like environment.
func (c *Config) IsProd() bool { return c.Env == "prod" || c.Env == "staging" }

// DepositsEnabled reports whether Solana USDC deposits are fully configured.
func (c *Config) DepositsEnabled() bool {
	return c.SolanaRPCURL != "" && c.SolanaUSDCMint != "" &&
		c.SolanaPlatformOwner != "" && c.SolanaPlatformATA != ""
}

// WithdrawalsSolana reports whether cash-out should use the Solana USDC rail:
// the deposit config plus a hot-wallet secret to sign payouts. When false, the
// existing Stripe/Dev payout rail is used.
func (c *Config) WithdrawalsSolana() bool {
	return c.DepositsEnabled() && c.SolanaHotWalletSecret != ""
}

// Load reads configuration from the environment, applying defaults, then
// validates it. All problems are aggregated into a single returned error so the
// operator sees everything wrong at once.
func Load() (*Config, error) {
	l := &loader{}

	c := &Config{
		Env:           l.str("ENV", "local"),
		Port:          l.intVal("PORT", 8080),
		BaseURL:       l.str("BASE_URL", "http://localhost:8080"),
		LogLevel:      l.str("LOG_LEVEL", "info"),
		ReadTimeout:   l.dur("READ_TIMEOUT", 10*time.Second),
		WriteTimeout:  l.dur("WRITE_TIMEOUT", 15*time.Second),
		IdleTimeout:   l.dur("IDLE_TIMEOUT", 60*time.Second),
		ShutdownGrace: l.dur("SHUTDOWN_GRACE", 20*time.Second),

		DatabaseURL: l.required("DATABASE_URL"),
		DBMaxConns:  int32(l.intVal("DB_MAX_CONNS", 20)),
		RedisURL:    l.required("REDIS_URL"),

		CORSAllowedOrigins: l.csv("CORS_ALLOWED_ORIGINS", "http://localhost:3000"),
		TrustedProxyCount:  l.intVal("TRUSTED_PROXY_COUNT", 1),

		JWTSigningKey:     l.required("JWT_SIGNING_KEY"),
		APIKeyPepper:      l.required("API_KEY_PEPPER"),
		DashboardTokenTTL: l.dur("DASHBOARD_TOKEN_TTL", 24*time.Hour),
		ClaimTTL:          l.dur("CLAIM_TTL", 30*time.Minute),
		HCaptchaSecret:    l.str("HCAPTCHA_SECRET", ""),
		XBearerToken:      l.str("X_BEARER_TOKEN", ""),

		PrivyAppID:           l.str("PRIVY_APP_ID", ""),
		PrivyVerificationKey: l.str("PRIVY_VERIFICATION_KEY", ""),

		SolanaRPCURL:        l.str("SOLANA_RPC_URL", ""),
		SolanaCommitment:    l.str("SOLANA_COMMITMENT", "finalized"),
		SolanaUSDCMint:      l.str("SOLANA_USDC_MINT", "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"),
		SolanaPlatformOwner: l.str("SOLANA_PLATFORM_OWNER", ""),
		SolanaPlatformATA:   l.str("SOLANA_PLATFORM_ATA", ""),
		DepositSessionTTL:   l.dur("DEPOSIT_SESSION_TTL", 30*time.Minute),
		DepositMinUSDC:      int64(l.intVal("DEPOSIT_MIN_USDC", 1)),
		DepositPollInterval: l.dur("DEPOSIT_POLL_INTERVAL", 15*time.Second),

		SolanaHotWalletSecret:   l.str("SOLANA_HOT_WALLET_SECRET", ""),
		WithdrawConfirmInterval: l.dur("WITHDRAW_CONFIRM_INTERVAL", 15*time.Second),
		WalletReconInterval:     l.dur("WALLET_RECON_INTERVAL", time.Hour),

		MoveWindow:     time.Duration(l.intVal("MOVE_WINDOW_SECONDS", 20)) * time.Second,
		RakePct:        l.intVal("RAKE_PCT", 5),
		DefaultRounds:  l.intVal("DEFAULT_ROUNDS", 13),
		SSEMaxConns:    l.intVal("SSE_MAX_CONNS", 20000),
		AutoMigrate:    l.boolVal("AUTO_MIGRATE", true),
		SandboxEnabled: l.boolVal("SANDBOX_ENABLED", true),

		AgentVerifyAllowPrivate:  l.boolVal("AGENT_VERIFY_ALLOW_PRIVATE", false),
		AgentVerifyAllowInsecure: l.boolVal("AGENT_VERIFY_ALLOW_INSECURE", false),
		AgentVerifyTimeout:       l.dur("AGENT_VERIFY_TIMEOUT", 5*time.Second),
		AgentVerifyMaxTimeout:    l.dur("AGENT_VERIFY_MAX_TIMEOUT", 15*time.Second),
		AgentVerifyRetries:       l.intVal("AGENT_VERIFY_RETRIES", 2),
		AgentVerifyMaxBodyBytes:  int64(l.intVal("AGENT_VERIFY_MAX_BODY_BYTES", 65536)),
		AgentEndpointSecretKey:   l.str("AGENT_ENDPOINT_SECRET_KEY", ""),

		SeasonLength: l.dur("SEASON_LENGTH", 30*24*time.Hour),

		ClipCDNBase: l.str("CLIP_CDN_BASE", "https://cdn.local/clips"),

		CoinCents:                int64(l.intVal("COIN_CENTS", 1)),
		WithdrawSellFeePct:       l.intVal("WITHDRAW_SELL_FEE_PCT", 10),
		StripePayoutFeePct:       l.intVal("STRIPE_PAYOUT_FEE_PCT", 0),
		StripePayoutFeeFlatCents: int64(l.intVal("STRIPE_PAYOUT_FEE_FLAT_CENTS", 25)),
		WithdrawMinCoins:         int64(l.intVal("WITHDRAW_MIN_COINS", 500)),
		WithdrawClearing:         l.dur("WITHDRAW_CLEARING", 24*time.Hour),

		AdminUserIDs:      l.csv("ADMIN_USER_IDS", ""),
		DetectInterval:    l.dur("DETECT_INTERVAL", time.Hour),
		CollusionLookback: l.dur("COLLUSION_LOOKBACK", 7*24*time.Hour),
		CollusionMinGames: l.intVal("COLLUSION_MIN_GAMES", 5),

		SessionWindow:     l.dur("SESSION_WINDOW", 6*time.Hour),
		ReconcileInterval: l.dur("RECONCILE_INTERVAL", 24*time.Hour),

		StripeSecretKey:           l.str("STRIPE_SECRET_KEY", ""),
		StripeWebhookSecret:       l.str("STRIPE_WEBHOOK_SECRET", ""),
		PaymentsReconcileInterval: l.dur("PAYMENTS_RECONCILE_INTERVAL", time.Hour),

		StripeArenaPassPriceID:    l.str("STRIPE_ARENA_PASS_PRICE_ID", ""),
		SubscriptionSuccessURL:    l.str("SUBSCRIPTION_SUCCESS_URL", "http://localhost:3000/subscription/success"),
		SubscriptionCancelURL:     l.str("SUBSCRIPTION_CANCEL_URL", "http://localhost:3000/subscription/cancel"),
		SubscriptionPortalURL:     l.str("SUBSCRIPTION_PORTAL_URL", "http://localhost:3000/subscription"),
		ArenaPassMonthlyCoins:     int64(l.intVal("ARENA_PASS_MONTHLY_COINS", 1000)),
		StripeArenaPassPriceCents: int64(l.intVal("ARENA_PASS_PRICE_CENTS", 999)),

		OTLPEndpoint: l.str("OTEL_EXPORTER_OTLP_ENDPOINT", ""),

		PlatformEnginePrivateKey: l.str("PLATFORM_ENGINE_PRIVATE_KEY", ""),
		PlatformAdminPublicKey:   l.str("PLATFORM_ADMIN_PUBLIC_KEY", ""),
	}

	// Hosted return pages default to the frontend billing flow.
	c.CheckoutSuccessURL = l.str("CHECKOUT_SUCCESS_URL", "http://localhost:3000/billing/success")
	c.CheckoutCancelURL = l.str("CHECKOUT_CANCEL_URL", "http://localhost:3000/billing/cancel")
	c.ConnectReturnURL = l.str("CONNECT_RETURN_URL", "http://localhost:3000/payouts/return")
	c.ConnectRefreshURL = l.str("CONNECT_REFRESH_URL", "http://localhost:3000/payouts/refresh")

	// Mint is a test affordance: on by default off prod, and never silently on in
	// a prod-like env even if the env var is set true.
	c.AllowMint = l.boolVal("ALLOW_MINT", !c.IsProd()) && !c.IsProd()
	c.DemoBots = l.boolVal("DEMO_BOTS", !c.IsProd())

	if err := l.err(); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	var errs []string
	if c.Port < 1 || c.Port > 65535 {
		errs = append(errs, fmt.Sprintf("PORT out of range: %d", c.Port))
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Sprintf("LOG_LEVEL invalid: %q", c.LogLevel))
	}
	if c.RakePct < 0 || c.RakePct > 50 {
		errs = append(errs, fmt.Sprintf("RAKE_PCT out of range: %d", c.RakePct))
	}
	if c.DefaultRounds < 1 {
		errs = append(errs, fmt.Sprintf("DEFAULT_ROUNDS must be >= 1: %d", c.DefaultRounds))
	}
	// The coin peg must divide a dollar exactly: the internal economy values one
	// coin at CoinCents, and a USDC deposit credits 100/CoinCents coins per dollar
	// via integer division. A non-divisor (or out-of-range) value makes the deposit
	// peg asymmetric or silently truncated (and >100 would floor to 0 coins/USDC and
	// hit the fallback). Fail loudly rather than mis-peg the economy.
	if c.CoinCents < 1 || c.CoinCents > 100 || 100%c.CoinCents != 0 {
		errs = append(errs, fmt.Sprintf("COIN_CENTS must be a positive integer that divides 100 exactly (1,2,4,5,10,20,25,50,100): %d", c.CoinCents))
	}
	// If real Stripe is wired, a webhook secret is mandatory — otherwise inbound
	// events cannot be verified (and would be rejected anyway).
	if c.StripeSecretKey != "" && c.StripeWebhookSecret == "" {
		errs = append(errs, "STRIPE_WEBHOOK_SECRET is required when STRIPE_SECRET_KEY is set")
	}
	// Privy login is all-or-nothing: an app id without its verification key (or the
	// reverse) can't verify tokens, so fail loudly rather than silently disabling.
	if (c.PrivyAppID == "") != (c.PrivyVerificationKey == "") {
		errs = append(errs, "PRIVY_APP_ID and PRIVY_VERIFICATION_KEY must be set together (or both empty)")
	}
	// Solana deposits: an RPC URL without the platform destination (or vice versa)
	// can't credit deposits safely — require the full set together.
	if c.SolanaRPCURL != "" && (c.SolanaPlatformOwner == "" || c.SolanaPlatformATA == "") {
		errs = append(errs, "SOLANA_RPC_URL requires SOLANA_PLATFORM_OWNER and SOLANA_PLATFORM_ATA (deposit destination)")
	}
	if c.IsProd() {
		if strings.Contains(c.JWTSigningKey, "dev-only") {
			errs = append(errs, "JWT_SIGNING_KEY must not be a dev placeholder in prod/staging")
		}
		if len(c.JWTSigningKey) < 32 {
			errs = append(errs, "JWT_SIGNING_KEY must be >= 32 bytes in prod/staging")
		}
		if strings.Contains(c.APIKeyPepper, "dev-only") {
			errs = append(errs, "API_KEY_PEPPER must not be a dev placeholder in prod/staging")
		}
		// Fail closed: the platform config bus signs rake/fees/rewards config with
		// this key. Without it the verifier is nil and every snapshot "verifies"
		// (accepts forged config). Require it in prod rather than boot unauthenticated.
		if c.PlatformAdminPublicKey == "" {
			errs = append(errs, "PLATFORM_ADMIN_PUBLIC_KEY is required in prod/staging (config bus would otherwise accept unsigned/forged config)")
		}
		// Fail closed: the engine signs the domain events (withdrawal.requested,
		// match.finished, …) it publishes to the Super Admin's Redis stream with this
		// key. Empty ⇒ events go out UNSIGNED and the admin side can't tell genuine
		// events from ones forged into the stream. Require it in prod rather than emit
		// an unauthenticated event feed.
		if c.PlatformEnginePrivateKey == "" {
			errs = append(errs, "PLATFORM_ENGINE_PRIVATE_KEY is required in prod/staging (domain events would otherwise be published unsigned)")
		}
		// Fail closed: without a captcha secret the dev accept-all captcha is used,
		// removing the only non-rate-limit anti-automation control on onboarding.
		if c.HCaptchaSecret == "" {
			errs = append(errs, "HCAPTCHA_SECRET is required in prod/staging (onboarding would otherwise use the dev accept-all captcha)")
		}
		// A wildcard CORS origin lets any site read authenticated JSON responses.
		for _, o := range c.CORSAllowedOrigins {
			if strings.TrimSpace(o) == "*" {
				errs = append(errs, "CORS_ALLOWED_ORIGINS must not be \"*\" in prod/staging")
			}
		}
		// These disable the SSRF guard / TLS requirement on agent-endpoint probes —
		// a dev flag that must never reach a real env (one copied var would turn the
		// platform into an SSRF proxy against internal services + cloud metadata).
		if c.AgentVerifyAllowPrivate {
			errs = append(errs, "AGENT_VERIFY_ALLOW_PRIVATE must not be set in prod/staging (it disables the SSRF private-IP guard on agent endpoint verification)")
		}
		if c.AgentVerifyAllowInsecure {
			errs = append(errs, "AGENT_VERIFY_ALLOW_INSECURE must not be set in prod/staging (it permits plaintext http:// agent endpoints)")
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// loader reads env vars, applies defaults, and accumulates parse/missing errors.
type loader struct{ problems []string }

func (l *loader) err() error {
	if len(l.problems) == 0 {
		return nil
	}
	return errors.New("missing or unparseable configuration:\n  - " + strings.Join(l.problems, "\n  - "))
}

func (l *loader) str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func (l *loader) required(key string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	l.problems = append(l.problems, key+" is required")
	return ""
}

func (l *loader) intVal(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.problems = append(l.problems, fmt.Sprintf("%s must be an integer, got %q", key, v))
		return def
	}
	return n
}

func (l *loader) boolVal(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.problems = append(l.problems, fmt.Sprintf("%s must be a boolean, got %q", key, v))
		return def
	}
	return b
}

func (l *loader) dur(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.problems = append(l.problems, fmt.Sprintf("%s must be a duration (e.g. 10s), got %q", key, v))
		return def
	}
	return d
}

func (l *loader) csv(key, def string) []string {
	raw := l.str(key, def)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
