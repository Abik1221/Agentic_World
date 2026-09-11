// Command server is the single entry point and composition root for Agent Arena.
// It is the only place that constructs concrete dependencies and wires them
// together; every other package depends on interfaces, not on each other's
// internals. See docs/architecture/project-layout.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/agent-arena/arena/internal/adminapi"
	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/antifraud"
	"github.com/agent-arena/arena/internal/arena"
	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/autoplay"
	"github.com/agent-arena/arena/internal/badges"
	"github.com/agent-arena/arena/internal/benchmark"
	"github.com/agent-arena/arena/internal/blockchain"
	"github.com/agent-arena/arena/internal/bot"
	"github.com/agent-arena/arena/internal/clips"
	"github.com/agent-arena/arena/internal/config"
	"github.com/agent-arena/arena/internal/deadline"
	"github.com/agent-arena/arena/internal/deception"
	"github.com/agent-arena/arena/internal/demo"
	"github.com/agent-arena/arena/internal/devplatform"
	"github.com/agent-arena/arena/internal/devprofile"
	"github.com/agent-arena/arena/internal/devtrace"
	"github.com/agent-arena/arena/internal/docs"
	mafiaengine "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/events"
	"github.com/agent-arena/arena/internal/gamestakes"
	"github.com/agent-arena/arena/internal/groupmatch"
	"github.com/agent-arena/arena/internal/health"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/identity"
	"github.com/agent-arena/arena/internal/invoices"
	"github.com/agent-arena/arena/internal/ledger"
	"github.com/agent-arena/arena/internal/liveness"
	"github.com/agent-arena/arena/internal/llmgw"
	"github.com/agent-arena/arena/internal/mafia"
	"github.com/agent-arena/arena/internal/manifest"
	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/matchmaking"
	"github.com/agent-arena/arena/internal/media"
	"github.com/agent-arena/arena/internal/middleware"
	"github.com/agent-arena/arena/internal/modelboard"
	"github.com/agent-arena/arena/internal/openapi"
	"github.com/agent-arena/arena/internal/payments"
	"github.com/agent-arena/arena/internal/paymenttrace"
	"github.com/agent-arena/arena/internal/payout"
	"github.com/agent-arena/arena/internal/pindex"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/agent-arena/arena/internal/platformcfg"
	"github.com/agent-arena/arena/internal/platformsign"
	"github.com/agent-arena/arena/internal/profiles"
	"github.com/agent-arena/arena/internal/rating"
	"github.com/agent-arena/arena/internal/sandbox"
	"github.com/agent-arena/arena/internal/sdkstats"
	"github.com/agent-arena/arena/internal/secretbox"
	"github.com/agent-arena/arena/internal/seedadmin"
	"github.com/agent-arena/arena/internal/skill"
	"github.com/agent-arena/arena/internal/social"
	"github.com/agent-arena/arena/internal/solanadeposit"
	"github.com/agent-arena/arena/internal/spectator"
	"github.com/agent-arena/arena/internal/store"
	"github.com/agent-arena/arena/internal/subscription"
	"github.com/agent-arena/arena/internal/telemetrybridge"
	"github.com/agent-arena/arena/internal/tournament"
	"github.com/agent-arena/arena/internal/turnproof"
	"github.com/agent-arena/arena/internal/twofa"
	"github.com/agent-arena/arena/internal/userevents"
	"github.com/agent-arena/arena/internal/verification"
	"github.com/agent-arena/arena/internal/wallet"
	"github.com/agent-arena/arena/internal/walletadmin"
	"github.com/agent-arena/arena/internal/walletrecon"
	"github.com/agent-arena/arena/internal/walletverify"
	"github.com/agent-arena/arena/internal/webhook"
)

// version is injected at build time via -ldflags "-X main.version=$(git rev-parse --short HEAD)".
var version = "dev"

