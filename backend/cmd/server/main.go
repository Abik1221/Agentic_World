// Command server is the single entry point and composition root for Agent Arena.
// It is the only place that constructs concrete dependencies and wires them
// together; every other package depends on interfaces, not on each other's
// internals. See docs/architecture/project-layout.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	"github.com/agent-arena/arena/internal/demo"
	"github.com/agent-arena/arena/internal/devprofile"
	"github.com/agent-arena/arena/internal/events"
	"github.com/agent-arena/arena/internal/gamestakes"
	"github.com/agent-arena/arena/internal/health"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/identity"
	"github.com/agent-arena/arena/internal/ledger"
	"github.com/agent-arena/arena/internal/mafia"
	"github.com/agent-arena/arena/internal/manifest"
	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/matchmaking"
	"github.com/agent-arena/arena/internal/middleware"
	"github.com/agent-arena/arena/internal/monopoly"
	"github.com/agent-arena/arena/internal/openapi"
	"github.com/agent-arena/arena/internal/payments"
	"github.com/agent-arena/arena/internal/payout"
	"github.com/agent-arena/arena/internal/pindex"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/agent-arena/arena/internal/platformcfg"
	"github.com/agent-arena/arena/internal/platformsign"
	"github.com/agent-arena/arena/internal/profiles"
	"github.com/agent-arena/arena/internal/rating"
	"github.com/agent-arena/arena/internal/sandbox"
	"github.com/agent-arena/arena/internal/secretbox"
	"github.com/agent-arena/arena/internal/social"
	"github.com/agent-arena/arena/internal/solanadeposit"
	"github.com/agent-arena/arena/internal/spectator"
	"github.com/agent-arena/arena/internal/store"
	"github.com/agent-arena/arena/internal/subscription"
	"github.com/agent-arena/arena/internal/telemetrybridge"
	"github.com/agent-arena/arena/internal/tournament"
	"github.com/agent-arena/arena/internal/twofa"
	"github.com/agent-arena/arena/internal/verification"
	"github.com/agent-arena/arena/internal/wallet"
	"github.com/agent-arena/arena/internal/walletadmin"
	"github.com/agent-arena/arena/internal/walletrecon"
	"github.com/agent-arena/arena/internal/walletverify"
	"github.com/agent-arena/arena/internal/webhook"
)