// publishableHosts is the set of upstreams whose responses may be published as a model
// measurement, as a sorted slice for the SQL ANY(...) parameter.
func publishableHosts() []string {
	set := llmgw.PublishableUpstreamHosts(os.Getenv("BENCHMARK_EXTRA_HOSTS"))
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

func main() {
	if err := run(); err != nil {
		// Use the default logger; structured logging may not be up yet on early failures.
		slog.Error("fatal: server exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. Configuration — fail fast on anything missing or invalid.
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// 2. Logging.
	log := platform.NewLogger(cfg.Env, cfg.LogLevel)

	// Non-fatal config concerns. These are things we can only SUSPECT are wrong — a
	// warning that is never printed is the same as no check at all, so they go out
	// at WARN before anything else starts.
	for _, w := range cfg.Warnings {
		log.Warn("config", "warning", w)
	}

	// 2b. Pyyol Lens telemetry emitter (observability). Constructed early so the
	// logger can be teed into it and every downstream component can take it. A
	// disabled/misconfigured Lens yields a no-op emitter (zero hot-path cost).
	lens := telemetry.New(telemetry.Config{
		Enabled:         cfg.PyyolLensEnabled,
		Endpoint:        cfg.PyyolLensEndpoint,
		APIKey:          cfg.PyyolLensAPIKey,
		Project:         cfg.PyyolLensProject,
		Organization:    cfg.PyyolLensOrg,
		Environment:     cfg.Env,
		ServiceName:     "arena-engine",
		TraceSampleRate: cfg.PyyolLensTraceSampleRate,
	}, log)
	// Tee logs at/above the configured level into the Lens as correlated
	// log_record events (stdout logging is untouched). "all logs, in one place."
	log = slog.New(telemetry.NewLogHandler(log.Handler(), lens, parseLensLogLevel(cfg.PyyolLensLogLevel)))
	slog.SetDefault(log)
	log.Info("starting agent-arena", "env", cfg.Env, "version", version, "port", cfg.Port, "telemetry", lens.Enabled())
	// Outbound TLS trust, checked before anything relies on it. A container with no CA bundle
	// starts clean and passes every health check while failing every https call — which surfaced
	// as "endpoint not verified" on agent onboarding and read as the developer's fault.
	platform.CheckTLSTrust(log)

	// 3. Signal-aware root context: SIGINT/SIGTERM begin graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 4. Tracing seam (no-op until OTLP is configured; see platform/tracing.go).
	tracer, err := platform.NewTracerProvider(ctx, cfg.OTLPEndpoint)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tracer.Shutdown(shutdownCtx)
	}()
	// Flush buffered telemetry on shutdown so the last match/logs are not lost.
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = lens.Shutdown(shutdownCtx)
	}()

	// 5. Cross-cutting platform primitives.
	metrics := platform.NewMetrics()
	clock := platform.NewClock()

	// 6. Data layer — verified (pinged) at open; fail fast if unreachable.
	openCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	st, err := store.Open(openCtx, cfg)
	cancel()
	if err != nil {
		return err
	}
	defer st.Close()
	log.Info("data layer connected")

	// Durable benchmark sink: per-match decision-quality summaries ride the outbox
	// (crash-safe, at-least-once). This is ALWAYS wired — the local P-Index
	// Intelligence projection (eventBus.On(TypeMatchBenchmark) below) consumes
	// these rows to feed agent_match_benchmark, and that recompute worker runs
	// regardless of telemetry. Gating this on lens.Enabled() would silently starve
	// the Intelligence dimension of all data in any deployment where the optional
	// Pyyol Lens/tracing stack is off. (The Lens *emission* — the `em` telemetry
	// client — is separately and correctly gated; see benchmark.Flush/Emit.)
	// Shared by every game's drive loop (goofspiel ranked, mafia/monopoly push).
	var benchMeta benchmark.AgentMetaResolver
	benchPersist := benchmark.Persist(func(ctx context.Context, eventType string, payload []byte) error {
		_, err := store.InsertEvent(ctx, st.DB, eventType, payload)
		return err
	})

	// Auto-migrate on startup: apply any pending schema migrations in-process
	// before serving. Safe for multi-instance (advisory-locked); disable with
	// AUTO_MIGRATE=false to manage migrations out-of-band.
	if cfg.AutoMigrate {
		if err := store.Migrate(cfg.DatabaseURL); err != nil {
			return err
		}
		log.Info("database migrations applied", "auto_migrate", true)
	}

	// Platform config plane: the Super Admin owns the business rules (season,
	// scoring, match limits, economy, feature flags, SDK/manifest requirements);
	// the engine consumes them asynchronously from Redis into an in-memory cache
	// with env-default fallback, so it keeps executing matches on last-known-good
	// config even while the admin service is unreachable. Services read dynamic
	// values via platformCfg.Get(). See docs/architecture/platform-config-bus.md.
	//
	// Messages are Ed25519-authenticated: the engine verifies config against the
	// Admin's public key and signs the events it publishes with its own private
	// key. Empty keys disable signing (dev/local); refuse that in prod-like envs.
	adminPubKey, err := platformsign.NewVerifier(cfg.PlatformAdminPublicKey)
	if err != nil {
		return err
	}
	enginePrivKey, err := platformsign.NewSigner(cfg.PlatformEnginePrivateKey)
	if err != nil {
		return err
	}
	if cfg.IsProd() && (!adminPubKey.Enabled() || !enginePrivKey.Enabled()) {
		log.Warn("platform bus signing keys unset in a prod-like env: config/events are UNAUTHENTICATED — set PLATFORM_ADMIN_PUBLIC_KEY and PLATFORM_ENGINE_PRIVATE_KEY")
	}
	platformCfg := platformcfg.New(
		store.NewPlatformConfigSource(st.Redis),
		platformcfg.Defaults(cfg),
		adminPubKey,
		log, time.Minute,
	)
	// Every long-lived background worker is launched through SafeLoop so a panic in
	// one loop logs + restarts instead of aborting the whole process — an unrecovered
	// goroutine panic kills the program, taking in-flight matches, money settlement,
	// and SSE streams down with it.
	launch := func(name string, run func(context.Context)) {
		go platform.SafeLoop(ctx, log, name, run)
	}
	launch("platform-config", platformCfg.Run)

	// 7. Modules.
	healthH := health.New(st, clock, version)

	// Identity & onboarding: pick external integrations by configuration. Dev
	// implementations let onboarding run fully offline (local/staging); prod
	// supplies real credentials. The X claim verifier's production implementation
	// is wired when X credentials are provisioned (see identity/verifier.go).
	var verifier identity.ClaimVerifier = identity.DevClaimVerifier{}
	var captcha identity.Captcha = identity.DevCaptcha{}
	if cfg.HCaptchaSecret != "" {
		captcha = identity.NewHCaptcha(cfg.HCaptchaSecret)
	}
	// X-claim (tweet) onboarding needs a real X verifier; only DevClaimVerifier
	// (auto-approve) exists today, so it is DEV-ONLY. In prod we fail closed: the
	// X-claim register/verify routes return 503 and onboarding goes through
	// email/password + magic-link. Phase 6 wires the real verifier and flips this on.
	xClaimEnabled := !cfg.IsProd()
	if cfg.IsProd() {
		log.Info("X-claim onboarding disabled in prod (no real verifier yet); using email/password + magic-link")
	}

	jwt := auth.NewJWT(cfg.JWTSigningKey, cfg.DashboardTokenTTL)
	idRepo := store.NewIdentityRepo(st.DB)
	idSvc := identity.New(idRepo, verifier, captcha, jwt, clock, cfg.APIKeyPepper, cfg.ClaimTTL)
	// Service-to-service admin auth: trust the Super Admin's Ed25519 "Platform"
	// tokens using the same public key as the config bus. Empty key ⇒ disabled.
	platformAuth, err := auth.NewPlatformVerifier(cfg.PlatformAdminPublicKey)
	if err != nil {
		return err
	}
	authn := auth.NewAuthenticator(idSvc, jwt, platformAuth, log)

	// Privy is the Beta login front door (social/email/wallet). Its ES256 access
	// token is verified here and exchanged for a dashboard JWT at /v1/auth/privy.
	// Empty config ⇒ nil verifier ⇒ that route returns 503 and the existing
	// email/password + magic-link paths are unaffected.
	privyAuth, err := auth.NewPrivyVerifier(cfg.PrivyAppID, cfg.PrivyVerificationKey)
	if err != nil {
		return err
	}
	if privyAuth == nil {
		log.Info("Privy login disabled (PRIVY_APP_ID / PRIVY_VERIFICATION_KEY unset); using email/password + magic-link")
	}

	// Client IP is read as the Nth-from-the-right X-Forwarded-For hop so it can't
	// be spoofed to bypass rate limits (see middleware.ClientIP).
	middleware.SetTrustedProxies(cfg.TrustedProxyCount)
	limiter := store.NewRateLimiter(st.Redis)
	// Auth buckets must NOT fail open on a Redis blip (that would open a
	// brute-force / enumeration window), so they fail over to a per-instance
	// in-memory limiter instead of serving unthrottled.
	localRL := middleware.NewLocalLimiter()
	registerRL := middleware.RateLimitFailover(limiter, localRL, cfg.AuthRegisterLimit, time.Hour, middleware.IPKey("register"))
	loginRL := middleware.RateLimitFailover(limiter, localRL, cfg.AuthLoginLimit, time.Minute, middleware.IPKey("login"))
	// Per-user limiters on sensitive authenticated routes: money movement, the
	// outbound endpoint probe, and credential minting. Keyed by the token principal
	// (never the body), falling back to IP only when unauthenticated. Fails open on
	// a Redis blip like the other soft limits.
	userKey := func(ns string) middleware.KeyFunc {
		return func(r *http.Request) string {
			p := auth.PrincipalFromContext(r.Context())
			id := p.UserPublicID
			if id == "" {
				id = p.AgentPublicID
			}
			if id == "" {
				return "rl:" + ns + ":" + middleware.ClientIP(r)
			}
			return "rl:" + ns + ":u:" + id
		}
	}
	depositRL := middleware.RateLimit(limiter, 30, time.Minute, userKey("deposits"))
	withdrawRL := middleware.RateLimit(limiter, 20, time.Minute, userKey("withdrawals"))
	verifyRL := middleware.RateLimit(limiter, 12, time.Minute, userKey("manifest-verify"))
	keysRL := middleware.RateLimit(limiter, 10, time.Hour, userKey("agent-keys"))
	idHandler := identity.NewHandler(idSvc, authn, privyAuth, registerRL, loginRL, !cfg.IsProd(), xClaimEnabled, cfg.EmailDeliveryEnabled)
	idHandler.SetGoogle(auth.NewGoogleVerifier(cfg.GoogleClientID))                         // POST /v1/auth/google (disabled when GOOGLE_CLIENT_ID unset)
	idHandler.SetGitHub(auth.NewGitHubVerifier(cfg.GitHubClientID, cfg.GitHubClientSecret)) // POST /v1/auth/github (disabled unless GITHUB_CLIENT_ID + GITHUB_CLIENT_SECRET set)
	idHandler.SetKeysRateLimit(keysRL)
	// POST /v1/admin/agents — create an account with an explicit agent kind (the platform's
	// harness seats). Additive to the Platform token, like every other admin surface.
	idHandler.SetAdmins(cfg.AdminUserIDs)
	// Per-ACCOUNT credential throttle, alongside the per-IP loginRL above. Per-IP is
	// blind to a password list spread one-guess-per-host across a botnet, which never
	// trips any single IP bucket; keying on the identity under attack bounds what one
	// account can absorb regardless of how many sources the attempts come from.
	//
	// Failover to the local limiter for the same reason the IP buckets do: a Redis blip
	// must not open a brute-force window. Only a failure of BOTH serves unthrottled.
	idHandler.SetAccountRateLimit(func(ctx context.Context, identifier string) (bool, time.Duration) {
		key := "rl:login-account:" + identifier
		ok, retry, err := limiter.Allow(ctx, key, cfg.AuthAccountLimit, time.Hour)
		if err != nil {
			ok, retry, err = localRL.Allow(ctx, key, cfg.AuthAccountLimit, time.Hour)
			if err != nil {
				return true, 0
			}
		}
		return ok, retry
	})
	// Rotating refresh tokens: short-lived access JWT (above) + a long-lived,
	// single-use refresh token with a sliding idle window, so active users stay
	// signed in and idle ones are logged out after RefreshTokenTTL.
	refreshSvc := auth.NewRefreshService(store.NewRefreshRepo(st.DB), jwt, cfg.RefreshTokenTTL)
	idHandler.SetRefresh(refreshSvc)

	// Agent manifests: the metadata contract a developer submits per agent
	// version (info, supported games, hosted endpoint, runtime, model, SDK). The
	// hardened agentclient (SSRF-guarded) performs endpoint verification, and the
	// endpoint bearer token is sealed at rest with AES-256-GCM.
	// AllowPrivate implies AllowInsecure (a loopback stub is http). Both are refused
	// in prod by config.validate; log loudly if either is on so it's never a silent
	// dev-flag-in-a-real-env footgun.
	allowInsecure := cfg.AgentVerifyAllowInsecure || cfg.AgentVerifyAllowPrivate
	if cfg.AgentVerifyAllowPrivate || cfg.AgentVerifyAllowInsecure {
		log.Warn("agent endpoint verification SSRF guards relaxed (dev/e2e only)",
			"allow_private_ip", cfg.AgentVerifyAllowPrivate, "allow_insecure_http", allowInsecure)
	}
	manifest.AllowInsecureEndpoint = allowInsecure
	endpointSecretKey := cfg.AgentEndpointSecretKey
	if endpointSecretKey == "" {
		endpointSecretKey = cfg.APIKeyPepper // always-present fallback
	}
	manifestSealer, err := secretbox.New(endpointSecretKey)
	if err != nil {
		return err
	}
	manifestProbe := agentclient.New(agentclient.Config{
		Timeout:      cfg.AgentVerifyTimeout,
		MaxTimeout:   cfg.AgentVerifyMaxTimeout,
		Retries:      cfg.AgentVerifyRetries,
		MaxBodyBytes: cfg.AgentVerifyMaxBodyBytes,
		AllowPrivate: cfg.AgentVerifyAllowPrivate,
	})
	// Playing a turn is NOT probing an endpoint, and the two must not share a client.
	//
	// manifestProbe is tuned for verification: AGENT_VERIFY_TIMEOUT (5s) per attempt,
	// AGENT_VERIFY_RETRIES (2) attempts. Correct for /health and /handshake, which are
	// cheap and idempotent. It used to drive live match turns as well, and that was wrong
	// twice over:
	//
	//   1. It capped every decision at ~15s (3 × 5s + backoff) no matter what the game's
	//      shot clock said. With MOVE_WINDOW_SECONDS=60 configured, 19 of 26 recorded lab
	//      decisions were still logged as `timeout` at ~15.16s. Any model that thinks for
	//      longer than 5s — which is most reasoning models — had its real move silently
	//      replaced by a legal fallback. That then feeds the ranked integrity gate, which
	//      reads "no provably LLM-backed decisions" and voids the match or withholds the
	//      payout. A slow model is not a cheating model.
	//   2. It retried. A turn push is not idempotent from the agent's side: each attempt
	//      carries a fresh nonce and timestamp (see agentclient.attempt), so the SDK's
	//      replay dedupe cannot collapse them and the developer is billed for inference
	//      three times over for one turn.
	//
	// So: one attempt, deadline set by the game's own clock. A push that outlives the
	// window is moot anyway — the sweeper has already applied the deterministic fallback.
	// Timeout is the FALLBACK for a caller that sets no deadline; MaxTimeout is the
	// absolute ceiling a caller-supplied deadline is clamped to. They are different
	// numbers on purpose: the ceiling has to leave room for an adaptive window, or a slow
	// agent's computed budget is silently truncated back to the base and the whole
	// adaptive path is inert.
	newPlayClient := func(window time.Duration) *agentclient.Client {
		return agentclient.New(agentclient.Config{
			Timeout:      window,
			MaxTimeout:   deadline.MaxCeiling,
			Retries:      0,
			MaxBodyBytes: cfg.AgentVerifyMaxBodyBytes,
			AllowPrivate: cfg.AgentVerifyAllowPrivate,
		})
	}
	// Mafia's clock is per-phase rather than per-move; discussion is the longest, so it
	// sets the ceiling. An explicit MAFIA_PHASE_WINDOW_SECONDS override wins when set.
	mafiaPlayWindow := cfg.MafiaPhaseWindow
	if mafiaPlayWindow <= 0 {
		mafiaPlayWindow = mafiaengine.DiscussionDuration
	}
	goofspielPlayClient := newPlayClient(cfg.MoveWindow)
	mafiaPlayClient := newPlayClient(mafiaPlayWindow)
	log.Info("agent play clients configured (separate from the verification probe)",
		"goofspiel", cfg.MoveWindow, "mafia", mafiaPlayWindow,
		"retries", 0)
	manifestSvc := manifest.New(store.NewManifestRepo(st.DB), manifestProbe, manifestSealer)
	manifestHandler := manifest.NewHandler(manifestSvc, authn)
	// Benchmark agent metadata: resolve each agent's active manifest at match time so
	// the benchmark fact carries trusted, server-side version + declared model
	// provider/model.
	//
	// NOT gated on Lens. This used to sit behind `if lens.Enabled()`, but the durable
	// benchmark fact is written to the outbox unconditionally — so with telemetry off,
	// agent_version/provider/model were permanently BLANK in the Postgres rows that
	// feed the P-Index Intelligence dimension. An observability switch must never
	// silently corrupt product data.
	{
		benchMeta = func(ctx context.Context, agentPublicID string) benchmark.AgentMeta {
			m, err := manifestSvc.PublicActive(ctx, agentPublicID)
			if err != nil {
				return benchmark.AgentMeta{}
			}
			meta := benchmark.AgentMeta{Version: m.AgentVersion}
			if m.Model != nil {
				meta.Provider = m.Model.Provider
				meta.Model = m.Model.Model
			}
			return meta
		}
	}
	manifestHandler.SetRateLimit(verifyRL) // bound the outbound endpoint probe

	// Agent gateway: the Beta local-runtime transport. Developer agents dial OUT
	// over a persistent WebSocket (no inbound endpoint; a laptop behind NAT works),
	// authenticated by their manifest endpoint secret. The engine drives matches
	// over the socket via agentgw.*Decider, falling back deterministically if an
	// agent is absent/slow — the same guarantee the HTTP push client gives.
	agentGateway := newAgentGateway(manifestSvc, idSvc, platformCfg, lens, log, cfg.AgentReconnectGrace)

	// Domain event bus (transactional outbox): producers emit facts in their own
	// tx; this dispatcher fans them out to idempotent handlers. It is the backbone
	// for notifications, badges, and analytics (P1). Handlers registered here.
	eventBus := events.New(store.NewEventsRepo(st.DB), log, time.Second)
	// Pyyol Lens projection: every domain fact (match started/finished, rating,
	// pindex, certification, disputes…) becomes a trace/span in the observability
	// stack — an idempotent outbox handler like badges/notifications. No-op when
	// telemetry is disabled. Registered before the dispatcher starts (see NOTE).
	telemetrybridge.New(lens).Register(eventBus.On)
	eventBus.On(events.TypeAgentCertified, func(_ context.Context, e events.Event) error {
		log.Info("agent certified", "event", e.ID, "payload", string(e.Payload))
		return nil
	})
	eventBus.On(events.TypeSeasonRolled, func(_ context.Context, e events.Event) error {
		log.Info("season rolled", "event", e.ID, "payload", string(e.Payload))
		return nil
	})
	// Badges (reputation) are awarded off the event bus, idempotently.
	badgeSvc := badges.New(store.NewBadgesRepo(st.DB), log)
	eventBus.On(events.TypeAgentCertified, badgeSvc.OnAgentCertified)
	eventBus.On(events.TypeAgentGatewayVerified, badgeSvc.OnAgentGatewayVerified)
	eventBus.On(events.TypeSeasonRolled, badgeSvc.OnSeasonRolled)
	eventBus.On(events.TypeMatchFinished, badgeSvc.OnMatchFinished)
	// Developer (P-Index) badges: rank thresholds + consistency off pindex.updated;
	// streak/win-count/underdog off rating.updated. Idempotent, so at-least-once is safe.
	eventBus.On(events.TypePIndexUpdated, badgeSvc.OnPIndexUpdated)
	eventBus.On(events.TypeRatingUpdated, badgeSvc.OnRatingUpdated)
	// Cross-service event mirror: every delivered domain event is also appended to
	// the Redis stream the Super Admin consumes (live dashboard + analytics). It is
	// just another idempotent outbox handler — a publish failure leaves the event
	// unpublished for the dispatcher to retry, and consumers dedupe on event id.
	platformEvents := store.NewPlatformEventStream(st.Redis, enginePrivKey)
	// Only publish events the Super Admin mirror actually consumes. Deliberately
	// excluded: badge.awarded (never emitted — badges are DB+log only), and
	// season.rolled / pindex.updated (the mirror projects season/P-Index from its
	// 30s HTTP backfill, not the live delta — publishing them was wasted stream
	// volume the consumer dropped). Add one back here only when the mirror grows a
	// handler for it.
	for _, t := range []string{
		events.TypeAgentCertified, events.TypeMatchStarted, events.TypeMatchFinished,
		events.TypeDisputeOpened, events.TypeWithdrawalRequested,
		events.TypeRatingUpdated,
	} {
		eventBus.On(t, platformEvents.Publish)
	}
	// NOTE: the event dispatcher is launched later (search "event-dispatcher"), after
	// ALL handlers are registered — including the P-Index recompute handler, which
	// depends on the rating service constructed further down. Registering handlers
	// after Run starts would race the dispatcher's handler map.

	// Durable webhook delivery for the push protocol's async notifications
	// (/event + /game-end). Drive loops ENQUEUE; this central dispatcher delivers
	// them signed (HMAC), at-least-once with exponential backoff, and gated by a
	// per-endpoint circuit breaker (webhookHealth) so an unhealthy endpoint is
	// skipped rather than hammered. A continuous /health monitor feeds the breaker.
	// The engine never blocks on any of this. See internal/webhook.
	webhookQueue := store.NewWebhookRepo(st.DB)
	webhookHealth := webhook.NewHealthTracker(webhook.HealthConfig{})
	webhookDispatcher := webhook.NewDispatcher(webhookQueue, manifestSvc, manifestProbe, webhookHealth, log, webhook.Config{})
	webhookMonitor := webhook.NewMonitor(manifestSvc, manifestProbe, webhookHealth, log, 30*time.Second)
	launch("webhook-dispatcher", webhookDispatcher.Run)
	launch("webhook-monitor", webhookMonitor.Run)

	// Verification (built in Stage 1) is wired into the match flow now.
	//
	// It also gets the completion-binding evidence, so a cryptographic PROOF outranks the
	// statistical timing guess. The timing detector infers "a human is playing this by hand"
	// from response-time distribution; binding shows the gateway watched a model emit the move
	// and the match refuse anything else. Both answer the same question and one of them is
	// direct — which is why ~112k verification_pending flags sat on deterministic agents that
	// were provably not human, and why the matchmaker could not pair them.
	verSvc := verification.New(store.NewVerificationRepo(st.DB))
	verSvc.SetProvenShare(store.NewLLMGatewayRepo(st.DB))

	// Money: the ledger is the only coin-mover; the wallet service layers
	// stake/settle/refund and the seven spending limits on top, and serves the
	// read-only /v1/wallet surface. One wallet service satisfies both the match
	// Limits and Wallet ports stubbed in Stage 3.
	ledgerSvc := ledger.New(store.NewLedgerRepo(st.DB), metrics.Registry())
	walletSvc := wallet.New(ledgerSvc, store.NewWalletRepo(st.DB), clock,
		wallet.Config{SessionWindow: cfg.SessionWindow, CoinCents: cfg.CoinCents}, metrics.Registry())
	walletHandler := wallet.NewHandler(walletSvc, authn, cfg.AllowMint, cfg.AdminUserIDs)

	// Super Admin wallet controls (P4): runtime settings (deposit/withdrawal
	// switches, maintenance, bounds) + risk actions (freeze, manual adjust). Also
	// the deposit/withdrawal gate — wired into those services below via SetGate.
	walletAdminSvc := walletadmin.New(store.NewWalletAdminRepo(st.DB), walletSvc, clock, log)
	walletAdminHandler := walletadmin.NewHandler(walletAdminSvc, authn, cfg.AdminUserIDs)

	// Game stake tiers: Super-Admin-configurable price bands per game (e.g. Mafia
	// Low/Mid/High). Public read exposes the enabled menu; admin GET/PUT configures
	// it with immediate effect. Play handlers resolve a chosen tier -> coin stake.
	gameStakesSvc := gamestakes.New(store.NewGameStakesRepo(st.DB), clock, log)
	// Stakes are administered in dollars (the unit an operator thinks in) and stored
	// in coins (the unit the ledger settles in). The peg makes that translation; the
	// floor keeps a paid table above the point where the rake rounds to nothing.
	gameStakesSvc.SetCoinCents(cfg.CoinCents)
	gameStakesSvc.SetMinStakeUSDCents(cfg.MinStakeUSDCents)
	gameStakesHandler := gamestakes.NewHandler(gameStakesSvc, authn, cfg.AdminUserIDs)

	// Wallet-ownership verification: prove control of the payout wallet (sign a
	// nonce) before a withdrawal can be sent there (payout gates on the result).
	walletVerifyRepo := store.NewWalletVerifyRepo(st.DB)
	walletVerifySvc := walletverify.New(walletVerifyRepo, clock)
	// Recording WHICH wallet the browser connected. Display hints only — the address is
	// self-reported, so it can never be a payout destination on its own, and the hint is
	// filled rather than repointed so a casual connect cannot break an established
	// payout pairing. See walletverify/connected.go.
	walletVerifySvc.SetConnectedRepo(walletVerifyRepo)
	walletVerifyHandler := walletverify.NewHandler(walletVerifySvc, authn)

	// Free, self-hosted TOTP two-factor: enrollment + step-up on money movement. The
	// secret is encrypted at rest with a cipher keyed on the always-present API-key
	// pepper (no new mandatory secret). Wired as the step-up check on withdrawals and
	// on withdrawal-wallet changes.
	totpCipher, err := secretbox.New(cfg.APIKeyPepper)
	if err != nil {
		return err
	}
	twofaSvc := twofa.New(store.NewTwoFARepo(st.DB), totpCipher, clock, "pyyol", cfg.APIKeyPepper)
	twofaHandler := twofa.NewHandler(twofaSvc, authn)
	// 2FA (when the user has enabled it) is required on the money-sensitive steps:
	// verifying/changing the payout wallet AND cashing out. Both are step-up gated.
	walletVerifyHandler.SetStepUp(twofaSvc)
	idHandler.SetTwoFAStatus(twofaSvc.Enabled) // surface the 2FA preference in GET /v1/me

	// Trust & anti-fraud: the payout gate holds suspect settlements (escrow kept),
	// the detector flags collusion/human-timing, and disputes drive admin review.
	// SetPayoutGate wires it into settlement after construction (the wallet is the
	// gate's settler, so they can't both be constructor args).
	antifraudSvc := antifraud.New(store.NewAntifraudRepo(st.DB), walletSvc, clock,
		antifraud.Config{CollusionMinGames: cfg.CollusionMinGames, DetectLookback: cfg.CollusionLookback},
		log, metrics.Registry())
	walletSvc.SetPayoutGate(antifraudSvc)
	antifraudSvc.SetClawback(walletSvc) // record fraud clawback debt on a new collusion flag (M4)
	antifraudHandler := antifraud.NewHandler(antifraudSvc, authn, cfg.AdminUserIDs)

	// Spectator: the SSE hub is the real Broadcaster (replaces the Stage 3 no-op).
	// It reuses the match repo as its Last-Event-ID backlog source. Fan-out is
	// non-blocking + drop-slow, so a stalled watcher can never delay a match.
	matchRepo := store.NewMatchRepo(st.DB)
	hub := spectator.NewHub(matchRepo, cfg.DefaultRounds, log, metrics.Registry())
	hub.SetMaxConns(cfg.SSEMaxConns) // instance-wide SSE ceiling; LB spreads the rest
	specHandler := spectator.NewHandler(hub, spectator.NewLive(store.NewSpectatorRepo(st.DB), clock))

	// Mafia hub (demo loop + DB-backed SSE). Service wired after engagement hooks.
	mafiaRepo := store.NewMafiaRepo(st.DB)
	mafiaHub := mafia.NewHub(mafiaRepo, log, metrics.Registry())
	launch("mafia-hub", mafiaHub.Run)

	// Ratings & profiles: ELO is applied at match finalize (idempotently, keyed by
	// match id) and powers the leaderboard + agent profiles + /v1/agent/stats.
	ratingSvc := rating.New(store.NewRatingRepo(st.DB), clock,
		rating.Config{SeasonLength: cfg.SeasonLength}, metrics.Registry())
	ratingHandler := rating.NewHandler(ratingSvc, authn, cfg.AllowMint, cfg.AdminUserIDs)  // dev-only season force-roll gated with mint
	launch("season-roller", rating.NewSeasonRoller(ratingSvc, log, time.Minute).Run)       // finalise ended seasons + emit season.rolled
	launch("rank-snapshotter", rating.NewRankSnapshotter(ratingSvc, log, 6*time.Hour).Run) // daily rank snapshot → leaderboard trend
	styleRepo := store.NewStyleRepo(st.DB)                                                 // read-only behavioral style aggregates (goofspiel)
	profilesSvc := profiles.New(store.NewProfilesRepo(st.DB), ratingSvc.CurrentSeason)
	profilesSvc.SetManifest(profileManifest{manifestSvc}) // certification + declared-capability card on profiles
	profilesSvc.SetStyleReader(styleRepo)                 // aggression/efficiency on the profile
	profilesHandler := profiles.NewHandler(profilesSvc, authn)

	// P-Index: the developer-reputation composite. rating.updated marks affected
	// developers dirty; a background worker recomputes their P-Index through the
	// modular scoring engine and refreshes the season ranking (off the hot path).
	pindexRepo := store.NewPIndexRepo(st.DB)
	pindexSvc := pindex.New(pindexRepo, ratingSvc.CurrentSeason, clock, log)
	// Publish the P-Index method, and version it the way documentation is versioned.
	//
	// The methodology endpoint is deliberately public: a reputation number a developer
	// cannot check the method for is one they are asked to trust. The weights come from the
	// active config row and the formulas from each dimension's own Explain, so the page
	// cannot describe a formula the engine is not running.
	pindexHandler := pindex.NewHandler(pindexRepo, pindex.NewEngine(), authn, cfg.AdminUserIDs)

	// The MODEL board: which model plays best with the developer's harness held constant.
	//
	// Refreshed on an interval rather than per request. One fit is a regularized optimization plus
	// a thousand bootstrap replicates — seconds of CPU that grow with the season — so computing it
	// per reader would make the board its own denial of service. A snapshot also means every reader
	// in a window sees the SAME fit, so two people comparing screenshots are not looking at two
	// different boards.
	//
	// 90 days of matches: long enough for the within-harness pairings the estimator needs, short
	// enough that a model's rating reflects how it plays now rather than a year ago.
	modelBoardSvc := modelboard.NewService(store.NewModelBoardRepo(st.DB), 90*24*time.Hour, log)
	modelBoardRepo := store.NewModelBoardRepo(st.DB)
	// Per-day history, so a rating can be shown as a series. Wired now rather than when the chart
	// is built: history is the one thing that cannot be backfilled — a fit describes the matches
	// that existed at a moment, and that moment does not come again.
	modelBoardSvc.SetHistoryWriter(modelBoardRepo)
	// WHICH upstreams may be attributed to a model. Defaults to the vendor endpoints this
	// binary ships, so a production deployment behaves exactly as before; BENCHMARK_EXTRA_HOSTS
	// declares a legitimate override (an enterprise egress proxy, a self-hosted vLLM whose
	// results the operator genuinely wants ranked).
	//
	// What it stops: a lab that points anthropic at a local stand-in records bound,
	// well-formed `anthropic / claude-opus-4` calls that never left the machine. Without this
	// they are indistinguishable from real ones and would be ranked as a model.
	modelBoardSvc.SetPublishableHosts(publishableHosts())
	// Names this instance's history series. The platform harness benchmark runs the same fit
	// over its own matches and writes the same shape of row; the discriminator is what keeps
	// the two series independent instead of one silently overwriting the other.
	modelBoardSvc.SetBoard("developer")
	modelBoardHandler := modelboard.NewHandler(modelBoardSvc)
	modelBoardHandler.SetHistoryReader(modelBoardRepo)
	modelBoardHandler.SetBoard("developer")

	// The platform harness board is gone, and the developer board above is the survivor.
	//
	// It was always the stronger of the two. The developer board requires `m.rated`, which
	// excludes any table a house bot had to fill; the harness board could not use that filter
	// — its own matches are unrated by design — and approximated it structurally instead.
	//
	// Nothing about the measurement was lost with it. Both ran the same Build, the same
	// Bradley-Terry estimator, the same bootstrap intervals and the same attribution rule.
	// The harness board was that machinery pointed at platform-run agents, so removing it
	// removes a data source, not a method.

	// The two boards refresh on DIFFERENT intervals, sized to what each one costs.
	//
	// The harness board reads the platform's own benchmark seats — a few dozen in the
	// window — and its refresh is scoped to them, so it costs ~113 MB and under a second.
	// Ten minutes is comfortable.
	//
	// The developer board reads every developer seat in a 90-day window, which is ~320,000
	// of them, and one refresh measured 16.7 MINUTES and ~20 GB of reads. On a ten-minute
	// interval a tick was always already waiting, so it refreshed back to back forever:
	// four refreshes accounted for 208 GB of reads, and the board was effectively a
	// permanent table scan wearing a schedule.
	//
	// An hour is the honest interval for it. A leaderboard computed over ninety days does
	// not change meaningfully in ten minutes, so the shorter period bought nothing a viewer
	// could perceive and cost the disk continuously. This is a mitigation and not the cure —
	// the query returns 600k rows to be aggregated in Go, and that is the thing to fix — so
	// the worker now WARNS when a refresh outlasts its interval rather than letting the next
	// regression hide the same way.
	// Coverage rollup. Frequent ticks, small batches: the public benchmark endpoints read this
	// instead of aggregating the decision history per request, which is what made them hang.
	launch("coverage-rollup",
		store.NewCoverageWorker(store.NewCoverageRepo(st.DB), time.Minute, 24*time.Hour, log).Run)
	launch("modelboard", modelboard.NewWorker(modelBoardSvc, time.Hour, log).Run)
	// Ledger integrity, on a schedule. The double-entry invariants were verified by hand and held
	// (960 transactions, 2873 entries, 152 wallets, nothing unbalanced), but that is a statement
	// about one afternoon. An imbalance is SILENT — per-wallet balances still add up, the UI still
	// renders, and the first external symptom is a user disputing a payout. Read-only; it reports
	// and never repairs, because writing to a ledger that has just been proved untrustworthy would
	// destroy the evidence of how it broke.
	launch("ledger-audit", ledger.NewAuditWorker(ledgerSvc, 15*time.Minute, log).Run)
	// P-Index Intelligence projection: fold each match.benchmark seat into the
	// per-match decision-quality aggregate the recompute reads (legal/fallback/
	// latency). Best-effort: a decode failure never wedges the outbox.
	eventBus.On(events.TypeMatchBenchmark, func(ctx context.Context, e events.Event) error {
		ms, _, err := benchmark.DecodePayload(e.Payload)
		if err != nil {
			return nil
		}
		for _, seat := range ms.Seats {
			if seat.AgentID == "" || seat.Decisions == 0 {
				continue
			}
			// The model this seat ACTUALLY called, recovered from the per-move usage in
			// its decision log. Persisted alongside the manifest's claim so the public
			// board can rank on what ran rather than on what was declared — and can say
			// which of the two it is using.
			obsProvider, obsModel := seat.ObservedModel()
			if err := pindexRepo.RecordMatchBenchmark(ctx, store.MatchBenchmarkFact{
				MatchID: ms.MatchID, AgentPublicID: seat.AgentID, Game: ms.Game, Result: string(seat.Result),
				Decisions: int(seat.Decisions), Legal: int(seat.Legal), Fallbacks: int(seat.Fallbacks),
				Illegal: int(seat.Illegal), Timeouts: int(seat.Timeouts), TransportErrors: int(seat.TransportErrors),
				LatencySumMS: seat.LatencySumMS, LatencyMinMS: seat.LatencyMinMS, LatencyMaxMS: seat.LatencyMaxMS,
				Tokens: seat.TotalTokens, PromptTokens: seat.PromptTokens,
				CompletionTokens: seat.CompletionTokens, ReasoningTokens: seat.ReasoningTokens,
				CachedTokens: seat.CachedTokens, EstimatedCost: seat.EstimatedCost,
				ObservedProvider: obsProvider, ObservedModel: obsModel,
				DeclaredProvider: seat.Provider, DeclaredModel: seat.Model,
			}); err != nil {
				return err // let the outbox retry
			}

			// The PER-DECISION record, from the same payload. This log was already being
			// carried on the event and emitted to Lens, then dropped — so with Lens
			// unconfigured (the default) a developer's match trace could show what
			// HAPPENED but never what their agent chose, why, or what the move cost.
			// That is the whole content of debugging an agent.
			if decisions := store.DecisionsFromSeat(seat, obsProvider, obsModel); len(decisions) > 0 {
				if err := pindexRepo.RecordMatchDecisions(ctx, ms.MatchID, seat.AgentID, decisions); err != nil {
					return err // let the outbox retry
				}
			}
		}
		return nil
	})
	eventBus.On(events.TypeRatingUpdated, pindexSvc.OnRatingUpdated)
	launch("pindex-recompute", pindex.NewWorker(pindexSvc, log, 5*time.Second).Run)

	// Decision-quality scoring. Runs OFF the match path deliberately: it is a pure
	// function of columns already persisted (input_json + action), so computing it inline
	// would add a CPU-heavy regret-matching solve to a live turn for an answer that is
	// identical whenever it is produced. As a batch it also back-fills every historical
	// decision and can rescore everything on a scorer improvement (bump skill.ScorerVersion)
	// with no migration and no replay.
	//
	// Feeds the P-Index "skill" dimension, which ships at weight 0 — the scores are
	// measured and shown but change nobody's ranking until an operator weights them.
	launch("skill-scoring", skill.NewWorker(store.NewSkillRepo(st.DB), skill.WorkerConfig{}, log).Run)

	// Public developer reputation surface (@handle profile, P-Index transparency,
	// match history, developer follow graph), aggregated across a developer's agents.
	devProfileRepo := store.NewDevProfileRepo(st.DB)
	devProfileSvc := devprofile.New(devProfileRepo, pindexSvc, ratingSvc.CurrentSeason)
	devProfileSvc.SetCoinCents(cfg.CoinCents) // price lifetime earnings in USD
	// Profile completion is derived from account state, and one of its steps is "have
	// you connected a wallet" — so the checklist needs to be able to read that.
	devProfileSvc.SetWalletReader(devProfileRepo)
	// Serve username availability from memory instead of a lookup per keystroke. The
	// route is public, unauthenticated and unthrottled; see CheckUsername for why a
	// Bloom filter is the safe shape for it and what the staleness costs.
	go devProfileSvc.RunUsernameFilter(ctx, cfg.UsernameFilterRefresh, log)
	devProfileHandler := devprofile.NewHandler(devProfileSvc, authn)

	// User-uploaded media (avatars) on S3/MinIO. The prod stack has shipped the bucket
	// and the credentials for a while; this is the first thing that writes to it, which
	// is why a profile photo used to exist only in the browser that uploaded it.
	//
	// An unconfigured store does NOT stop the arena: uploads answer 503 and everything
	// else runs, because a platform that will not boot without object storage is worse
	// than one that cannot take a photo.
	mediaStore := media.New(media.Config{
		Endpoint:   cfg.S3Endpoint,
		Bucket:     cfg.S3Bucket,
		AccessKey:  cfg.S3AccessKey,
		SecretKey:  cfg.S3SecretKey,
		Region:     cfg.S3Region,
		PublicBase: cfg.MediaPublicBase,
	})
	if mediaStore.Enabled() {
		// Create the bucket if a fresh volume has none. Advisory: logged, never fatal.
		bctx, bcancel := context.WithTimeout(ctx, 10*time.Second)
		if err := mediaStore.EnsureBucket(bctx); err != nil {
			log.Error("media: object storage unusable — avatar uploads will fail",
				"endpoint", cfg.S3Endpoint, "bucket", cfg.S3Bucket, "error", err)
		} else {
			log.Info("media: object storage ready", "endpoint", cfg.S3Endpoint, "bucket", cfg.S3Bucket)
		}
		bcancel()
	} else {
		log.Warn("media: object storage not configured — avatar uploads disabled " +
			"(set S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY, S3_SECRET_KEY)")
	}
	mediaHandler := media.NewHandler(mediaStore, devProfileRepo, authn, cfg.BaseURL, log)

	// A developer's read-back of their OWN agent's traces. Ownership is resolved
	// here (Postgres is the only place that knows it) and the visibility allowlist
	// is applied twice — see internal/devtrace.
	//
	// The PRIMARY source is the arena's own match log, wired via SetLocalRepo. The Lens is
	// enrichment on top: it adds connection lifecycle and endpoint checks, and when it is
	// unconfigured, unreachable, or refusing our key the page still shows every decision,
	// every line of table talk and every result, because those come from Postgres. Before
	// this, /traces had exactly the availability of a separate service on a separate host
	// and spent most of its life reporting that the trace store was unreachable.
	//
	// The Lens READ key is PyyolLensQueryAPIKey, not the ingest key: it gates its two planes
	// on separate secrets, so sending the ingest key here 401s every read.
	devTraceRepo := store.NewDevTraceRepo(st.DB)
	devTraceSvc := devtrace.New(devTraceRepo, cfg.PyyolLensQueryEndpoint,
		cfg.PyyolLensQueryAPIKey, cfg.PyyolLensOrg, log)
	devTraceSvc.SetLocalRepo(devTraceRepo)
	// The paginated game history + per-match record (/v1/developer/matches). Same repo,
	// separate port: those are aggregate queries over matches rather than a scan of the
	// event log, and the history must not be reachable when it has no source.
	devTraceSvc.SetMatchRepo(devTraceRepo)
	// Cross-match agent telemetry (failure taxonomy, latency percentiles, hotspots).
	devTraceSvc.SetTelemetryRepo(devTraceRepo)
	devTraceHandler := devtrace.NewHandler(devTraceSvc, authn)

	// All event handlers are now registered — start the dispatcher (see the NOTE at
	// its handler-registration block above).
	launch("event-dispatcher", eventBus.Run)

	// Engagement: clips (dramatic-moment detection + async asset render) and social
	// (follows + notification fan-out). Both run on bounded worker pools off the
	// hot path; the match finish hook only enqueues (never blocks finalize).
	clipsSvc := clips.New(store.NewClipsRepo(st.DB), matchRepo, clips.NewDevGenerator(cfg.ClipCDNBase),
		clips.Config{}, log, metrics.Registry())
	clipsHandler := clips.NewHandler(clipsSvc)
	socialRepo := store.NewSocialRepo(st.DB)
	socialSvc := social.New(socialRepo, social.Config{}, log, metrics.Registry())
	socialHandler := social.NewHandler(socialSvc, authn)

	// Realtime user event rail. Every money event already produced a durable
	// notifications row and then stopped there — the browser learned about it on its
	// next 60-second bell poll or on a full reload, which is why a successful deposit
	// showed no confirmation and no balance change. The bus pushes the SAME event to
	// the user's open tabs immediately; the row remains the source of truth.
	//
	// Redis pub/sub, so it is cross-instance. Best-effort throughout: no money path
	// blocks on it and none of them fail if it is unavailable.
	userBus := userevents.NewBus(st.Redis, log)
	userSignals := userevents.NewSignaller(2, 256, log)
	launch("user-event-signals", userSignals.Run)
	userEventsHandler := userevents.NewHandler(userBus, authn)

	// Notification writer shared by the deposit + withdrawal flows (idempotent).
	// It now writes the row AND pushes it live, in that order, and pushes only when
	// the insert actually inserted — so a redelivered webhook or a re-observed
	// on-chain transfer cannot re-toast an event the user already acknowledged.
	notifier := notifierAdapter{repo: socialRepo, bus: userBus}
	socialSvc.SetPusher(notifier) // match results reach the tab that is watching
	// A new follower is a notification like any other: persisted for the bell, mirrored
	// onto the open tab so the count moves without a refresh.
	devProfileSvc.SetFollowAnnouncer(notifier)
	walletSvc.SetEventSink(walletEvents{bus: userBus, sig: userSignals, notifier: notifier, log: log})

	// The payment log. Every money flow records which stage it reached, so
	// "my payment did not work" is answerable from the product instead of from
	// server logs joined by hand. Diagnostic only: the ledger stays the authority,
	// and a failed write here can never fail a payment (see paymenttrace.Record).
	paymentTrace := paymenttrace.New(store.NewPaymentTraceRepo(st.DB), log)
	paymentTraceHandler := paymenttrace.NewHandler(paymentTrace, authn, adminIDSet(cfg.AdminUserIDs))
	walletSvc.SetTracer(paymentTrace)

	// Receipts. Derived on read from the deposits/top-ups/withdrawals that already
	// happened — there is no invoices table, so a document can never disagree with
	// the money it describes.
	invoicesHandler := invoices.NewHandler(
		invoices.New(store.NewInvoicesRepo(st.DB, cfg.CoinCents), invoices.Config{
			CoinCents:  cfg.CoinCents,
			ExplorerTx: cfg.SolanaExplorerTx,
			Issuer:     "Pyyol",
		}),
		authn,
	)

	// The live platform commission, read fresh for each new match from the config
	// bus. Until now this value was published by the Super Admin, seeded into the
	// snapshot, and read by NOTHING: Goofspiel used RAKE_PCT (5), Mafia and Monopoly
	// were hardcoded to 10, and the admin's fee control moved no money at all. The
	// three games now agree, and the control is real.
	liveRake := func(fallback int) func() int {
		return func() int { return platformCfg.Get().CommissionPct(fallback) }
	}
	// The rest of the economy, read the same way. Every one of these was published by
	// the Super Admin and read by NOTHING until now: the operator could set a coin
	// price, a withdrawal fee or a deposit minimum and the arena would keep serving
	// its own env vars. Each accessor is bounded, since these cross a service
	// boundary and a corrupt publisher must not be able to set a 100% fee.
	liveCashout := func() (int, int64) {
		snap := platformCfg.Get()
		return snap.WithdrawFeePct(cfg.WithdrawSellFeePct),
			snap.MinWithdrawalCoins(cfg.WithdrawMinCoins, cfg.CoinCents)
	}
	liveMinDepositCents := func() int64 {
		return platformCfg.Get().MinDepositCents(cfg.DepositMinUSDC * 100)
	}
	liveMinStakeUSDCents := func() int64 {
		return platformCfg.Get().MinStakeUSDCents(cfg.MinStakeUSDCents)
	}
	// The paid-table floor is policy, not a constant: an operator moves it from the
	// admin without a redeploy. Env stays the fallback for a bus-less deployment.
	gameStakesSvc.SetMinStakeSource(liveMinStakeUSDCents)

	mafiaSvc := mafia.NewService(
		mafiaRepo,
		store.NewLocker(st.Redis),
		walletSvc,
		wallet.NewMafiaWallet(walletSvc),
		mafiaHub,
		verifierAdapter{v: verSvc, cert: manifestSvc, susp: platformCfg, conn: agentGateway.Connected},
		finishHook{clips: clipsSvc, social: socialSvc},
		clock,
		// No hardcoded economics here: the stake comes from the admin's tiers (see
		// SetDefaultStakeSource / SetRakeSource below) and these are only the
		// fallbacks used when the admin has configured nothing at all.
		mafia.Config{EntryFee: mafia.DefaultEntryFee, PlatformFeePct: cfg.RakePct, PhaseWindow: cfg.MafiaPhaseWindow, LockTTL: 15 * time.Second},
	)
	mafiaSvc.SetRakeSource(liveRake(cfg.RakePct))
	mafiaSvc.SetDefaultStakeSource(func(ctx context.Context) (int64, bool) {
		return gameStakesSvc.LowestEnabledCoins(ctx, "mafia")
	})
	mafiaSvc.SetRater(ratingSvc) // paid tables update the per-arena Mafia rating (TrueSkill)
	// A seat that proved no LLM-backed decision is not paid from a STAKED table. Inert
	// until proofs actually exist (see internal/integrity), so it is safe on by default.
	mafiaSvc.SetIntegrityChecker(store.NewPIndexRepo(st.DB))
	// The proof minter must be installed BEFORE EnablePushPlay copies it onto the push
	// player. Without it Mafia views carry no turn_proof, so no decision can be counted as
	// LLM-backed and the check above can never arm — which is exactly the state Mafia and
	// Monopoly were in: the check installed, the evidence never produced.
	mafiaSvc.SetTurnMinter(turnproof.New(cfg.TurnProofSecret))
	mafiaHandler := mafia.NewHandler(mafiaHub, mafiaSvc, authn)
	mafiaHandler.SetStakeResolver(gameStakesSvc) // Low/Mid/High tier → stake, budget-checked
	// AND on the service: the bot runner calls CreateTable directly, and mafia's own
	// DefaultEntryFee of 100 is substituted when no fee is given — both below the 500 floor.
	mafiaSvc.SetStakeFloor(gameStakesSvc)
	launch("mafia-sweeper", mafia.NewSweeper(mafiaSvc, log, time.Second).Run)


	// Mafia push-play: like monopoly, ALWAYS on (not gated on DEMO_BOTS) so it works in
	// prod with the live arena clean. The 11 filler seats are dedicated kind='house'
	// bots (seeded by migration 0063), engine-driven in the drive loop — never LLM,
	// never rated, never in the leaderboard/lobby. Only the autonomous demo-bot runner
	// (below) stays gated on DEMO_BOTS.
	mafiaHouseBots := make([]mafia.BotAgent, 0, mafiaengine.RosterSize-1)
	for i := 1; i <= mafiaengine.RosterSize-1; i++ {
		mafiaHouseBots = append(mafiaHouseBots, mafia.BotAgent{
			PublicID:      fmt.Sprintf("ag_house_mafia_%02d", i),
			OwnerPublicID: "usr_system",
		})
	}
	mafiaSvc.EnablePushPlay(manifestSvc, mafiaPlayClient, mafiaHouseBots, log)
	mafiaSvc.SetWebhookEnqueuer(webhookQueue)
	mafiaSvc.SetGateway(agentGateway)                    // play over the socket when the agent is connected
	mafiaSvc.SetBenchmark(lens, benchPersist, benchMeta) // per-match decision-quality telemetry
	// Request-path instrumentation, as for Monopoly: without it a Mafia match played by polling
	// state and posting actions produces no benchmark fact, no decision log and no board
	// presence. See OBSERVABILITY_COVERAGE_GAP.md.
	mafiaSvc.SetActDecisionRecorder(mafiaActRecorder{repo: pindexRepo})

	// Monopoly is OFF: no routes, and — the part that matters — no sweeper.
	//
	// The sweeper polled every second for tables whose turn deadline had lapsed and
	// advanced them. That is correct for a table someone is still playing, and ruinous
	// for one nobody is: an abandoned match keeps being advanced, one forced turn per
	// 60-second timeout, until it reaches the 1000-turn cap.
	//
	// Measured on a real one (mp_mkbmjjwqoajggqbq): the agent stopped answering at turn
	// 80, and six hours later the arena had dealt that dead seat 314 more turns and was
	// still going — 2034 events, turn 394 of 1000, roughly ten hours left to run. Worse,
	// it was invisible: /v1/games reported monopoly live=0 the whole time, because the
	// live counter only sees matches with a connected agent. So the load accumulated
	// with nothing on the platform showing it.
	//
	// The symptom was the API: about one request in ten hanging for 21 seconds, and
	// WebSocket connects timing out, which made Goofspiel and Mafia untestable.
	//
	// Removing the sweeper is what stops it. A restart would not have: the state lives
	// in the database, so the sweeper picks the same matches straight back up.
	//
	// This is the entry-point cut only. The engine, service, telemetry, SDKs, docs and
	// UI come out separately — deliberately, because the server needed to be healthy
	// before a change that size, not after it.

	// Funded freeroll (Stage 10): the prize pool moves through the ledger via the
	// Bank adapter; entry is gated on the tournament_ready badge + no fraud flags.
	tournamentSvc := tournament.New(store.NewTournamentRepo(st.DB), tourneyBank{ledgerSvc}, metrics.Registry())
	tournamentHandler := tournament.NewHandler(tournamentSvc, authn, cfg.AdminUserIDs)

	// Cash-out (coins → money): request → admin approve → Stripe payout. Coins are
	// held in escrow on request and burned only on a confirmed payout. The live
	// Stripe transferrer is selected whenever a secret key is configured.
	var transferrer payout.Transferrer = payout.DevTransferrer{}
	payoutChain := payout.ChainStripe
	var solanaXfer *payout.SolanaTransferrer
	switch {
	case cfg.WithdrawalsSolana():
		// Solana USDC cash-out: the hot wallet signs a real USDC transfer to the
		// user's wallet; coins burn only after the tx finalizes (confirm watcher).
		// The signing key is decrypted at rest (secretbox) when the encrypted form is
		// configured; plaintext is dev-only and warned about in prod (W3).
		hotSecret, err := payout.ResolveHotWalletSecret(cfg.SolanaHotWalletSecret, cfg.SolanaHotWalletSecretEnc, cfg.SolanaHotWalletEncKey)
		if err != nil {
			return err
		}
		if cfg.IsProd() && cfg.SolanaHotWalletSecretEnc == "" {
			log.Warn("hot-wallet key is a PLAINTEXT env var in prod — set SOLANA_HOT_WALLET_SECRET_ENC (see cmd/wallet-secret-encrypt)")
		}
		// Payouts are signed FROM cfg.PayoutATA(): the deposit account unless
		// SOLANA_PAYOUT_ATA splits custody, in which case user deposits accumulate in an
		// account this process holds no key for and this wallet carries only a float.
		sx, err := payout.NewSolanaTransferrer(cfg.SolanaRPCURL, hotSecret, cfg.SolanaUSDCMint, cfg.PayoutATA(), 6)
		if err != nil {
			return err
		}
		transferrer, solanaXfer, payoutChain = sx, sx, payout.ChainSolana
		log.Info("withdrawals: solana USDC rail enabled",
			"mint", cfg.SolanaUSDCMint, "payout_ata", cfg.PayoutATA(),
			"hot_wallet", sx.HotPublicKey(), "custody_split", cfg.CustodySplit())
	case cfg.StripeSecretKey != "":
		transferrer = payout.NewStripeTransferrer(cfg.StripeSecretKey)
	}
	payoutSvc := payout.New(store.NewPayoutRepo(st.DB), payoutBank{ledgerSvc}, transferrer, clock,
		payout.Config{
			CoinCents: cfg.CoinCents, SellFeePct: cfg.WithdrawSellFeePct,
			StripeFeePct: cfg.StripePayoutFeePct, StripeFeeFlatCents: cfg.StripePayoutFeeFlatCents,
			MinCoins: cfg.WithdrawMinCoins, Clearing: cfg.WithdrawClearing, Chain: payoutChain,
			VelocityWindow: cfg.WithdrawVelocityWindow, MaxPerWindow: cfg.WithdrawMaxPerWindow,
			MaxCentsPerWindow: cfg.WithdrawMaxCentsPerWindow, NewAddressCooldown: cfg.WithdrawNewAddressCooldown,
		}, log, metrics.Registry())
	// Cash-out economics from the admin, resolved when a withdrawal is REQUESTED and
	// persisted on the row, so settlement never re-prices what the user was quoted.
	payoutSvc.SetEconomySource(liveCashout)
	// Platform-wide payout breaker. Every other control here is per-owner and cannot
	// see the shape that actually empties a treasury: many accounts each withdrawing
	// a legal amount at once. Fails closed and stays closed until an admin resumes.
	payoutSvc.SetBreaker(&payout.Breaker{
		Window:           cfg.PayoutBreakerWindow,
		WindowCents:      cfg.PayoutBreakerWindowCents,
		SpikeMultiple:    cfg.PayoutBreakerSpike,
		BaselineWindows:  cfg.PayoutBreakerBaselineN,
		MinBaselineCents: cfg.PayoutBreakerMinBaseline,
	}, store.NewPayoutRepo(st.DB))
	if solanaXfer != nil {
		// The transferrer also confirms finality; the watcher burns/releases escrow
		// once each broadcast withdrawal reaches a terminal on-chain state.
		payoutSvc.SetConfirmer(solanaXfer)
		launch("payout-confirm-watcher", payout.NewConfirmWatcher(payoutSvc, log, cfg.WithdrawConfirmInterval).Run)
	}
	payoutSvc.SetGate(walletAdminSvc) // Super Admin withdrawal gate (settings/freeze)
	payoutSvc.SetNotifier(notifier)   // notify owner on paid/failed
	payoutSvc.SetTracer(paymentTrace) // record which stage each cash-out reached
	payoutHandler := payout.NewHandler(payoutSvc, authn, cfg.AdminUserIDs)
	payoutHandler.SetRateLimit(withdrawRL)
	payoutHandler.SetStepUp(twofaSvc) // require the 2FA code on cash-out when enabled

	// Deposits (Beta wallet pipeline P2): a background listener watches Solana for
	// USDC transfers to the platform token account (tagged by each session's
	// Solana Pay reference) and credits the user's treasury via the same ledger
	// top-up path (peg derived from CoinCents: 1 USDC = 100/CoinCents coins).
	// Enabled only when fully configured; otherwise /v1/deposits returns 503.
	var depositHandler *solanadeposit.Handler
	// Declared out here so the admin overview can report the REAL on-chain treasury.
	// Stays nil when payouts are unconfigured, which the overview renders as "no
	// observation" rather than as a zero balance.
	var solvencyMonitor *payout.SolvencyMonitor
	if cfg.DepositsEnabled() {
		coinsPerUSDC := int64(100)
		if cfg.CoinCents > 0 {
			coinsPerUSDC = 100 / cfg.CoinCents
		}
		chain := blockchain.New(blockchain.Config{RPCURL: cfg.SolanaRPCURL, Commitment: cfg.SolanaCommitment})
		depositSvc := solanadeposit.New(store.NewDepositRepo(st.DB), chain, walletSvc, clock,
			solanadeposit.Config{
				USDCMint: cfg.SolanaUSDCMint, PlatformOwner: cfg.SolanaPlatformOwner,
				PlatformATA: cfg.SolanaPlatformATA, CoinsPerUSDC: coinsPerUSDC, USDCDecimals: 6,
				SessionTTL: cfg.DepositSessionTTL, MinDepositBase: cfg.DepositMinUSDC * 1_000_000,
			}, log)
		depositSvc.SetGate(walletAdminSvc) // Super Admin deposit gate (settings/freeze)
		depositSvc.SetMinDepositSource(liveMinDepositCents)
		// The entry fee is now admin-controlled too, so the economy screen owns BOTH
		// sides of the round trip rather than only the way out.
		depositSvc.SetDepositFeeSource(func() int {
			return platformCfg.Get().DepositFeePct(cfg.DepositFeePct)
		})
		depositSvc.SetNotifier(notifier) // notify user when a deposit is credited
		depositSvc.SetTracer(paymentTrace)
		depositHandler = solanadeposit.NewHandler(depositSvc, authn)
		depositHandler.SetRateLimit(depositRL)
		launch("solana-deposit-listener", solanadeposit.NewListener(depositSvc, log, cfg.DepositPollInterval).Run)
		// Read-only solvency monitor: reconcile the on-chain USDC against outstanding
		// withdrawal liability and alert on any shortfall (never moves funds). Watches the
		// PAYOUT account, because that is the balance a cash-out actually draws on.
		solvency := payout.NewSolvencyMonitor(store.NewPayoutRepo(st.DB), chain, cfg.PayoutATA(), log, metrics.Registry())
		// Cap the float. Anything above this should live at a cold address this process
		// holds no key for; the monitor alerts, a human sweeps.
		solvency.SetExposureCap(cfg.HotWalletCapCents)
		solvency.SetColdAddress(cfg.SolanaColdWalletAddress)
		// Under split custody the deposit account is a second place the platform holds
		// user money; leaving it out would report a solvent platform as insolvent every
		// time the float dipped below the queue. A no-op when both are one account.
		solvency.SetVault(cfg.SolanaPlatformATA)
		// The SOL that pays for every payout transaction. Only meaningful once a signer
		// exists — with no hot wallet there is nothing to run out of.
		if solanaXfer != nil {
			solvency.SetFeeWatch(chain, solanaXfer.HotPublicKey(), cfg.HotWalletMinSOLLamports)
			// Let approval refuse a cash-out the wallet cannot settle, instead of
			// approving it and letting the broadcast fail — which would notify the user
			// their withdrawal FAILED for an operational shortfall on our side.
			payoutSvc.SetFunding(solvency)
		}
		solvencyMonitor = solvency
		launch("solvency-monitor", solvency.Run(cfg.SolvencyInterval))

		// Prove the destination we advertise is the account we watch. A mismatch
		// between SOLANA_PLATFORM_OWNER (where payers are told to send) and
		// SOLANA_PLATFORM_ATA (the only account crediting counts) makes every
		// deposit land on-chain and credit nothing — the payer loses the money and
		// the platform never sees it, silently. Off the boot path so a slow RPC does
		// not delay startup, and advisory: it shouts, it does not disable the rail.
		go func() {
			vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if ok, detail := depositSvc.VerifyRails(vctx, chain); !ok {
				log.Error("DEPOSIT RAILS MISCONFIGURED — deposits will not credit",
					"detail", detail, "owner", cfg.SolanaPlatformOwner, "ata", cfg.SolanaPlatformATA,
					"mint", cfg.SolanaUSDCMint)
			} else {
				log.Info("deposit rails verified", "detail", detail)
			}
		}()

		// The same proof for the way OUT: the hot wallet must hold authority over the
		// account it signs transfers from, or every cash-out fails at broadcast with
		// nothing wrong at deploy time. Off the boot path and advisory, for the same
		// reasons as the deposit check above.
		if solanaXfer != nil {
			go func() {
				vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer cancel()
				if ok, detail := solanaXfer.VerifyRails(vctx, payoutRails{chain}); !ok {
					log.Error("PAYOUT RAILS MISCONFIGURED — cash-outs will fail to broadcast",
						"detail", detail, "payout_ata", cfg.PayoutATA(),
						"hot_wallet", solanaXfer.HotPublicKey(), "mint", cfg.SolanaUSDCMint)
				} else {
					log.Info("payout rails verified", "detail", detail,
						"payout_ata", cfg.PayoutATA(), "hot_wallet", solanaXfer.HotPublicKey())
				}
			}()
		}

		log.Info("solana deposits enabled", "mint", cfg.SolanaUSDCMint, "ata", cfg.SolanaPlatformATA,
			"custody_split", cfg.CustodySplit(), "cold_wallet_configured", cfg.SolanaColdWalletAddress != "")
	} else {
		log.Info("solana deposits disabled (set SOLANA_RPC_URL + SOLANA_PLATFORM_OWNER + SOLANA_PLATFORM_ATA to enable)")
	}

	// Wallet reconciliation (P6): read-only drift safety net — cross-checks
	// on-chain deposits/withdrawals against the ledger every interval and alerts
	// (never auto-corrects). Cheap; runs regardless of rail config.
	reconSvc := walletrecon.New(store.NewWalletReconRepo(st.DB), time.Hour, log, metrics.Registry())
	launch("wallet-recon", walletrecon.NewWorker(reconSvc, cfg.WalletReconInterval).Run)

	// Read-only admin surface the Super Admin backfills its live mirror from
	// (users/agents/matches/payments/disputes + revenue overview). Authorized by a
	// Platform service token or the ADMIN_USER_IDS allowlist; additive only.
	adminReadHandler := adminapi.NewHandler(store.NewAdminRepo(st.DB), authn, cfg.AdminUserIDs)
	// Price the dashboard's coin totals in dollars, and surface the real on-chain
	// USDC alongside them. These are different facts, not two currencies for one
	// number: the first is what the ledger says was earned, the second is what the
	// hot wallet actually holds.
	adminReadHandler.SetCoinCents(cfg.CoinCents)
	// The operator's per-user money view: connected wallets (login hint vs the
	// PROVEN payout destination), where the coins are sitting, and the ledger lines
	// behind them. Without it a money support ticket ends in someone with database
	// access pasting a screenshot.
	adminReadHandler.SetWalletRepo(store.NewAdminWalletRepo(st.DB))
	// The operator's per-user AGENT view: each agent's guardrails and how close it is to
	// hitting them. "Why did my agent stop playing" is almost always a limit doing its job,
	// and no operator surface could see a single one of those limits — so the only visible
	// fact was an unspent balance, which points at a fault that does not exist. Read-only:
	// seeing a developer's risk settings is support, changing them is deciding how much of
	// someone else's money to stake.
	adminReadHandler.SetAgentsRepo(store.NewAdminAgentsRepo(st.DB))
	// Queue funnel reporting. ONE repo instance, used by both the writers (matchmaking and
	// the ready check) and the admin reader, so the dashboard cannot end up reading a
	// different table than the one being written.
	queueEvents := store.NewQueueEventsRepo(st.DB, log)
	adminReadHandler.SetQueueHealth(store.QueueHealthAdapter{Repo: queueEvents})
	if solvencyMonitor != nil {
		adminReadHandler.SetTreasury(solvencyMonitor)
	}

	// Match lifecycle: real engine + persistence + per-match Redis lock, real coin
	// escrow/settlement + limit enforcement, live broadcast, ELO at finalize, and
	// engagement hooks (clips + notifications) fired off the hot path.
	matchSvc := match.New(
		matchRepo,
		store.NewLocker(st.Redis),
		walletSvc, walletSvc, hub,
		verifierAdapter{v: verSvc, cert: manifestSvc, susp: platformCfg, conn: agentGateway.Connected},
		raterAdapter{ratingSvc},
		finishHook{clips: clipsSvc, social: socialSvc},
		clock,
		match.Config{MoveWindow: cfg.MoveWindow, RakePct: cfg.RakePct, Rounds: cfg.DefaultRounds, LockTTL: 10 * time.Second},
	)
	// Low-latency wake-ups for long-polling agents (Redis pub/sub, cross-instance).
	// Set after construction so a notifier-less build still works (no-op fallback).
	matchSvc.SetRakeSource(liveRake(cfg.RakePct))
	matchSvc.SetNotifier(store.NewNotifier(st.Redis))

	// Platform-outage grace. A missed turn forfeits a staked seat, which is correct
	// when an agent quits and wrong when WE were unreachable — both look identical on
	// the wire. Detect() reads the PREVIOUS heartbeat before Run() overwrites it, so a
	// gap left by real downtime opens a window in which the sweepers skip forfeits and
	// matches are decided on play instead. Detection keys on our own heartbeat, never
	// on agent disconnects, so a player cannot manufacture grace for a match they are
	// losing. It fails closed: any error leaves forfeits enabled.
	livenessRepo := store.NewLivenessRepo(st.DB)
	livenessTracker := liveness.NewTracker(clock, log)
	livenessTracker.Detect(ctx, livenessRepo)
	// Trace agent table talk. Chat was the single largest hole in agent
	// observability: an agent could post hundreds of lines and Lens recorded nothing —
	// not the line, not the rejection, not a count. In Mafia the talking IS the game,
	// so this is the behaviour a spectator judges and an operator audits after a
	// dispute. A disabled client makes every call a no-op.
	matchSvc.SetChatTracer(lens)
	mafiaSvc.SetChatTracer(lens)
	// Per-decision events, emitted as each turn resolves. The benchmark Recorder is an
	// in-process buffer flushed once at match end and capped at 256 moves, so a crash
	// lost every decision in the match and a long game silently stopped recording.
	matchSvc.SetDecisionTracer(lens)
	mafiaSvc.SetDecisionTracer(lens)
	// Endpoint verification outcomes. A FAILED verification previously produced no
	// telemetry at all, so a developer whose endpoint never passed had nothing to look
	// at and an operator could not see failures in aggregate.
	manifestSvc.SetLifecycleTracer(lens)
	matchSvc.SetLiveness(livenessTracker)
	mafiaSvc.SetLiveness(livenessTracker)
	launch("liveness", func(c context.Context) { livenessTracker.Run(c, livenessRepo) })
	matchSvc.SetStyleRecorder(styleRepo) // record aggression/efficiency at match finish (best-effort)
	// House-agent move picker for sandbox practice matches (no coins/limits/rating).
	matchSvc.SetBot(bot.NewService())
	// RANKED INTEGRITY IS NOT ENFORCED WHEN AUTO-DRIVE IS OFF, and that is deliberate —
	// but it must not be SILENT, which is what this warning is for.
	//
	// The mechanism is built and tested (internal/match/integrity_test.go, 16 cases): a
	// finished ranked match is voided when a seat cannot show its moves were LLM-backed.
	// It is installed below only under RankedAutoDrive, and RANKED_AUTODRIVE defaults to
	// false, so on a default deployment no checker exists and rankedIntegrityFailed
	// returns "did not fail" for every match.
	//
	// DO NOT "FIX" THAT BY INSTALLING THE CHECKER HERE. Two things have to be true before
	// the gate can be turned on, and neither is yet:
	//
	//   1. matchSvc's turn minter is also installed only under auto-drive (below), unlike
	//      mafiaSvc which gets one unconditionally. With no minter, ranked turn
	//      views carry no proof token at all, so NO seat can bind a decision — and the
	//      zero-proof gate would then void EVERY ranked match. Strictly worse than no
	//      enforcement.
	//   2. A proof only exists if the agent routes its LLM call through the Pyyol gateway
	//      (`route()`). An honest developer calling their provider directly scores zero
	//      bound decisions, so enforcing today would void the matches of exactly the
	//      people who are paying for inference. The proof-carrying path ships with the
	//      SDK; it has to reach developers first.
	//
	// So: MEASURE FIRST. Per-match `bound_decisions` is now surfaced to the developer who
	// produced it (GET /v1/developer/matches/{id} → "proven LLM turns"), which is the
	// observation the threshold has to be derived from. Turn the gate on when honest
	// agents are seen scoring above zero, and set the share rule from what they score —
	// not from a guess.
	// PROOF MINTING AND PROOF CHECKING ARE WIRED UNCONDITIONALLY.
	//
	// Both used to sit inside `if cfg.RankedAutoDrive`, which coupled ranked integrity to
	// an unrelated feature — whether the server drives paired agents over their sockets.
	// Auto-drive is off by default, so on a default deployment `s.integrity` was nil and
	// rankedIntegrityFailed returned at its first line. Every ranked match settled with no
	// integrity check of any kind, including the zero-proof gate.
	//
	// That made SetIntegrityCheck's own documented promise false — "the zero-proof gate
	// (rule 1) is always active once a checker is installed" — because outside auto-drive a
	// checker was never installed. A ranked match played by agents polling /v1/match/{id}/action
	// (the ordinary path, no auto-drive involved) got nothing.
	//
	// What the correct dependency is: TURN_PROOF_SECRET, not RANKED_AUTODRIVE. Enforcement
	// needs proofs to exist, and proofs need a minting secret.
	//
	//   secret unset → turnproof.New("") is inert by construction: Mint returns "" and
	//                  Verify is false, so nothing binds, every seat scores zero, `total`
	//                  is zero and rule 1 does not fire. Identical to today's behaviour,
	//                  and NOT forgeable — an empty secret disables the proof rather than
	//                  signing with an empty key.
	//   secret set   → proofs mint on every ranked view, agents routing through the gateway
	//                  bind decisions, and rule 1 starts protecting automatically.
	//
	// Rule 1 is safe to have always on because it is RELATIVE: a zero-proof seat is only
	// voided when another seat in the same match did prove its decisions. Until proofs
	// actually flow it cannot fire at all. That is the property the gate was written to
	// have (see rankedIntegrityFailed) and it is the property it now actually has.
	//
	// RANKED_INTEGRITY_MIN_PCT is deliberately still 0, and the reason has CHANGED.
	//
	// # The metric is no longer the blocker
	//
	// It used to be. Coverage counted CALLS, so one completion bound one round and an agent
	// that batched — one call planning three rounds — scored ~33% while playing entirely
	// model-backed. Phase 4 rewards batching as cost optimisation, so no threshold reconciled
	// the two: above ~33% voided honest batchers, below it let a cheat binding one round in
	// three straight through. Range bindings fixed that (internal/movebind CanonPlan): a
	// completion declares the rounds it decided, each is bound and each is ENFORCED. Measured
	// on real staked tables after the change:
	//
	//	perfect  13/13, 13/13   100%    13 completions
	//	batcher  13/13, 13/13   100%     5 completions   (was 33-44%)
	//	flaky    11/13, 12/13   85-92%  15% of calls failing
	//
	// The honest floor is now set by PROVIDER FAILURES, which is correct — a call that never
	// happened genuinely proved nothing — and it sits far above any threshold worth setting.
	//
	// # What the number should be, when it is turned on
	//
	// False-VOID rate for an honest agent over 13 rounds, by threshold and provider failure
	// rate (binomial; a void cancels a real staked match):
	//
	//	thresh   fail 1%     fail 5%     fail 15%    fail 25%
	//	  40%    0.0000%     0.0000%     0.0162%     0.5649%
	//	  50%    0.0000%     0.0001%     0.1268%     2.4290%
	//	  60%    0.0000%     0.0020%     0.7534%     8.0213%
	//	  70%    0.0007%     0.3103%    11.8003%    41.5747%
	//
	// The cliff is between 60 and 70. 50 is the defensible choice — "more than half of your
	// decisions must be model-backed" — at roughly one false void in 800 matches against a
	// pessimistic 15% failure rate. Longer games are safer still, because the tail tightens
	// with the round count (at 25 rounds, 50% costs 0.0017%).
	//
	// # Why it stays 0 anyway: ADOPTION, not the metric
	//
	// Rule 2 is ABSOLUTE — it voids a seat for a low share regardless of what the other seat
	// did — and almost nobody routes through the gateway yet. Measured over the last 48 hours
	// of staked ranked play: 3863 of 3895 seats proved NOTHING, and a threshold of 50 would
	// have voided 3523 of them. Turning it on today would cancel essentially every staked
	// match on the platform.
	//
	// So the gate to opening this is no longer a measurement, it is that routing through the
	// gateway is the NORM for staked play. Rule 1 is the right control until then: it is
	// relative, so it fires only where proofs actually flow, and it is already on.
	//
	//	SELECT count(*) FILTER (WHERE bound = 0), count(*) FROM <seats in staked rated matches>
	//
	// When that first figure approaches zero, set this to 50.
	//
	// SetTurnMinter must precede EnableRankedDrive: the driver copies the minter at
	// construction, so installing it afterwards would ship views with no proof token.
	matchSvc.SetTurnMinter(turnproof.New(cfg.TurnProofSecret))
	matchSvc.SetIntegrityCheck(store.NewPIndexRepo(st.DB), cfg.RankedIntegrityMinPct)
	// Adaptive decision windows: each seat's budget is derived from the latency it has
	// actually demonstrated rather than from one constant that has to serve both a 0.9s
	// cloud model and a 95s local one. Cached per agent and fails open to cfg.MoveWindow,
	// so a lookup problem degrades to the previous behaviour instead of stalling a turn.
	matchSvc.SetWindowProvider(store.NewWindowRepo(st.DB, log))
	// The other half of the adaptive deadline: at expiry, ask whether the agent is still
	// there. Alive means it is thinking and earns bounded extra time; gone means stop
	// waiting now instead of burning the rest of the window on a dead process.
	matchSvc.SetLivenessProber(store.EndpointProber{
		Resolve: manifestSvc.PlayTarget, Client: manifestProbe, Log: log,
	})
	if cfg.TurnProofSecret == "" {
		log.Warn("ranked integrity INERT: no TURN_PROOF_SECRET, so no decision can be proven LLM-backed and a scripted agent can take ranked stakes",
			"fix", "set TURN_PROOF_SECRET to mint per-turn proof tokens",
			"then", "the zero-proof gate enforces itself; measure with GET /v1/developer/matches/{id} → bound_decisions before setting RANKED_INTEGRITY_MIN_PCT")
	} else {
		log.Info("ranked integrity active: zero-proof gate on (a seat proving nothing is voided when another seat in the same match proved something)",
			"share_rule_min_pct", cfg.RankedIntegrityMinPct)
	}

	if cfg.RankedAutoDrive {
		// Hands-free live-vs-live: drive paired agents over their sockets. Off by
		// default (auto-plays real staked matches) — enable post integration test.
		matchSvc.EnableRankedDrive(agentGateway, manifestSvc, goofspielPlayClient, lens, benchPersist, benchMeta, log)
		log.Info("ranked auto-drive enabled (paired agents driven over their sockets)")
	}
	// The LLM Gateway: a pass-through proxy that turns "which model did this agent use"
	// from a claim into an observation. The developer brings their own provider key, so
	// they cannot name a model they are not being billed for — verification is
	// incentive-compatible rather than trust-based. A call is CREDITED only when its
	// per-turn proof verifies for the exact decision it claims, which is what stops one
	// cheap call buying a verified badge for a whole match.
	llmGatewayRepo := store.NewLLMGatewayRepo(st.DB)
	llmGateway := llmgw.New(llmgw.Config{Upstreams: llmgw.UpstreamsFromEnv(os.Getenv("LLM_GATEWAY_UPSTREAMS"))}, llmGatewayRepo, turnproof.New(cfg.TurnProofSecret), log)
	llmGateway.SetCoverageReader(llmGatewayRepo)
	// Labels each Lens span with WHOSE traffic it is (external / harness). The platform's
	// benchmark plays real matches through this same gateway, so without the label a
	// benchmark run is indistinguishable from user telemetry in the trace views.
	llmGateway.SetKindReader(llmGatewayRepo)
	// A 429 must never cost a stake.
	//
	// The gateway already records every proxied call with its upstream status, match and round,
	// so a rate-limited turn is durable evidence that this agent made a real model call for
	// THIS decision and its provider refused it. That qualifies the seat for the SAME bounded
	// extension a slow-but-answering agent gets — MaxExtensions and Ceiling unchanged, so a
	// throttled agent cannot hold a table open any longer than a slow one.
	//
	// Without this, a developer on a free tier forfeits a staked match because OpenRouter's 50
	// requests a day ran out mid-round. They neither played badly nor went dark.
	matchSvc.SetRateLimitObserver(llmGatewayRepo)
	// Lens spans for server-observed calls, so a gateway round trip shows up in the same trace
	// waterfall as the agent's own handler rather than leaving a hole where the slow part was.
	llmGateway.SetEmitter(lens)
	// The developer-visible "Verified" badge, granted on an agent's first PROVEN decision.
	// The retired gateway granted it on any observed call, which made it mean "routed a request
	// through us"; a bound call is the smallest thing that shows the pipeline actually worked.
	// An EVENT rather than a direct badge write: badge award is already idempotent and driven
	// off the event stream, so going through it keeps one path for granting badges instead of
	// two that can disagree about who has one.
	llmGateway.SetAwarder(awarderFunc(func(ctx context.Context, agentPublicID string) error {
		payload, err := json.Marshal(map[string]string{"agent_id": agentPublicID})
		if err != nil {
			return err
		}
		_, err = store.InsertEvent(ctx, st.DB, events.TypeAgentGatewayVerified, payload)
		return err
	}))
	// COMPLETION BINDING. The gateway extracts the move from the model's own structured tool
	// call; these three lines are what make the game services CHECK it before applying a move.
	// Without them the extraction is recorded and enforced nowhere — exactly the shape of bug
	// this codebase has hit repeatedly, where a correct control sat where the traffic did not go.
	//
	// All three games, not one. A control installed on Goofspiel alone would leave Mafia and
	// Monopoly — the two with more seats and more money on the table — unprotected, and nothing
	// in a passing Goofspiel test would say so.
	//
	// Inert until a turn is actually bound: an agent that does not route through the gateway, or
	// whose completion carried no move tool call, plays exactly as it does today. Only a move
	// that CONTRADICTS an attested model output is rejected.
	matchSvc.SetBoundMoveReader(llmGatewayRepo)
	// A refused move must not buy more time. tryExtend gives a responsive seat extra window on
	// the reasoning that it is thinking; a seat that answered and was REJECTED is not, and
	// without this it holds the round open to the policy ceiling while answering /health
	// perfectly — roughly two minutes a round on a table an opponent has staked on.
	matchSvc.SetRejectionLog(store.NewMatchRepo(st.DB))
	mafiaSvc.SetBoundMoveReader(llmGatewayRepo)
	log.Info("completion binding active: a submitted move that contradicts the model's own output is rejected",
		"games", "goofspiel,mafia,monopoly",
		"inert_when", "the turn carries no bound move (agent does not route, or no move tool call)")

	llmGatewayHandler := llmgw.NewHandler(llmGateway, authn)
	if cfg.TurnProofSecret == "" {
		log.Warn("LLM gateway will record calls but can PROVE none: TURN_PROOF_SECRET is unset, so no call can be bound to a decision and the verified tier stays empty",
			"fix", "set TURN_PROOF_SECRET")
	}

	matchHandler := match.NewHandler(matchSvc, authn)
	// Honour the admin-configured stake tiers on direct table creation too. Without
	// this, /v1/lobby/create accepted an arbitrary bid while /v1/queue and
	// /v1/group-queue rejected free-form stakes for the same game — so tier config was
	// unenforceable across half the ranked surface.
	matchHandler.SetStakeResolver(gameStakesSvc)
	// Dashboard JWT has no AgentPublicID. Rooms sit the same agent /v1/me returns.
	matchHandler.SetPrimaryAgentLookup(idSvc)
	// AND on the service. internal/bot/runner.go calls CreateOpen directly with a hardcoded bid,
	// so the handler's resolver never saw it — the floor has to sit where the escrow happens.
	matchSvc.SetStakeFloor(gameStakesSvc)

	// Sandbox: risk-free practice vs the seeded house agents, played through the
	// same match endpoints. Only starting a match is new.
	sandboxSvc := sandbox.New(matchSvc, cfg.SandboxEnabled)
	// Push-play: drive the developer's seat of a sandbox match from their hosted
	// agent endpoint (manifest push model). Reuses the hardened verification client
	// and the same match machinery, so the browser watches it live over SSE.
	// BEFORE EnablePushPlay, which copies the minter onto the pusher. Sandbox mints proofs
	// for the same reason Mafia and Monopoly do: it is where a developer confirms their
	// gateway wiring actually earns credit, before anything is at stake.
	sandboxSvc.SetTurnMinter(turnproof.New(cfg.TurnProofSecret))
	sandboxSvc.EnablePushPlay(matchSvc, manifestSvc, goofspielPlayClient, log)
	sandboxSvc.SetWebhookEnqueuer(webhookQueue)
	sandboxSvc.SetGateway(agentGateway)                    // play over the socket when the agent is connected
	sandboxSvc.SetBenchmark(lens, benchPersist, benchMeta) // per-match decision-quality telemetry
	sandboxHandler := sandbox.NewHandler(sandboxSvc, authn)

	// Matchmaking: a server-driven, skill-banded queue replaces grabbing matches[0]
	// from the open lobby. The matcher pairs agents within a rating band that widens
	// over wait time (never same-owner) and seats them in an already-active match —
	// making ratings load-bearing and removing the deterministic-rendezvous collusion
	// vector. The Pairer is match.CreatePaired; ratings come from the rating service.
	matchmakingRepo := store.NewMatchmakingRepo(st.DB)
	// Best-effort history on enqueue and pairing. Nil-checked inside, so an unwired build
	// behaves exactly as before rather than failing a pairing over telemetry.
	matchmakingRepo.SetQueueEvents(queueEvents)
	matchmakingSvc := matchmaking.New(
		matchmakingRepo,
		matchPairer{matchSvc}, goofspielRater{ratingSvc}, clock,
		matchmaking.Config{}, log, metrics.Registry(),
	)
	// Ranked queue entry gate: certified AND not suspended. Enforcing suspension
	// here (not just at CreatePaired) means a suspended agent fails fast at enqueue
	// with a specific error, instead of getting a misleading 202 and squatting a
	// `waiting` slot forever for a pairing that CheckEligible would always reject —
	// the same fail-fast principle SetAffordability applies to broke/over-limit agents.
	matchmakingSvc.SetEligibility(rankedEntryGate{cert: manifestSvc, susp: platformCfg, game: string(devplatform.GameGoofspiel), ver: verSvc})
	matchmakingSvc.SetAffordability(walletSvc) // reject unaffordable/over-limit stakes at enqueue (no stuck-waiting)
	// Reject an offline agent at enqueue so it never gets matched and forfeit-bleeds its
	// stake (the auto-play-ranked money leak). Reachable = live socket OR verified endpoint.
	matchmakingSvc.SetLiveness(rankedLivenessGate{gw: agentGateway, resolver: manifestSvc})
	matchmakingHandler := matchmaking.NewHandler(matchmakingSvc, authn)
	matchmakingHandler.SetStakeResolver(gameStakesSvc) // ranked queue by Low/Mid/High tier
	// AND on the service itself. The handler check is not enough: autoplay and the pairing driver
	// call Enqueue directly, so their bids never reached it — which is how 870 matches came to be
	// staked at 50 and 100 coins against a configured floor of 500, starting two seconds after the
	// tiers were seeded and continuing for two days without a single error.
	matchmakingSvc.SetStakeFloor(gameStakesSvc)
	// Deception index. Public read, like the model board — and served WITH its methodology,
	// because "this seat deceives 92% of the time" is a claim about conduct and a number without
	// its chance baseline reads as damning when it is often below random.
	deceptionHandler := deception.NewHandler(store.NewDeceptionRepo(st.DB))
	// Reconciler for queue entries the finalize hook could not clear. A match that ends in about
	// a second can finish BEFORE the pairing transaction that marked its rows 'matched' commits,
	// so the clear deletes nothing and the row is orphaned afterwards. That is two transactions
	// racing, not a missing call, so the invariant is restated as a periodic check instead.
	launch("queue-orphan-sweep", matchmaking.NewSweepWorker(matchmakingRepo, time.Minute, log).Run)

	// READY CHECK. Wired HERE, after matchmaking exists, because a dropped seat is requeued
	// through it — and wired in the same breath as the sweeper on purpose.
	//
	// CreatePaired only takes the ready path when this is configured; without it, pairing
	// escrows and starts exactly as it always did. That guard is what makes this safe to add,
	// but it also means the sweeper and the service must be installed TOGETHER: install the
	// service alone and every paired table lands in ready_check with nothing to release it,
	// which looks precisely like matchmaking having died — no error anywhere, every layer
	// behaving as designed.
	//
	// OFF BY DEFAULT, and the default is the whole point. Neither SDK calls
	// POST /v1/match/{id}/ready yet, so turning this on today means every paired table is
	// asked, re-asked, dropped when its window expires and requeued — forever. Matchmaking
	// would produce no games and report no error, because each layer would be doing exactly
	// what it was built to do. Enable it only once the SDKs acknowledge.
	if cfg.ReadyCheckEnabled {
		// The ask is POST /initialize — the lifecycle call the protocol already has, whose
		// InitializeResponse.Ready both SDKs already return and the transports have always
		// discarded. So the ready check works against agents that have already shipped,
		// without asking any developer to change a line.
		//
		// Socket first: a locally-run `pyyol run` agent is on the WebSocket, which is both
		// open already and the case a developer watching a terminal is actually in.
		asker := match.InitializeAsker{
			Resolver: manifestSvc,
			Client:   goofspielPlayClient,
			Sockets:  agentGateway,
			Log:      log,
		}
		matchSvc.SetReadyCheck(matchRepo, asker, readyRequeue{matchmakingSvc})
		// The "unreachable after the developer started it" number comes from here: a seat
		// dropped for never answering. Nil-checked at every call inside the ready check, which
		// decides whether real coins are escrowed — telemetry must not be able to fail it.
		matchSvc.SetQueueEvents(store.QueueEventAdapter{Repo: queueEvents})
		launch("ready-check-sweeper", match.NewReadySweeper(matchSvc, matchRepo, log, time.Second).Run)
		log.Warn("ready check ENABLED — paired tables wait for every seat to acknowledge before any stake is escrowed; agents that do not call /ready will be dropped and requeued")
	}
	// Clear ranked-queue entries when a match ends. Without this an entry stayed 'matched'
	// forever — live rows were still 'matched' against matches finished an hour earlier — and
	// autoplay, which counts 'matched' as still-queued, never re-entered the agent. An autoplay
	// agent played exactly one ranked match and then wedged, reporting "in a ranked match or
	// waiting in the queue" the whole time.
	matchSvc.SetQueueClearer(rankedQueueAdapter{mm: matchmakingSvc})
	// So a developer learns at SET time that their own max_bid locks them out of ranked play,
	// rather than from a 409 at join time long after the setting was saved and forgotten.
	idHandler.SetStakeSource(gameStakesSvc)

	// Group matchmaking: the N-player sibling of the 2-player queue above. Gives Mafia
	// (12) and Monopoly (a configured seat count) the same skill-banded staked play by
	// pooling distinct-owner agents into a full table (reusing each game's CreateTable+
	// Join, so all escrow/limits/anti-collusion carry over). Goofspiel stays on the
	// 2-player queue; each queue rejects the other's games. Gates are the SAME adapters
	// (certification+suspension, affordability, reachability) — no per-game gate here,
	// since the group queue guards its own games via ErrGameNotGrouped.
	groupSvc := groupmatch.New(
		store.NewGroupQueueRepo(st.DB),
		map[string]groupmatch.TableCreator{
			// Mafia carries the same house-bot set push-play uses, so a thin queue can
			// start a real table for however many agents ARE waiting instead of leaving
			// them queued forever behind a 12-seat requirement.
			string(devplatform.GameMafia): mafiaTableCreator{svc: mafiaSvc, bots: mafiaHouseBots, min: cfg.MafiaMinSeats},
			// Monopoly keeps forming at exactly MinPlayers, as it already did: it is
			// natively playable at that size, so its target IS its minimum and the
			// short-form fallback never engages. Nothing about its timing changes here.
		},
		ratingSvc, clock,
		groupmatch.Config{ShortFormAfter: cfg.GroupShortFormAfter},
		log, metrics.Registry(),
	)
	groupSvc.SetEligibility(rankedEntryGate{cert: manifestSvc, susp: platformCfg, ver: verSvc}) // certified + not suspended + not flagged (game guarded by the queue)
	groupSvc.SetAffordability(walletSvc)
	groupSvc.SetLiveness(rankedLivenessGate{gw: agentGateway, resolver: manifestSvc})
	groupHandler := groupmatch.NewHandler(groupSvc, authn)
	groupHandler.SetStakeResolver(gameStakesSvc)
	groupSvc.SetStakeFloor(gameStakesSvc)

	// Auto-play: devs flip availability on their agent (settings API below); the
	// reconciler loop (launched only when AUTOPLAY_ENABLED) keeps them in matches.
	autoplayRepo := store.NewAutoplayRepo(st.DB)
	autoplayHandler := autoplay.NewHandler(autoplayRepo, authn)
	// Fail fast at enable time when an owner points ranked auto-play at an agent that
	// doesn't declare the (Goofspiel-only) ranked game — instead of silently never
	// playing. Other games use their lobbies / sandbox auto-play.
	autoplayHandler.SetRankedGate(autoplayRankedGate{cert: manifestSvc})

	// Payments — fiat (card) top-ups via Stripe. This is OPTIONAL: Pyyol's on-ramp
	// is a real USDC deposit (see the Solana deposit rail), so a card gateway is a
	// convenience, not a requirement. Three modes, chosen with security first:
	//
	//   • Stripe configured  → live Stripe gateway (real charges). In prod we then
	//     REQUIRE the rest of the Stripe config (webhook secret + real redirect
	//     URLs); a *partial* Stripe setup refuses to boot rather than run half-wired.
	//   • No Stripe, prod     → DisabledGateway: card endpoints return 503. It never
	//     credits coins, so there is no free-coin hole — deposits/withdrawals (Solana)
	//     remain the sole money rail. This is the default Solana-only deployment.
	//   • No Stripe, non-prod → offline DevGateway for local testing (credits coins
	//     with no charge — NEVER reachable in prod by construction below).
	stripeConfigured := cfg.StripeSecretKey != ""
	if stripeConfigured && cfg.IsProd() {
		var missing []string
		if cfg.StripeWebhookSecret == "" {
			missing = append(missing, "STRIPE_WEBHOOK_SECRET")
		}
		// The redirect URLs default to localhost; a real deploy must override them.
		if cfg.CheckoutSuccessURL == "" || strings.Contains(cfg.CheckoutSuccessURL, "localhost") {
			missing = append(missing, "CHECKOUT_SUCCESS_URL")
		}
		if cfg.CheckoutCancelURL == "" || strings.Contains(cfg.CheckoutCancelURL, "localhost") {
			missing = append(missing, "CHECKOUT_CANCEL_URL")
		}
		if cfg.ConnectReturnURL == "" || strings.Contains(cfg.ConnectReturnURL, "localhost") {
			missing = append(missing, "CONNECT_RETURN_URL")
		}
		if cfg.ConnectRefreshURL == "" || strings.Contains(cfg.ConnectRefreshURL, "localhost") {
			missing = append(missing, "CONNECT_REFRESH_URL")
		}
		if len(missing) > 0 {
			return fmt.Errorf("payments: STRIPE_SECRET_KEY is set (fiat top-ups on) but the setup is incomplete; "+
				"missing/placeholder: %s — set them, or unset STRIPE_SECRET_KEY to run Solana-only (card top-ups disabled)",
				strings.Join(missing, ", "))
		}
	}

	var gateway payments.Gateway
	var billingGW subscription.BillingGateway
	devPayments := false
	switch {
	case stripeConfigured:
		gateway = payments.NewStripeGateway(cfg.StripeSecretKey)
		billingGW = subscription.NewStripeBillingGateway(cfg.StripeSecretKey)
	case cfg.IsProd():
		gateway = payments.DisabledGateway{}
		billingGW = subscription.DisabledBillingGateway{}
		log.Info("STRIPE_SECRET_KEY unset in prod: fiat card top-ups DISABLED (Solana USDC deposits are the on-ramp)")
	default:
		gateway = &payments.DevGateway{}
		billingGW = subscription.DevBillingGateway{}
		devPayments = true
		log.Warn("STRIPE_SECRET_KEY unset (non-prod): payments using the offline DevGateway (no real charges, coins credited immediately)")
	}
	paymentsSvc := payments.New(gateway, walletSvc, store.NewPaymentsRepo(st.DB), clock,
		payments.Config{
			Packs:             payments.DefaultPacks(),
			FeeSchedules:      payments.DefaultFeeSchedules(),
			SuccessURL:        cfg.CheckoutSuccessURL,
			CancelURL:         cfg.CheckoutCancelURL,
			ConnectReturnURL:  cfg.ConnectReturnURL,
			ConnectRefreshURL: cfg.ConnectRefreshURL,
			WebhookSecret:     cfg.StripeWebhookSecret,
			DevMode:           devPayments,
		}, log, metrics.Registry())

	subscriptionSvc := subscription.New(billingGW, walletSvc, store.NewSubscriptionRepo(st.DB),
		subscription.Config{
			Plan: subscription.Plan{
				Key: subscription.PlanArenaPass, Label: "Arena Pass",
				PriceCents: cfg.StripeArenaPassPriceCents, MonthlyCoins: cfg.ArenaPassMonthlyCoins,
				Currency: "usd",
			},
			StripePriceID:   cfg.StripeArenaPassPriceID,
			SuccessURL:      cfg.SubscriptionSuccessURL,
			CancelURL:       cfg.SubscriptionCancelURL,
			PortalReturnURL: cfg.SubscriptionPortalURL,
			DevMode:         devPayments,
		}, log)
	paymentsSvc.SetStripeHook(subscriptionSvc)
	paymentsSvc.SetPayoutReconciler(payoutSvc) // transfer.reversed → re-credit withdrawn coins
	subscriptionHandler := subscription.NewHandler(subscriptionSvc, authn)
	paymentsHandler := payments.NewHandler(paymentsSvc, authn)

	// Background: the move-window timeout sweeper + ledger reconciliation + the
	// Stripe↔ledger reconciliation job (all safe to run on every instance).
	launch("match-sweeper", match.NewSweeper(matchSvc, log, time.Second).Run)
	launch("matchmaker", matchmakingSvc.NewMatcher().Run)
	launch("group-matchmaker", groupSvc.NewMatcher().Run)
	if cfg.AutoplayEnabled {
		autoplaySvc := autoplay.New(
			autoplayRepo,
			rankedQueueAdapter{mm: matchmakingSvc},
			sandboxStarterAdapter{
				throttle: autoplay.NewSandboxThrottle(5 * time.Minute),
				goof: sandboxSvc, mafia: mafiaSvc,
			},
			autoplay.Config{},
			log,
		)
		// Feed the stop-conditions today's matches/tokens (benchmark aggregate) +
		// losses (wallet); schedule works without it.
		autoplaySvc.SetStats(autoplayStats{pindex: pindexRepo, wallet: walletSvc, now: clock.Now})
		autoplaySvc.SetGroupQueue(groupQueueAdapter{svc: groupSvc}) // ranked Mafia/Monopoly → N-player group queue
		launch("autoplay", autoplay.NewTicker(autoplaySvc, cfg.AutoplayInterval).Run)
	}
	launch("ledger-reconciler", ledgerSvc.NewReconciler(log, cfg.ReconcileInterval).Run)
	launch("payments-reconciler", paymentsSvc.NewReconciler(cfg.PaymentsReconcileInterval).Run)
	launch("clips", clipsSvc.Run)
	launch("social", socialSvc.Run)
	launch("antifraud-detector", antifraudSvc.NewDetector(cfg.DetectInterval).Run)

	// Dev/demo: rule-based bots fill Goofspiel + Mafia tables (no LLM). Real users
	// bring their own agents via API keys; disable with DEMO_BOTS=false.
	if cfg.DemoBots {
		if agents, err := demo.EnsureAgents(ctx, idRepo, walletSvc, log); err != nil {
			log.Warn("demo agent seed failed", "error", err)
		} else {
			log.Info("demo bots enabled (rules engine, not LLM)", "count", len(agents))
			// The house roster, as an EXPLICIT id set from the seeder's own return value. This is an
			// exemption inside a fraud control, so it must be impossible to fall into: matching a slug
			// or framework label would let any agent that came to look house-shaped inherit it.
			houseIDs := make([]string, 0, len(agents))
			for _, a := range agents {
				houseIDs = append(houseIDs, a.PublicID)
			}
			mafiaSvc.SetHouseRoster(houseIDs)
			// Dev-only: certify the platform's demo bots so they clear the ranked
			// certification gate. They have no hosted endpoint, so record a
			// pre-verified manifest directly. This lets the demo runner produce rated
			// Goofspiel matches, which populate the ELO leaderboard + season champion.
			demoManifestRepo := store.NewManifestRepo(st.DB)
			for _, a := range agents {
				if err := demoManifestRepo.SeedVerifiedManifest(ctx, a.PublicID); err != nil {
					log.Warn("demo agent certify failed", "agent", a.PublicID, "error", err)
				}
			}
			launch("demo-bot-runner", bot.NewRunner(matchSvc, mafiaSvc, agents, log).Run)
			// NOTE: mafia push-play is now enabled unconditionally above with dedicated
			// kind='house' filler bots, so it no longer depends on these demo agents.
		}
	}
	// Long-poll wake-ups for GET /v1/mafia/{id}/state?wait=true (parity with Goofspiel).
	mafiaSvc.SetNotifier(store.NewNotifier(st.Redis))

	// Docs-as-data: seed the modular Markdown docs (internal/docs/content) into the
	// docs_pages table at the current version so the frontend serves versioned,
	// structured docs from /v1/docs. Best-effort — a seed failure must not stop boot.
	docsRepo := store.NewDocsRepo(st.DB)
	if pages, derr := docs.Load(); derr != nil {
		log.Warn("docs: failed to load embedded content", "err", derr)
	} else if serr := docsRepo.Seed(ctx, docs.DocsVersion, pages); serr != nil {
		log.Warn("docs: failed to seed docs_pages", "version", docs.DocsVersion, "err", serr)
	} else {
		log.Info("docs seeded", "version", docs.DocsVersion, "pages", len(pages))
		if derr := docsRepo.DeleteSlugAllVersions(ctx, "games/monopoly"); derr != nil {
			log.Warn("docs: could not withdraw games/monopoly from older versions", "err", derr)
		}
	}

	// THE OPERATOR'S OWN LOGIN, provisioned at boot.
	//
	// Admin rights come from ADMIN_USER_IDS, which cannot name an account that does not exist
	// yet — so the first admin on a fresh deployment had to be created by hand against the
	// database, and a CI/CD deploy could never produce a usable one. Seeding here closes that
	// with no extra deploy step and no shell in the distroless image.
	//
	// Disabled unless both env vars are set, so any deployment that does not want it is
	// untouched. Best-effort: a seed failure is logged loudly and does not stop boot, because
	// refusing to serve the arena over a failed convenience is the wrong trade.
	if cfg.SeedAdminEmail != "" && cfg.SeedAdminPassword != "" {
		if res, serr := seedadmin.Run(ctx, st.DB, cfg.SeedAdminEmail, cfg.SeedAdminPassword,
			cfg.SeedAdminUserID, cfg.APIKeyPepper); serr != nil {
			log.Error("operator account seed failed", "email", cfg.SeedAdminEmail, "err", serr)
		} else {
			// Whether the id is actually trusted is worth stating: seeding the account and
			// granting it admin are two separate decisions, and getting the second one wrong
			// produces a login that works and can see nothing.
			admin := false
			for _, id := range cfg.AdminUserIDs {
				if id == res.UserPublicID {
					admin = true
					break
				}
			}
			log.Info("operator account seeded", "user", res.UserPublicID, "email", cfg.SeedAdminEmail,
				"created", res.Created, "in_admin_allowlist", admin)
			if !admin {
				log.Warn("operator account is NOT in ADMIN_USER_IDS — it can sign in but has no admin rights",
					"user", res.UserPublicID,
					"fix", "add "+res.UserPublicID+" to ADMIN_USER_IDS")
			}
		}
	}

	// Several operators, from one JSON secret. Runs alongside the single-account path above
	// rather than replacing it, so a deployment already using SEED_ADMIN_EMAIL keeps working
	// untouched.
	//
	// Best-effort like the single seed, and for the same reason: refusing to serve the arena
	// because an operator's password was two characters short is the wrong trade. But every
	// outcome is logged per account, because "seeding failed" tells whoever is locked out
	// nothing about which account or why.
	if ops, perr := seedadmin.ParseOperators(cfg.SeedAdminOperators); perr != nil {
		log.Error("SEED_ADMIN_OPERATORS could not be read — no operators seeded from it", "err", perr)
	} else if len(ops) > 0 {
		results, rerr := seedadmin.RunAll(ctx, st.DB, ops, cfg.APIKeyPepper)
		if rerr != nil {
			log.Error("some operator accounts could not be seeded", "err", rerr)
		}
		for _, res := range results {
			admin := false
			for _, id := range cfg.AdminUserIDs {
				if id == res.UserPublicID {
					admin = true
					break
				}
			}
			log.Info("operator account seeded", "user", res.UserPublicID, "created", res.Created,
				"in_admin_allowlist", admin)
			if !admin {
				// The account works and sees nothing, which is the confusing half of this
				// pair of settings. Name the exact id so the fix is a copy-paste.
				log.Warn("operator account is NOT in ADMIN_USER_IDS — it can sign in but has no admin rights",
					"user", res.UserPublicID,
					"fix", "add "+res.UserPublicID+" to ADMIN_USER_IDS")
			}
		}
	}

	docsHandler := docs.NewHandler(docsRepo)
	docsAdminHandler := docs.NewAdminHandler(docsRepo, authn, cfg.AdminUserIDs)

	// SDK download analytics (admin): public install-ping ingest + admin reads, plus a
	// daily poller pulling npm + PyPI download totals. Registry data is daily and 404s
	// until the packages are published — the poller handles both gracefully.
	sdkStatsRepo := store.NewSDKStatsRepo(st.DB)
	sdkStatsHandler := sdkstats.NewHandler(sdkstats.New(sdkStatsRepo), authn, cfg.AdminUserIDs)
	launch("sdk-download-poller", sdkstats.NewPoller(sdkStatsRepo, log, "pyyol", 12*time.Hour).Run)

	// 8. HTTP server with the standard middleware chain.
	mounts := []httpx.Mount{
		healthH.Register,
		openapi.NewHandler().Register,
		idHandler.Register,
		manifestHandler.Register,
		agentGateway.Register,
		mountAgentStatus(authn, agentGateway),
		func() httpx.Mount {
			mu := store.NewMatchUsageRepo(st.DB)
			return mountMatchUsage(authn, mu, mu.OwnedBy)
		}(),
		mountCapabilities(xClaimEnabled, cfg.DepositsEnabled(), !cfg.IsProd(), cfg.SolanaCluster, func() economics {
			// Live from the admin snapshot when one is published, falling back to the
			// boot config so the price list is never blank.
			e := economics{
				RakePct: cfg.RakePct, DepositFeePct: cfg.DepositFeePct,
				WithdrawFeePct: cfg.WithdrawSellFeePct, CoinCents: cfg.CoinCents,
				MinStakeUSDCents: cfg.MinStakeUSDCents,
				// The cash-out floor. Published for the same reason the rake is: it is a
				// price-list fact the user is refused by. Withheld, the withdrawal form
				// happily accepted 100 coins and the server rejected it as too small with
				// nothing on the screen ever having mentioned a minimum.
				MinWithdrawalCoins: cfg.WithdrawMinCoins,
			}
			if platformCfg != nil {
				snap := platformCfg.Get()
				ec := snap.Economy
				e.RakePct, e.DepositFeePct = ec.PlatformCommissionPct, ec.DepositFeePct
				e.WithdrawFeePct, e.MinStakeUSDCents = ec.WithdrawFeePct, ec.MinStakeUSDCents
				// Bounded accessor. Superseded below when the Super Admin gate is wired.
				e.MinWithdrawalCoins = snap.MinWithdrawalCoins(cfg.WithdrawMinCoins, cfg.CoinCents)
			}
			// PUBLISH THE FIGURE THAT IS ACTUALLY ENFORCED. payout.Service only falls back
			// to the env floor when no gate is wired (`if s.gate == nil && coins <
			// s.minCoins()`); with the gate present, walletadmin's MinWithdrawCoins is the
			// number that refuses a request. Publishing the env value instead would put a
			// different minimum on the form than the one the server applies — the same
			// class of bug as publishing none, since the user is still refused by a figure
			// they were never shown. Read is cached inside walletadmin; an error leaves
			// the value already set above.
			if walletAdminSvc != nil {
				if st, err := walletAdminSvc.Settings(context.Background()); err == nil && st.MinWithdrawCoins > 0 {
					e.MinWithdrawalCoins = st.MinWithdrawCoins
				}
			}
			return e
		}),
		matchHandler.Register,
		deceptionHandler.Register,
		matchmakingHandler.Register,
		groupHandler.Register,
		autoplayHandler.Register,
		sandboxHandler.Register,
		walletHandler.Register,
		paymentsHandler.Register,
		subscriptionHandler.Register,
		specHandler.Register,
		mafiaHandler.Register,
		ratingHandler.Register,
		pindexHandler.Register,
		modelBoardHandler.Register,
		llmGatewayHandler.Register,
		profilesHandler.Register,
		devProfileHandler.Register,
		devTraceHandler.Register,
		mediaHandler.Register,       // avatar upload (user scope) + public object read-back
		arena.NewHandler().Register, // public GET /v1/arenas (SDK discovery)
		docsHandler.Register,        // public GET /v1/docs (versioned docs-as-data)
		docsAdminHandler.Register,   // super-admin CRUD /v1/admin/docs (edit/publish versions)
		sdkStatsHandler.Register,    // public install-ping ingest + admin SDK download analytics
		clipsHandler.Register,
		socialHandler.Register,
		antifraudHandler.Register,
		tournamentHandler.Register,
		payoutHandler.Register,
		adminReadHandler.Register,
		walletAdminHandler.Register,
		gameStakesHandler.Register,
		walletVerifyHandler.Register,
		twofaHandler.Register,
		userEventsHandler.Register,   // GET /v1/events/stream — the caller's own live feed
		paymentTraceHandler.Register, // payment flow models + per-user timelines
		invoicesHandler.Register,     // GET /v1/user/invoices — receipts
	}
	if depositHandler != nil {
		mounts = append(mounts, depositHandler.Register)
	}
	{
		// The developer-facing view of the queue history: "which of MY agents is stuck".
		//
		// A Mount closure because this file builds the router from `mounts` at the end; there is
		// no router variable in scope where the repo is constructed. Guarded to USER scope
		// inside RegisterQueueSelf — an agent key resolves to its owner, so a router without
		// that guard would let a leaked CI key read its owner's whole queue history.
		// A registrar VALUE, like depositHandler.Register above: main.go does not import chi,
		// so the closure is built in devplatform where the router type already is.
		mounts = append(mounts, devplatform.QueueSelfMount(store.QueueSelfAdapter{Repo: queueEvents}))
	}
	// internal/llmgateway (mounted at /gw/*) is RETIRED. internal/llmgw at
	// /v1/gw/{provider}/* replaced it and is wired above with everything the old one did —
	// turn-proof binding, per-match verified cost, the Lens span, the "Verified" badge — plus
	// what it never had: separated prompt-cache read/write accounting, a coverage endpoint, and
	// a coverage-gated verified tier.
	//
	// Two gateways writing two different verified stores was the actual defect: the boards read
	// only the older one, so an agent with every decision proven through the new path was still
	// reported as merely SDK-observed. agent_match_verified_cost held zero rows platform-wide
	// at the time of removal, so there was no history to migrate — the old path had never
	// produced a verified row on this deployment.
	router := httpx.NewRouter(httpx.Deps{Config: cfg, Logger: log, Metrics: metrics}, mounts...)
	srv := httpx.NewServer(cfg, router, log)

	// 9. Serve until shutdown, then drain.
	return srv.Run(ctx)
}

// parseLensLogLevel maps PYYOL_LENS_LOG_LEVEL to a slog.Level; unknown/empty
// defaults to WARN so the telemetry log stream stays signal-heavy by default.
func parseLensLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "error":
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}

// adminIDSet turns the ADMIN_USER_IDS slice into the lookup shape the auth
// guards take. Several handlers build this independently; it is here so a future
// one cannot get the conversion subtly wrong (an empty entry admitting everyone,
// say) in its own copy.
func adminIDSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" {
			set[id] = true
		}
	}
	return set
}

// notifierAdapter lets the deposit + withdrawal services write user notifications
// through the shared, idempotent notifications table (store.SocialRepo) without
// importing store. Satisfies solanadeposit.Notifier, payout.Notifier and
// social.Pusher.
//
// Durable row first, live push second, and the push happens ONLY when the insert
// actually inserted. That ordering is what makes the two views agree: the bell's
// persisted feed is the record, the stream is an accelerator, and a redelivered
// event (a re-observed on-chain transfer, a replayed match finalize) is a no-op in
// both — it neither duplicates a row nor re-toasts a confirmation.
type notifierAdapter struct {
	repo *store.SocialRepo
	bus  *userevents.Bus
}

func (n notifierAdapter) Notify(ctx context.Context, userPublicID, kind, ref string, payload []byte) error {
	inserted, err := n.repo.InsertNotification(ctx, userPublicID, kind, ref, payload)
	if err != nil {
		return err
	}
	if inserted {
		n.bus.Publish(ctx, userPublicID, kind, ref, payload)
	}
	return nil
}