// version is injected at build time via -ldflags "-X main.version=$(git rev-parse --short HEAD)".
var version = "dev"

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
	registerRL := middleware.RateLimitFailover(limiter, localRL, 5, time.Hour, middleware.IPKey("register"))
	loginRL := middleware.RateLimitFailover(limiter, localRL, 10, time.Minute, middleware.IPKey("login"))
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
	idHandler.SetKeysRateLimit(keysRL)

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
	manifestSvc := manifest.New(store.NewManifestRepo(st.DB), manifestProbe, manifestSealer)
	manifestHandler := manifest.NewHandler(manifestSvc, authn)
	// Benchmark agent metadata: resolve each agent's active manifest at match time
	// so the benchmark fact carries trusted, server-side version + declared model
	// provider/model (for version-diff and provider benchmarks). Telemetry only.
	if lens.Enabled() {
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
	agentGateway := newAgentGateway(manifestSvc, idSvc, platformCfg, lens, log)

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
	verSvc := verification.New(store.NewVerificationRepo(st.DB))

	// Money: the ledger is the only coin-mover; the wallet service layers
	// stake/settle/refund and the seven spending limits on top, and serves the
	// read-only /v1/wallet surface. One wallet service satisfies both the match
	// Limits and Wallet ports stubbed in Stage 3.
	ledgerSvc := ledger.New(store.NewLedgerRepo(st.DB), metrics.Registry())
	walletSvc := wallet.New(ledgerSvc, store.NewWalletRepo(st.DB), clock,
		wallet.Config{SessionWindow: cfg.SessionWindow, CoinCents: cfg.CoinCents}, metrics.Registry())
	walletHandler := wallet.NewHandler(walletSvc, authn, cfg.AllowMint)

	// Super Admin wallet controls (P4): runtime settings (deposit/withdrawal
	// switches, maintenance, bounds) + risk actions (freeze, manual adjust). Also
	// the deposit/withdrawal gate — wired into those services below via SetGate.
	walletAdminSvc := walletadmin.New(store.NewWalletAdminRepo(st.DB), walletSvc, clock, log)
	walletAdminHandler := walletadmin.NewHandler(walletAdminSvc, authn, cfg.AdminUserIDs)

	// Game stake tiers: Super-Admin-configurable price bands per game (e.g. Mafia
	// Low/Mid/High). Public read exposes the enabled menu; admin GET/PUT configures
	// it with immediate effect. Play handlers resolve a chosen tier -> coin stake.
	gameStakesSvc := gamestakes.New(store.NewGameStakesRepo(st.DB), clock, log)
	gameStakesHandler := gamestakes.NewHandler(gameStakesSvc, authn, cfg.AdminUserIDs)

	// Wallet-ownership verification: prove control of the payout wallet (sign a
	// nonce) before a withdrawal can be sent there (payout gates on the result).
	walletVerifyHandler := walletverify.NewHandler(walletverify.New(store.NewWalletVerifyRepo(st.DB), clock), authn)

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
	ratingHandler := rating.NewHandler(ratingSvc, cfg.AllowMint)                           // dev-only season force-roll gated with mint
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
			if err := pindexRepo.RecordMatchBenchmark(ctx, ms.MatchID, seat.AgentID,
				int(seat.Decisions), int(seat.Legal), int(seat.Fallbacks), seat.LatencySumMS, seat.TotalTokens); err != nil {
				return err // let the outbox retry
			}
		}
		return nil
	})
	eventBus.On(events.TypeRatingUpdated, pindexSvc.OnRatingUpdated)
	launch("pindex-recompute", pindex.NewWorker(pindexSvc, log, 5*time.Second).Run)

	// Public developer reputation surface (@handle profile, P-Index transparency,
	// match history, developer follow graph), aggregated across a developer's agents.
	devProfileHandler := devprofile.NewHandler(
		devprofile.New(store.NewDevProfileRepo(st.DB), pindexSvc, ratingSvc.CurrentSeason), authn)

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
	// Notification writer shared by the deposit + withdrawal flows (idempotent).
	notifier := notifierAdapter{socialRepo}

	mafiaSvc := mafia.NewService(
		mafiaRepo,
		store.NewLocker(st.Redis),
		walletSvc,
		wallet.NewMafiaWallet(walletSvc),
		mafiaHub,
		verifierAdapter{v: verSvc, cert: manifestSvc, susp: platformCfg},
		finishHook{clips: clipsSvc, social: socialSvc},
		clock,
		mafia.Config{EntryFee: 100, PlatformFeePct: 10, PhaseWindow: cfg.MoveWindow, LockTTL: 15 * time.Second},
	)
	mafiaSvc.SetRater(ratingSvc) // paid tables update the per-arena Mafia rating (TrueSkill)
	mafiaHandler := mafia.NewHandler(mafiaHub, mafiaSvc, authn)
	mafiaHandler.SetStakeResolver(gameStakesSvc) // Low/Mid/High tier → stake, budget-checked
	launch("mafia-sweeper", mafia.NewSweeper(mafiaSvc, log, time.Second).Run)

	// Monopoly (turn-based property game) on the same patterns as Mafia: pure
	// engine → match service → SSE spectator stream → agent action API. The
	// service seats the creator and fills the rest of the table with deterministic
	// server bots, auto-advancing bot turns after each agent move. Wallet is nil
	// for now (practice tables, entry fee 0); wire a MonopolyWallet adapter to
	// pool stakes once staked matchmaking lands.
	monopolyRepo := store.NewMonopolyRepo(st.DB)
	monopolyHub := monopoly.NewHub(monopolyRepo, log)
	monopolySvc := monopoly.NewService(
		monopolyRepo,
		store.NewLocker(st.Redis),
		wallet.NewMonopolyWallet(walletSvc), // staked agent-vs-agent tables: escrow + settle + refund
		monopolyHub,
		nil, // FinishHook
		clock,
		monopoly.Config{PlatformFeePct: 10, MoveWindow: cfg.MoveWindow, LockTTL: 15 * time.Second},
	)
	// Staked-join gates (mirror Mafia): spending budget + certification/suspension.
	monopolySvc.SetLimits(walletSvc)
	monopolySvc.SetVerifier(verifierAdapter{v: verSvc, cert: manifestSvc, susp: platformCfg})
	// Push-play: drive the creator's seat from their hosted endpoint; engine bots
	// fill the rest. Reuses the same match machinery + SSE spectating.
	monopolySvc.EnablePushPlay(manifestSvc, manifestProbe, log)
	monopolySvc.SetWebhookEnqueuer(webhookQueue)
	monopolySvc.SetGateway(agentGateway)                    // play over the socket when the agent is connected
	monopolySvc.SetBenchmark(lens, benchPersist, benchMeta) // per-match decision-quality telemetry
	// Long-poll wake-ups for GET /v1/monopoly/{id}/state?wait=true (parity with Goofspiel).
	monopolySvc.SetNotifier(store.NewNotifier(st.Redis))
	monopolySvc.SetRater(ratingSvc) // paid tables update the per-arena Monopoly rating (TrueSkill)
	monopolyHandler := monopoly.NewHandler(monopolyHub, monopolySvc, authn)
	monopolyHandler.SetStakeResolver(gameStakesSvc) // tier → stake; escrowed + settled via MonopolyWallet
	launch("monopoly-sweeper", monopoly.NewSweeper(monopolySvc, log, time.Second).Run)

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
		sx, err := payout.NewSolanaTransferrer(cfg.SolanaRPCURL, hotSecret, cfg.SolanaUSDCMint, cfg.SolanaPlatformATA, 6)
		if err != nil {
			return err
		}
		transferrer, solanaXfer, payoutChain = sx, sx, payout.ChainSolana
		log.Info("withdrawals: solana USDC rail enabled", "mint", cfg.SolanaUSDCMint)
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
	if solanaXfer != nil {
		// The transferrer also confirms finality; the watcher burns/releases escrow
		// once each broadcast withdrawal reaches a terminal on-chain state.
		payoutSvc.SetConfirmer(solanaXfer)
		launch("payout-confirm-watcher", payout.NewConfirmWatcher(payoutSvc, log, cfg.WithdrawConfirmInterval).Run)
	}
	payoutSvc.SetGate(walletAdminSvc) // Super Admin withdrawal gate (settings/freeze)
	payoutSvc.SetNotifier(notifier)   // notify owner on paid/failed
	payoutHandler := payout.NewHandler(payoutSvc, authn, cfg.AdminUserIDs)
	payoutHandler.SetRateLimit(withdrawRL)
	payoutHandler.SetStepUp(twofaSvc) // require the 2FA code on cash-out when enabled

	// Deposits (Beta wallet pipeline P2): a background listener watches Solana for
	// USDC transfers to the platform token account (tagged by each session's
	// Solana Pay reference) and credits the user's treasury via the same ledger
	// top-up path (peg derived from CoinCents: 1 USDC = 100/CoinCents coins).
	// Enabled only when fully configured; otherwise /v1/deposits returns 503.
	var depositHandler *solanadeposit.Handler
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
		depositSvc.SetNotifier(notifier)   // notify user when a deposit is credited
		depositHandler = solanadeposit.NewHandler(depositSvc, authn)
		depositHandler.SetRateLimit(depositRL)
		launch("solana-deposit-listener", solanadeposit.NewListener(depositSvc, log, cfg.DepositPollInterval).Run)
		// Read-only solvency monitor: reconcile the hot-wallet on-chain USDC against
		// outstanding withdrawal liability and alert on any shortfall (never moves funds).
		solvency := payout.NewSolvencyMonitor(store.NewPayoutRepo(st.DB), chain, cfg.SolanaPlatformATA, log, metrics.Registry())
		launch("solvency-monitor", solvency.Run(cfg.SolvencyInterval))
		log.Info("solana deposits enabled", "mint", cfg.SolanaUSDCMint, "ata", cfg.SolanaPlatformATA)
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

	// Match lifecycle: real engine + persistence + per-match Redis lock, real coin
	// escrow/settlement + limit enforcement, live broadcast, ELO at finalize, and
	// engagement hooks (clips + notifications) fired off the hot path.
	matchSvc := match.New(
		matchRepo,
		store.NewLocker(st.Redis),
		walletSvc, walletSvc, hub,
		verifierAdapter{v: verSvc, cert: manifestSvc, susp: platformCfg},
		raterAdapter{ratingSvc},
		finishHook{clips: clipsSvc, social: socialSvc},
		clock,
		match.Config{MoveWindow: cfg.MoveWindow, RakePct: cfg.RakePct, Rounds: cfg.DefaultRounds, LockTTL: 10 * time.Second},
	)
	// Low-latency wake-ups for long-polling agents (Redis pub/sub, cross-instance).
	// Set after construction so a notifier-less build still works (no-op fallback).
	matchSvc.SetNotifier(store.NewNotifier(st.Redis))
	matchSvc.SetStyleRecorder(styleRepo) // record aggression/efficiency at match finish (best-effort)
	// House-agent move picker for sandbox practice matches (no coins/limits/rating).
	matchSvc.SetBot(bot.NewService())
	if cfg.RankedAutoDrive {
		// Hands-free live-vs-live: drive paired agents over their sockets. Off by
		// default (auto-plays real staked matches) — enable post integration test.
		matchSvc.EnableRankedDrive(agentGateway, manifestSvc, manifestProbe, lens, benchPersist, benchMeta, log)
		log.Info("ranked auto-drive enabled (paired agents driven over their sockets)")
	}
	matchHandler := match.NewHandler(matchSvc, authn)

	// Sandbox: risk-free practice vs the seeded house agents, played through the
	// same match endpoints. Only starting a match is new.
	sandboxSvc := sandbox.New(matchSvc, cfg.SandboxEnabled)
	// Push-play: drive the developer's seat of a sandbox match from their hosted
	// agent endpoint (manifest push model). Reuses the hardened verification client
	// and the same match machinery, so the browser watches it live over SSE.
	sandboxSvc.EnablePushPlay(matchSvc, manifestSvc, manifestProbe, log)
	sandboxSvc.SetWebhookEnqueuer(webhookQueue)
	sandboxSvc.SetGateway(agentGateway)                    // play over the socket when the agent is connected
	sandboxSvc.SetBenchmark(lens, benchPersist, benchMeta) // per-match decision-quality telemetry
	sandboxHandler := sandbox.NewHandler(sandboxSvc, authn)

	// Matchmaking: a server-driven, skill-banded queue replaces grabbing matches[0]
	// from the open lobby. The matcher pairs agents within a rating band that widens
	// over wait time (never same-owner) and seats them in an already-active match —
	// making ratings load-bearing and removing the deterministic-rendezvous collusion
	// vector. The Pairer is match.CreatePaired; ratings come from the rating service.
	matchmakingSvc := matchmaking.New(
		store.NewMatchmakingRepo(st.DB),
		matchPairer{matchSvc}, goofspielRater{ratingSvc}, clock,
		matchmaking.Config{}, log, metrics.Registry(),
	)
	// Ranked queue entry gate: certified AND not suspended. Enforcing suspension
	// here (not just at CreatePaired) means a suspended agent fails fast at enqueue
	// with a specific error, instead of getting a misleading 202 and squatting a
	// `waiting` slot forever for a pairing that CheckEligible would always reject —
	// the same fail-fast principle SetAffordability applies to broke/over-limit agents.
	matchmakingSvc.SetEligibility(rankedEntryGate{cert: manifestSvc, susp: platformCfg})
	matchmakingSvc.SetAffordability(walletSvc) // reject unaffordable/over-limit stakes at enqueue (no stuck-waiting)
	matchmakingHandler := matchmaking.NewHandler(matchmakingSvc, authn)
	matchmakingHandler.SetStakeResolver(gameStakesSvc) // ranked queue by Low/Mid/High tier

	// Auto-play: devs flip availability on their agent (settings API below); the
	// reconciler loop (launched only when AUTOPLAY_ENABLED) keeps them in matches.
	autoplayRepo := store.NewAutoplayRepo(st.DB)
	autoplayHandler := autoplay.NewHandler(autoplayRepo, authn)

	// Payments: real money → coins via Stripe Checkout, with idempotent webhook
	// processing into the ledger. A configured secret key selects the live Stripe
	// gateway; otherwise the offline DevGateway runs the whole flow locally.
	//
	// Guard: in production/staging we must NEVER silently run the DevGateway (it
	// credits coins with NO real charge) or accept unsigned webhooks — refuse to
	// boot without real Stripe configuration.
	if cfg.IsProd() {
		var missing []string
		if cfg.StripeSecretKey == "" {
			missing = append(missing, "STRIPE_SECRET_KEY")
		}
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
			return fmt.Errorf("payments: %s=%q requires real Stripe configuration; missing/placeholder: %s "+
				"(refusing to run the offline DevGateway that credits coins with no charge)",
				"APP_ENV", cfg.Env, strings.Join(missing, ", "))
		}
	}

	var gateway payments.Gateway = &payments.DevGateway{}
	devPayments := true
	if cfg.StripeSecretKey != "" {
		gateway = payments.NewStripeGateway(cfg.StripeSecretKey)
		devPayments = false
	} else {
		log.Warn("STRIPE_SECRET_KEY unset: payments using the offline DevGateway (no real charges, coins credited immediately)")
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

	var billingGW subscription.BillingGateway = subscription.DevBillingGateway{}
	if cfg.StripeSecretKey != "" {
		billingGW = subscription.NewStripeBillingGateway(cfg.StripeSecretKey)
	}
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
	if cfg.AutoplayEnabled {
		autoplaySvc := autoplay.New(
			autoplayRepo,
			rankedQueueAdapter{mm: matchmakingSvc},
			sandboxStarterAdapter{
				throttle: autoplay.NewSandboxThrottle(5 * time.Minute),
				goof:     sandboxSvc, mafia: mafiaSvc, monopoly: monopolySvc,
			},
			autoplay.Config{},
			log,
		)
		// Feed the stop-conditions today's matches/tokens (benchmark aggregate) +
		// losses (wallet); schedule works without it.
		autoplaySvc.SetStats(autoplayStats{pindex: pindexRepo, wallet: walletSvc, now: clock.Now})
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
			// Mafia push-play needs a full roster: seat the developer's agent (via
			// their endpoint) and fill the other seats with these demo bots.
			botSeats := make([]mafia.BotAgent, 0, len(agents))
			for _, a := range agents {
				botSeats = append(botSeats, mafia.BotAgent{PublicID: a.PublicID, OwnerPublicID: a.OwnerPublicID})
			}
			mafiaSvc.EnablePushPlay(manifestSvc, manifestProbe, botSeats, log)
			mafiaSvc.SetWebhookEnqueuer(webhookQueue)
			mafiaSvc.SetGateway(agentGateway)                    // play over the socket when the agent is connected
			mafiaSvc.SetBenchmark(lens, benchPersist, benchMeta) // per-match decision-quality telemetry
		}
	}
	// Long-poll wake-ups for GET /v1/mafia/{id}/state?wait=true (parity with Goofspiel).
	mafiaSvc.SetNotifier(store.NewNotifier(st.Redis))

	// 8. HTTP server with the standard middleware chain.
	mounts := []httpx.Mount{
		healthH.Register,
		openapi.NewHandler().Register,
		idHandler.Register,
		manifestHandler.Register,
		agentGateway.Register,
		mountAgentStatus(authn, agentGateway),
		mountCapabilities(xClaimEnabled, cfg.DepositsEnabled(), !cfg.IsProd()),
		matchHandler.Register,
		matchmakingHandler.Register,
		autoplayHandler.Register,
		sandboxHandler.Register,
		walletHandler.Register,
		paymentsHandler.Register,
		subscriptionHandler.Register,
		specHandler.Register,
		mafiaHandler.Register,
		monopolyHandler.Register,
		ratingHandler.Register,
		profilesHandler.Register,
		devProfileHandler.Register,
		arena.NewHandler().Register, // public GET /v1/arenas (SDK discovery)
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
	}
	if depositHandler != nil {
		mounts = append(mounts, depositHandler.Register)
	}
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