// Push satisfies social.Pusher: the social worker has already written (and
// deduped) the row, so this is the live half only.
func (n notifierAdapter) Push(ctx context.Context, userPublicID, kind, ref string, payload []byte) {
	n.bus.Publish(ctx, userPublicID, kind, ref, payload)
}

// walletEvents satisfies wallet.EventSink: it lets the wallet service announce a
// balance move (a stake leaving an agent, a treasury allocation, a card top-up
// settling) without importing the event package's dispatch policy. The Signaller
// keeps the owner lookup a stake needs, and the notification write a top-up needs,
// off the ledger transaction's critical path.
type walletEvents struct {
	bus      *userevents.Bus
	sig      *userevents.Signaller
	notifier notifierAdapter
	log      *slog.Logger
}

func (w walletEvents) Go(fn func(ctx context.Context)) { w.sig.Go(fn) }

// Publish is the transient half: straight to the user's open streams, no record.
func (w walletEvents) Publish(ctx context.Context, userPublicID, kind, ref string, payload map[string]any) {
	w.bus.PublishJSON(ctx, userPublicID, kind, ref, payload)
}

// Notify is the durable half: the same idempotent row every other money event
// writes, then the live push (only when the row was genuinely new). The error is
// returned, not just logged, because the payment trace records whether the user
// was actually told.
func (w walletEvents) Notify(ctx context.Context, userPublicID, kind, ref string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := w.notifier.Notify(ctx, userPublicID, kind, ref, body); err != nil {
		w.log.Warn("wallet notify failed", "user", userPublicID, "kind", kind, "error", err)
		return err
	}
	return nil
}

// matchPairer bridges matchmaking.Pairer to match.Service.CreatePaired, so the
// matchmaker creates an already-active two-seat match without importing match's
// internals.
type matchPairer struct{ m *match.Service }

func (p matchPairer) CreatePaired(ctx context.Context, aAgent, aOwner, bAgent, bOwner string, bid int64) (string, error) {
	return p.m.CreatePaired(ctx, aAgent, aOwner, bAgent, bOwner, bid)
}

// profileManifest adapts manifest.Service to profiles.Manifest: it maps the
// agent's public active manifest into the profile's certification card, or nil
// when the agent has no public/verified manifest.
type profileManifest struct{ svc *manifest.Service }

func (p profileManifest) Card(ctx context.Context, agentPublicID string) (*profiles.ManifestCard, error) {
	m, err := p.svc.PublicActive(ctx, agentPublicID)
	if errors.Is(err, manifest.ErrNoManifest) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	card := &profiles.ManifestCard{
		Certified:    m.Status == manifest.StatusVerified,
		AgentVersion: m.AgentVersion,
		Games:        m.Games,
	}
	if m.Model != nil {
		card.Model = &profiles.ModelInfo{
			Provider: m.Model.Provider, Model: m.Model.Model,
			Declared: true, Reasoning: m.Model.Reasoning,
		}
	}
	return card, nil
}