// notifierAdapter lets the deposit + withdrawal services write user notifications
// through the shared, idempotent notifications table (store.SocialRepo) without
// importing store. Satisfies solanadeposit.Notifier and payout.Notifier.
type notifierAdapter struct{ repo *store.SocialRepo }

func (n notifierAdapter) Notify(ctx context.Context, userPublicID, kind, ref string, payload []byte) error {
	_, err := n.repo.InsertNotification(ctx, userPublicID, kind, ref, payload)
	return err
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
// ranked queue only if it is not Super-Admin-suspended AND is certified. The
// authoritative money/play gate is still CreatePaired (verifierAdapter.CheckEligible);
// this just fails suspended/uncertified agents fast at enqueue rather than letting
// them sit in `waiting` for a pairing that can never escrow.
type rankedEntryGate struct {
	cert *manifest.Service
	susp *platformcfg.Provider // may be nil (suspension list unavailable)
}

func (g rankedEntryGate) RequireCertified(ctx context.Context, agentPublicID string) error {
	if g.susp != nil && g.susp.Get().IsSuspended(agentPublicID) {
		return httpx.NewError(http.StatusForbidden, "agent_suspended", "This agent has been suspended by platform administrators.")
	}
	return g.cert.RequireCertified(ctx, agentPublicID)
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
	res := rating.MatchResult{MatchPublicID: rr.MatchPublicID, Game: rr.Game}
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