// verifierAdapter bridges verification.Service to match.Verifier and adds the
// certification gate: an agent must have an active, endpoint-verified manifest to
// enter ranked play. cert may be nil (gate disabled).
type verifierAdapter struct {
	v    *verification.Service
	cert *manifest.Service
	susp *platformcfg.Provider // Super Admin suspension list (may be nil)
	// conn reports whether an agent currently holds a live socket. Needed because a
	// CONNECTED-RANKED agent declares no hosted endpoint: its socket is the only way
	// to reach it, so staking it while disconnected would forfeit every turn to the
	// engine's fallback and lose the match without a decision being made.
	conn func(agentPublicID string) bool
}

func (a verifierAdapter) Record(ctx context.Context, agentPublicID string, matchPublicID *string, responseMs int) {
	_ = a.v.Record(ctx, agentPublicID, matchPublicID, responseMs)
}

func (a verifierAdapter) CheckEligible(ctx context.Context, agentPublicID string) error {
	// Super Admin moderation gate: a suspended agent may not enter ranked play.
	// Sourced from the platform config snapshot (Admin → engine), so a suspension
	// takes effect within one config refresh without an engine deploy.
	if a.susp != nil && a.susp.Get().IsSuspended(agentPublicID) {
		return httpx.NewError(http.StatusForbidden, "agent_suspended", "This agent has been suspended by platform administrators.")
	}
	// Certification gate (the wedge): no ranked play without a verified agent.
	if a.cert != nil {
		if err := a.cert.RequireCertified(ctx, agentPublicID); err != nil {
			return err
		}
	}
	// Connected-ranked: no endpoint means the socket is the ONLY way to reach this
	// agent, so it must be connected right now. Hosting buys you the freedom to be
	// away; without it, being away means losing a stake to fallback moves you never
	// chose. Refuse the stake instead of taking it and playing the agent as a corpse.
	if a.cert != nil && a.conn != nil {
		if _, hasEndpoint, err := a.cert.PlayTarget(ctx, agentPublicID); err == nil && !hasEndpoint && !a.conn(agentPublicID) {
			return httpx.NewError(http.StatusConflict, "agent_not_connected",
				"This agent has no hosted endpoint, so it can only play ranked while connected. "+
					"Start it (`pyyol play <game> --ranked` keeps it connected), or add an endpoint "+
					"to your manifest to play while you are away: https://pyyol.com/docs/deploy.md")
		}
	}
	e, err := a.v.CheckEligibility(ctx, agentPublicID)
	if err != nil {
		return err
	}
	if !e.Eligible {
		return httpx.NewError(http.StatusUnprocessableEntity, "verification_pending", "Agent flagged for review: "+e.Reason)
	}
	return nil
}

// rankedEntryGate is the matchmaking.Eligibility gate: an agent may enter the
// ranked queue only if it is not Super-Admin-suspended, is certified, and is not
// flagged for review. The authoritative money/play gate is still CreatePaired
// (verifierAdapter.CheckEligible); this fails those agents fast at enqueue rather than
// letting them sit in `waiting` for a pairing that can never escrow.
type rankedEntryGate struct {
	cert *manifest.Service
	susp *platformcfg.Provider // may be nil (suspension list unavailable)
	game string                // the only game the ranked queue matchmakes; "" ⇒ skip the game check
	// ver is the timing/verification check. Nil ⇒ skipped, which is the previous behaviour.
	ver *verification.Service
}

func (g rankedEntryGate) RequireCertified(ctx context.Context, agentPublicID string) error {
	if g.susp != nil && g.susp.Get().IsSuspended(agentPublicID) {
		return httpx.NewError(http.StatusForbidden, "agent_suspended", "This agent has been suspended by platform administrators.")
	}
	if err := g.cert.RequireCertified(ctx, agentPublicID); err != nil {
		return err
	}
	// Flagged for review, checked HERE and not only at pairing time.
	//
	// This gate's whole purpose is to keep an agent that cannot escrow out of the pool, and
	// eligibility was the one sticky rejection it did not check. The result was a retry storm:
	// the matcher claims a pair, CreatePaired refuses on verification_pending, the claim is
	// released "so both re-enter the pool and are retried next tick" — and next tick it fails
	// for exactly the same reason, forever. Measured on the lab: 4,205 pairing failures in 30
	// minutes for FOUR agents.
	//
	// That release-and-retry is right for a TRANSIENT refusal (a seat that cannot afford the
	// stake this second). A review flag is not transient: it clears when a human clears it, or
	// when the agent starts proving its decisions. Retrying it every tick also blocks the
	// agents it keeps getting paired against.
	//
	// Fails OPEN on a lookup error. Refusing entry to the ranked queue because a timing query
	// hiccuped would lock honest agents out of play, and the authoritative gate still runs at
	// CreatePaired where the money actually moves.
	if g.ver != nil {
		e, err := g.ver.CheckEligibility(ctx, agentPublicID)
		if err == nil && !e.Eligible {
			return httpx.NewError(http.StatusUnprocessableEntity, "verification_pending",
				"Agent flagged for review: "+e.Reason+". Route your model calls through the Pyyol "+
					"gateway so decisions are provably LLM-backed; a proven agent is not judged on "+
					"timing alone.")
		}
	}
	// Ranked matchmaking runs one game (Goofspiel). Reject an agent whose manifest
	// doesn't declare it, so a Mafia/Monopoly-only agent can't be enqueued into the
	// Goofspiel queue and forfeit-bleed its stake. Certified ⇒ an active manifest
	// exists, so !supported here means it genuinely lacks the game.
	if g.game != "" {
		supported, _, err := g.cert.SupportsGame(ctx, agentPublicID, g.game)
		if err != nil {
			return err
		}
		if !supported {
			return errRankedGameUnsupported(g.game)
		}
	}
	return nil
}

// errRankedGameUnsupported explains that ranked matchmaking is single-game today and
// points the developer at the paths that DO work for other games.
func errRankedGameUnsupported(game string) error {
	return httpx.NewError(http.StatusConflict, "ranked_game_unsupported",
		fmt.Sprintf("Ranked matchmaking currently runs %s only, and this agent's manifest does not declare %s. Mafia and Monopoly play through their game lobbies; use sandbox auto-play to practice them.", game, game))
}

// mafiaTableCreator / monopolyTableCreator adapt each game's Service to
// groupmatch.TableCreator. They deliberately reuse the existing CreateTable + Join
// path (the Nth join auto-starts the table and escrows every seat), so the group
// matcher inherits all of that path's money-safety and anti-collusion checks. A join
// that fails mid-fill returns an error → the matcher releases the queue claim and the
// partially-filled waiting table is reaped by the game's waiting-lobby TTL sweeper
// (no stake escrowed until the table actually starts).
type mafiaTableCreator struct {
	svc  *mafia.Service
	bots []mafia.BotAgent // house fillers (migration 0063), same set push-play uses
	min  int              // fewest REAL agents to start with; see MinSeats
}

func (mafiaTableCreator) SeatTarget() int { return mafiaengine.RosterSize } // fixed 12

// MinSeats is how few real agents Mafia will start with. The TABLE is still twelve
// seats — the engine deals from a fixed 12-role pool, and dealing the first N of a
// shuffled twelve to a shorter table produces degenerate games (roughly one in six
// five-seat tables gets no mafia at all and is over before anyone acts). So a
// short-handed start means "fewer humans, rest are bots", not "smaller table".
//
// Falls back to full-roster-only if misconfigured, or if there are not enough house
// bots to cover the gap — better to keep waiting than to try to start a table that
// cannot be filled.
func (c mafiaTableCreator) MinSeats() int {
	if c.min < 2 || c.min > mafiaengine.RosterSize {
		return mafiaengine.RosterSize
	}
	if len(c.bots) < mafiaengine.RosterSize-c.min {
		return mafiaengine.RosterSize
	}
	return c.min
}

func (c mafiaTableCreator) CreateStartedTable(ctx context.Context, seats []groupmatch.Seat, bid int64) (string, error) {
	id, err := c.svc.CreateTable(ctx, seats[0].AgentPublicID, seats[0].OwnerPublicID, bid)
	if err != nil {
		return "", err
	}
	for _, s := range seats[1:] {
		if _, err := c.svc.Join(ctx, s.AgentPublicID, s.OwnerPublicID, id); err != nil {
			return "", err
		}
	}
	// Backfill whatever the queue could not supply. Bots join LAST so that every real
	// agent is already seated when the final join starts the match, and so a table that
	// could have filled with humans never has a bot in it. JoinHouseSeat rather than
	// Join: the fillers share one owner and hold no coins (see mafia.JoinHouseSeat).
	//
	// The human seats still stake and settle normally; the table is recorded unrated
	// because its opposition was partly engine-driven.
	filled := make([]string, 0, mafiaengine.RosterSize-len(seats))
	for i := 0; i < mafiaengine.RosterSize-len(seats); i++ {
		b := c.bots[i]
		if _, err := c.svc.JoinHouseSeat(ctx, b.PublicID, b.OwnerPublicID, id); err != nil {
			return "", err
		}
		filled = append(filled, b.PublicID)
	}
	// Nothing else would ever act for the fillers — push-play's drive loop only runs for a
	// push-play table. Without this the bot seats would stay silent until every phase timed out.
	c.svc.DriveHouseSeats(id, filled)
	// AND push turns to the REAL agents.
	//
	// The line above used to be the whole story, on the premise that "real agents act for
	// themselves over the API". That is true of a polling client and false of both transports
	// the SDK offers: `pyyol run` waits for socket turn frames and a hosted endpoint waits to be
	// POSTed to. Neither polls, so nothing drove them and a ranked Mafia seat sat idle until its
	// phases timed out. Goofspiel has had the equivalent all along (match.maybeDrive).
	real := make([]string, 0, len(seats))
	all := make([]string, 0, len(seats)+len(filled))
	for _, st := range seats {
		real = append(real, st.AgentPublicID)
		all = append(all, st.AgentPublicID)
	}
	all = append(all, filled...)
	c.svc.DriveMatchedSeats(ctx, id, real, all)
	return id, nil
}


// autoplayRankedGate validates, at auto-play ENABLE time, that an agent may enter
// ranked auto-play for the game it would play — that its manifest declares that game
// (Goofspiel, Mafia, or Monopoly — all support ranked now). It checks only the
// permanent game-support property (not the transient certified/reachable state, which
// the enqueue gate enforces per match), and stays quiet when the agent has no manifest
// yet (can't tell → don't block preconfiguration; the enqueue gate still enforces).
type autoplayRankedGate struct {
	cert *manifest.Service
}

func (g autoplayRankedGate) CheckRankedGame(ctx context.Context, agentPublicID, game string) error {
	if game == "" {
		return nil
	}
	supported, found, err := g.cert.SupportsGame(ctx, agentPublicID, game)
	if err != nil {
		return err
	}
	if found && !supported {
		return errRankedGameUnsupported(game)
	}
	return nil
}

// groupQueueAdapter lets the auto-play reconciler route Mafia/Monopoly agents into
// the N-player ranked queue (mirrors rankedQueueAdapter for the 2-player queue).
type groupQueueAdapter struct{ svc *groupmatch.Service }

func (a groupQueueAdapter) Handles(game string) bool { return a.svc.Handles(game) }

func (a groupQueueAdapter) Enqueue(ctx context.Context, agent, owner, game string, bid int64) error {
	_, err := a.svc.Enqueue(ctx, agent, owner, game, bid)
	return err
}

func (a groupQueueAdapter) Queued(ctx context.Context, agent string) (bool, error) {
	e, err := a.svc.Status(ctx, agent)
	if err != nil {
		return false, nil // no live entry ⇒ not queued (top up on the next tick)
	}
	return e.Status == groupmatch.StatusWaiting || e.Status == groupmatch.StatusMatched, nil
}

// rankedLivenessGate is the matchmaking.Liveness gate: an agent may enter the ranked
// queue only if it is reachable RIGHT NOW — either holding a live gateway socket or
// exposing a verified, resolvable hosted endpoint. This is the SAME reachability test
// match.driver.seatFor uses to decide whether a seat is drivable, applied at enqueue
// so an offline agent never gets matched (and never forfeit-bleeds its stake). Under
// auto-play a rejection is swallowed and retried, so the agent resumes on reconnect.
type rankedLivenessGate struct {
	gw       interface{ Connected(agentID string) bool }
	resolver interface {
		PlayTarget(ctx context.Context, agentPublicID string) (agentclient.Target, bool, error)
	}
}

func (g rankedLivenessGate) RequireReachable(ctx context.Context, agentPublicID string) error {
	if g.gw != nil && g.gw.Connected(agentPublicID) {
		return nil // live socket
	}
	if g.resolver != nil {
		if _, ok, err := g.resolver.PlayTarget(ctx, agentPublicID); err == nil && ok {
			return nil // verified, resolvable hosted endpoint
		}
	}
	return matchmaking.ErrAgentOffline
}

// finishHook fans the match-finalize signal out to the engagement workers. Both
// Enqueue calls are non-blocking, satisfying match.FinishHook's no-block contract.
type finishHook struct {
	clips  *clips.Service
	social *social.Service
}

func (h finishHook) MatchFinished(_ context.Context, matchPublicID string) {
	h.clips.Enqueue(matchPublicID)
	h.social.Enqueue(matchPublicID)
}

// tourneyBank moves a tournament's prize pool through the ledger (sponsor →
// escrow on fund, escrow → champion on payout). Both posts are idempotent on the
// tournament id, so a retried fund/payout is a no-op. Satisfies tournament.Bank.
type tourneyBank struct{ l *ledger.Service }

func (b tourneyBank) FundPool(ctx context.Context, tournamentPublicID string, coins int64) error {
	_, err := b.l.Post(ctx, ledger.Txn{
		Kind:     ledger.KindTopup,
		Key:      "fund:tourney:" + tournamentPublicID,
		Metadata: map[string]any{"tournament": tournamentPublicID, "coins": coins, "source": "sponsor"},
		Postings: []ledger.Posting{
			{Wallet: ledger.SystemWallet(ledger.SysStripeClearing), Amount: -coins},
			{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: coins},
		},
	})
	return err
}

func (b tourneyBank) PayWinner(ctx context.Context, tournamentPublicID, agentPublicID string, coins int64) error {
	_, err := b.l.Post(ctx, ledger.Txn{
		Kind:     ledger.KindSettle,
		Key:      "payout:tourney:" + tournamentPublicID,
		Metadata: map[string]any{"tournament": tournamentPublicID, "winner": agentPublicID, "coins": coins},
		Postings: []ledger.Posting{
			{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: -coins},
			{Wallet: ledger.AgentWallet(agentPublicID), Amount: coins},
		},
	})
	return err
}

// payoutRails adapts the Solana RPC client to payout.TokenAccountReader, so the payout
// package can verify its own rails without importing the blockchain package.
//
// ErrNotToken becomes a ZERO identity rather than an error, because the payout check has
// to tell "that address is not a token account" (a real misconfiguration, report it)
// from "the RPC did not answer" (unknown, stay quiet). Collapsing both into an error
// would make a flaky endpoint look like a broken deployment.
type payoutRails struct{ c *blockchain.Client }

func (p payoutRails) TokenAccount(ctx context.Context, tokenAccount string) (payout.TokenAccountIdentity, error) {
	info, err := p.c.TokenAccount(ctx, tokenAccount)
	if errors.Is(err, blockchain.ErrNotToken) {
		return payout.TokenAccountIdentity{}, nil
	}
	if err != nil {
		return payout.TokenAccountIdentity{}, err
	}
	return payout.TokenAccountIdentity{Mint: info.Mint, Owner: info.Owner}, nil
}

// payoutBank moves coins for the cash-out flow through the ledger. Hold locks the
// agent's coins in escrow; Payout burns them (fee → revenue, rest → clearing as
// cash leaves); Release returns them. All idempotent on the withdrawal id, so
// retries are single-effect. Satisfies payout.Bank.
type payoutBank struct{ l *ledger.Service }

func (b payoutBank) Hold(ctx context.Context, withdrawalID, agentPublicID string, coins int64) error {
	_, err := b.l.Post(ctx, ledger.Txn{
		Kind: "withdraw_hold", Key: "wh-hold:" + withdrawalID,
		Metadata: map[string]any{"withdrawal": withdrawalID, "agent": agentPublicID, "coins": coins},
		Postings: []ledger.Posting{
			{Wallet: ledger.AgentWallet(agentPublicID), Amount: -coins},
			{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: coins},
		},
	})
	return err
}

// SweepFromTreasury moves the owner's treasury coins onto the agent's wallet so the
// escrow hold below can debit them. Same two postings as wallet.Allocate — this is that
// move, initiated by the payout path instead of by the owner — but keyed on the
// withdrawal id so a retried request cannot move the treasury twice.
func (b payoutBank) SweepFromTreasury(ctx context.Context, withdrawalID, ownerUserPublicID, agentPublicID string, coins int64) error {
	_, err := b.l.Post(ctx, ledger.Txn{
		Kind: ledger.KindAllocate, Key: "wh-sweep:" + withdrawalID,
		Metadata: map[string]any{
			"withdrawal": withdrawalID, "user": ownerUserPublicID,
			"agent": agentPublicID, "coins": coins, "reason": "withdrawal_funding",
		},
		Postings: []ledger.Posting{
			{Wallet: ledger.UserWallet(ownerUserPublicID), Amount: -coins},
			{Wallet: ledger.AgentWallet(agentPublicID), Amount: coins},
		},
	})
	return err
}

func (b payoutBank) Release(ctx context.Context, withdrawalID, agentPublicID string, coins int64) error {
	_, err := b.l.Post(ctx, ledger.Txn{
		Kind: "withdraw_release", Key: "wh-release:" + withdrawalID,
		Metadata: map[string]any{"withdrawal": withdrawalID, "agent": agentPublicID, "coins": coins},
		Postings: []ledger.Posting{
			{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: -coins},
			{Wallet: ledger.AgentWallet(agentPublicID), Amount: coins},
		},
	})
	return err
}

func (b payoutBank) Payout(ctx context.Context, withdrawalID, agentPublicID string, coins, feeCoins int64) error {
	_, err := b.l.Post(ctx, ledger.Txn{
		Kind: "withdraw_payout", Key: "wh-payout:" + withdrawalID,
		Metadata: map[string]any{"withdrawal": withdrawalID, "agent": agentPublicID, "coins": coins, "fee": feeCoins},
		Postings: []ledger.Posting{
			{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: -coins},
			{Wallet: ledger.SystemWallet(ledger.SysPlatformRevenue), Amount: feeCoins},
			{Wallet: ledger.SystemWallet(ledger.SysStripeClearing), Amount: coins - feeCoins},
		},
	})
	return err
}

// ReversePayout undoes a burned payout when Stripe reverses the transfer: the exact
// inverse of Payout (clearing + revenue back out, coins back to the agent). Balanced
// and idempotent on the withdrawal id.
func (b payoutBank) ReversePayout(ctx context.Context, withdrawalID, agentPublicID string, coins, feeCoins int64) error {
	_, err := b.l.Post(ctx, ledger.Txn{
		Kind: "withdraw_reversal", Key: "wh-reversal:" + withdrawalID,
		Metadata: map[string]any{"withdrawal": withdrawalID, "agent": agentPublicID, "coins": coins, "fee": feeCoins},
		Postings: []ledger.Posting{
			{Wallet: ledger.SystemWallet(ledger.SysStripeClearing), Amount: -(coins - feeCoins)},
			{Wallet: ledger.SystemWallet(ledger.SysPlatformRevenue), Amount: -feeCoins},
			{Wallet: ledger.AgentWallet(agentPublicID), Amount: coins},
		},
	})
	return err
}

// goofspielRater adapts rating.Service to matchmaking.RatingSource: the ranked queue
// is Goofspiel-only, so band placement uses the agent's Goofspiel arena rating.
type goofspielRater struct{ r *rating.Service }

func (g goofspielRater) Elo(ctx context.Context, agentPublicID string) (int, error) {
	return g.r.Elo(ctx, agentPublicID, rating.GameGoofspiel)
}

// raterAdapter bridges match.Rater to rating.Service, mapping the match's seat
// view onto the rating input.
type raterAdapter struct{ r *rating.Service }

func (a raterAdapter) Rate(ctx context.Context, rr match.RatingResult) error {
	// Integrity travels with the result. Dropping it here would silently restore the bug
	// this adapter sits in the middle of: money voided, rating applied anyway.
	res := rating.MatchResult{
		MatchPublicID: rr.MatchPublicID, Game: rr.Game, Integrity: rr.Integrity,
	}
	for _, p := range rr.Players {
		placement := p.Placement
		if placement == 0 {
			// 2-player fallback: derive placement from the winning seat. A tie
			// (WinnerSeat == gs.Tie) makes both placement 1.
			switch {
			case rr.WinnerSeat < 0: // tie sentinel
				placement = 1
			case p.Seat == rr.WinnerSeat:
				placement = 1
			default:
				placement = 2
			}
		}
		res.Players = append(res.Players, rating.PlayerResult{
			AgentPublicID: p.AgentPublicID, Seat: p.Seat,
			Placement: placement, CoinsDelta: p.CoinsDelta,
		})
	}
	return a.r.Rate(ctx, res)
}

// awarderFunc adapts a plain function to llmgw.Awarder.
type awarderFunc func(ctx context.Context, agentPublicID string) error

func (f awarderFunc) AwardVerified(ctx context.Context, agentPublicID string) error {
	return f(ctx, agentPublicID)
}


// mafiaActRecorder adapts the store to the shape mafia asks for, so internal/mafia does not
// import internal/store. Same reasoning as the game services generally: the engine never imports persistence.
type mafiaActRecorder struct{ repo *store.PIndexRepo }

func (a mafiaActRecorder) RecordActDecision(ctx context.Context, d mafia.ActDecision) error {
	return a.repo.RecordActDecision(ctx, store.ActDecision{
		MatchID: d.MatchID, AgentPublicID: d.AgentPublicID, Game: "mafia",
		Seq: d.Seq, Round: d.Round, Action: d.Action, Outcome: d.Outcome,
		InputJSON: d.InputJSON,
	})
}

func (a mafiaActRecorder) AggregateSeatBenchmark(ctx context.Context, matchID, game string, results map[string]string) error {
	return a.repo.AggregateSeatBenchmark(ctx, matchID, game, results)
}

// readyRequeue returns a seat that missed its ready window to the matchmaking queue.
//
// A thin adapter rather than a dependency from match → matchmaking: the match service must not
// know how agents are queued, only that a dropped seat gets another chance. Missing a window
// costs a place, not coins — nothing was escrowed — and without this it would silently cost a
// place in the arena too.
type readyRequeue struct{ svc *matchmaking.Service }

func (r readyRequeue) Requeue(ctx context.Context, agentPublicID, ownerPublicID string, bid int64) error {
	_, err := r.svc.Enqueue(ctx, agentPublicID, ownerPublicID, bid)
	return err
}
