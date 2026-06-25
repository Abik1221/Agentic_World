// Command server is the single entry point and composition root for Agent Arena.
// It is the only place that constructs concrete dependencies and wires them
// together; every other package depends on interfaces, not on each other's
// internals. See docs/architecture/project-layout.md.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/agent-arena/arena/internal/antifraud"
	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/clips"
	"github.com/agent-arena/arena/internal/config"
	"github.com/agent-arena/arena/internal/health"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/identity"
	"github.com/agent-arena/arena/internal/ledger"
	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/matchmaking"
	"github.com/agent-arena/arena/internal/middleware"
	"github.com/agent-arena/arena/internal/openapi"
	"github.com/agent-arena/arena/internal/payments"
	"github.com/agent-arena/arena/internal/payout"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/profiles"
	"github.com/agent-arena/arena/internal/rating"
	"github.com/agent-arena/arena/internal/social"
	"github.com/agent-arena/arena/internal/spectator"
	"github.com/agent-arena/arena/internal/store"
	"github.com/agent-arena/arena/internal/tournament"
	"github.com/agent-arena/arena/internal/verification"
	"github.com/agent-arena/arena/internal/wallet"
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
	slog.SetDefault(log)
	log.Info("starting agent-arena", "env", cfg.Env, "version", version, "port", cfg.Port)

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
	if cfg.IsProd() && cfg.XBearerToken == "" {
		log.Warn("X_BEARER_TOKEN unset in a prod-like env: onboarding is using the DEV claim verifier — DO NOT run real onboarding like this")
	}

	jwt := auth.NewJWT(cfg.JWTSigningKey, cfg.DashboardTokenTTL)
	idRepo := store.NewIdentityRepo(st.DB)
	idSvc := identity.New(idRepo, verifier, captcha, jwt, clock, cfg.APIKeyPepper, cfg.ClaimTTL)
	authn := auth.NewAuthenticator(idSvc, jwt, log)

	limiter := store.NewRateLimiter(st.Redis)
	registerRL := middleware.RateLimit(limiter, 5, time.Hour, middleware.IPKey("register"))
	idHandler := identity.NewHandler(idSvc, authn, registerRL)

	// Verification (built in Stage 1) is wired into the match flow now.
	verSvc := verification.New(store.NewVerificationRepo(st.DB))

	// Money: the ledger is the only coin-mover; the wallet service layers
	// stake/settle/refund and the seven spending limits on top, and serves the
	// read-only /v1/wallet surface. One wallet service satisfies both the match
	// Limits and Wallet ports stubbed in Stage 3.
	ledgerSvc := ledger.New(store.NewLedgerRepo(st.DB), metrics.Registry())
	walletSvc := wallet.New(ledgerSvc, store.NewWalletRepo(st.DB), clock,
		wallet.Config{SessionWindow: cfg.SessionWindow}, metrics.Registry())
	walletHandler := wallet.NewHandler(walletSvc, authn, cfg.AllowMint)

	// Trust & anti-fraud: the payout gate holds suspect settlements (escrow kept),
	// the detector flags collusion/human-timing, and disputes drive admin review.
	// SetPayoutGate wires it into settlement after construction (the wallet is the
	// gate's settler, so they can't both be constructor args).
	antifraudSvc := antifraud.New(store.NewAntifraudRepo(st.DB), walletSvc, clock,
		antifraud.Config{CollusionMinGames: cfg.CollusionMinGames, DetectLookback: cfg.CollusionLookback},
		log, metrics.Registry())
	walletSvc.SetPayoutGate(antifraudSvc)
	antifraudHandler := antifraud.NewHandler(antifraudSvc, authn, cfg.AdminUserIDs)

	// Spectator: the SSE hub is the real Broadcaster (replaces the Stage 3 no-op).
	// It reuses the match repo as its Last-Event-ID backlog source. Fan-out is
	// non-blocking + drop-slow, so a stalled watcher can never delay a match.
	matchRepo := store.NewMatchRepo(st.DB)
	hub := spectator.NewHub(matchRepo, cfg.DefaultRounds, log, metrics.Registry())
	specHandler := spectator.NewHandler(hub, spectator.NewLive(store.NewSpectatorRepo(st.DB), clock))

	// Ratings & profiles: ELO is applied at match finalize (idempotently, keyed by
	// match id) and powers the leaderboard + agent profiles + /v1/agent/stats.
	ratingSvc := rating.New(store.NewRatingRepo(st.DB), clock,
		rating.Config{K: cfg.RatingK, SeasonLength: cfg.SeasonLength}, metrics.Registry())
	ratingHandler := rating.NewHandler(ratingSvc)
	profilesSvc := profiles.New(store.NewProfilesRepo(st.DB), ratingSvc.CurrentSeason)
	profilesHandler := profiles.NewHandler(profilesSvc, authn)

	// Engagement: clips (dramatic-moment detection + async asset render) and social
	// (follows + notification fan-out). Both run on bounded worker pools off the
	// hot path; the match finish hook only enqueues (never blocks finalize).
	clipsSvc := clips.New(store.NewClipsRepo(st.DB), matchRepo, clips.NewDevGenerator(cfg.ClipCDNBase),
		clips.Config{}, log, metrics.Registry())
	clipsHandler := clips.NewHandler(clipsSvc)
	socialSvc := social.New(store.NewSocialRepo(st.DB), social.Config{}, log, metrics.Registry())
	socialHandler := social.NewHandler(socialSvc, authn)

	// Funded freeroll (Stage 10): the prize pool moves through the ledger via the
	// Bank adapter; entry is gated on the tournament_ready badge + no fraud flags.
	tournamentSvc := tournament.New(store.NewTournamentRepo(st.DB), tourneyBank{ledgerSvc}, metrics.Registry())
	tournamentHandler := tournament.NewHandler(tournamentSvc, authn, cfg.AdminUserIDs)

	// Cash-out (coins → money): request → admin approve → Stripe payout. Coins are
	// held in escrow on request and burned only on a confirmed payout. The live
	// Stripe transferrer is selected whenever a secret key is configured.
	var transferrer payout.Transferrer = payout.DevTransferrer{}
	if cfg.StripeSecretKey != "" {
		transferrer = payout.NewStripeTransferrer(cfg.StripeSecretKey)
	}
	payoutSvc := payout.New(store.NewPayoutRepo(st.DB), payoutBank{ledgerSvc}, transferrer, clock,
		payout.Config{
			CoinCents: cfg.CoinCents, SellFeePct: cfg.WithdrawSellFeePct,
			StripeFeePct: cfg.StripePayoutFeePct, StripeFeeFlatCents: cfg.StripePayoutFeeFlatCents,
			MinCoins: cfg.WithdrawMinCoins, Clearing: cfg.WithdrawClearing,
		}, log, metrics.Registry())
	payoutHandler := payout.NewHandler(payoutSvc, authn, cfg.AdminUserIDs)

	// Match lifecycle: real engine + persistence + per-match Redis lock, real coin
	// escrow/settlement + limit enforcement, live broadcast, ELO at finalize, and
	// engagement hooks (clips + notifications) fired off the hot path.
	matchSvc := match.New(
		matchRepo,
		store.NewLocker(st.Redis),
		walletSvc, walletSvc, hub,
		verifierAdapter{verSvc},
		raterAdapter{ratingSvc},
		finishHook{clips: clipsSvc, social: socialSvc},
		clock,
		match.Config{MoveWindow: cfg.MoveWindow, RakePct: cfg.RakePct, Rounds: cfg.DefaultRounds, LockTTL: 10 * time.Second},
	)
	// Low-latency wake-ups for long-polling agents (Redis pub/sub, cross-instance).
	// Set after construction so a notifier-less build still works (no-op fallback).
	matchSvc.SetNotifier(store.NewNotifier(st.Redis))
	matchHandler := match.NewHandler(matchSvc, authn)

	// Matchmaking: a server-driven, skill-banded queue replaces grabbing matches[0]
	// from the open lobby. The matcher pairs agents within a rating band that widens
	// over wait time (never same-owner) and seats them in an already-active match —
	// making ratings load-bearing and removing the deterministic-rendezvous collusion
	// vector. The Pairer is match.CreatePaired; ratings come from the rating service.
	matchmakingSvc := matchmaking.New(
		store.NewMatchmakingRepo(st.DB),
		matchPairer{matchSvc}, ratingSvc, clock,
		matchmaking.Config{}, log, metrics.Registry(),
	)
	matchmakingHandler := matchmaking.NewHandler(matchmakingSvc, authn)

	// Payments: real money → coins via Stripe Checkout, with idempotent webhook
	// processing into the ledger. A configured secret key selects the live Stripe
	// gateway; otherwise the offline DevGateway runs the whole flow locally,
	// immediately crediting coins to simulate webhook receipt (tests full flow offline).
	var gateway payments.Gateway = payments.NewDevGateway(walletSvc)
	if cfg.StripeSecretKey != "" {
		gateway = payments.NewStripeGateway(cfg.StripeSecretKey)
	} else {
		log.Warn("STRIPE_SECRET_KEY unset: payments using the offline DevGateway (no real charges, coins credited immediately)")
	}
	paymentsSvc := payments.New(gateway, walletSvc, store.NewPaymentsRepo(st.DB), clock,
		payments.Config{
			Packs:             payments.DefaultPacks(),
			SuccessURL:        cfg.CheckoutSuccessURL,
			CancelURL:         cfg.CheckoutCancelURL,
			ConnectReturnURL:  cfg.ConnectReturnURL,
			ConnectRefreshURL: cfg.ConnectRefreshURL,
			WebhookSecret:     cfg.StripeWebhookSecret,
		}, log, metrics.Registry())
	paymentsHandler := payments.NewHandler(paymentsSvc, authn)

	// Background: the move-window timeout sweeper + ledger reconciliation + the
	// Stripe↔ledger reconciliation job (all safe to run on every instance).
	go match.NewSweeper(matchSvc, log, time.Second).Run(ctx)
	go matchmakingSvc.NewMatcher().Run(ctx)
	go ledgerSvc.NewReconciler(log, cfg.ReconcileInterval).Run(ctx)
	go paymentsSvc.NewReconciler(cfg.PaymentsReconcileInterval).Run(ctx)
	go clipsSvc.Run(ctx)
	go socialSvc.Run(ctx)
	go antifraudSvc.NewDetector(cfg.DetectInterval).Run(ctx)

	// 8. HTTP server with the standard middleware chain. Modules attach their
	//    routes via registrars (httpx stays decoupled from every module).
	router := httpx.NewRouter(
		httpx.Deps{Config: cfg, Logger: log, Metrics: metrics},
		healthH.Register,
		openapi.NewHandler().Register,
		idHandler.Register,
		matchHandler.Register,
		matchmakingHandler.Register,
		walletHandler.Register,
		paymentsHandler.Register,
		specHandler.Register,
		ratingHandler.Register,
		profilesHandler.Register,
		clipsHandler.Register,
		socialHandler.Register,
		antifraudHandler.Register,
		tournamentHandler.Register,
		payoutHandler.Register,
	)
	srv := httpx.NewServer(cfg, router, log)

	// 9. Serve until shutdown, then drain.
	return srv.Run(ctx)
}

// matchPairer bridges matchmaking.Pairer to match.Service.CreatePaired, so the
// matchmaker creates an already-active two-seat match without importing match's
// internals.
type matchPairer struct{ m *match.Service }

func (p matchPairer) CreatePaired(ctx context.Context, aAgent, aOwner, bAgent, bOwner string, bid int64) (string, error) {
	return p.m.CreatePaired(ctx, aAgent, aOwner, bAgent, bOwner, bid)
}

// verifierAdapter bridges verification.Service to match.Verifier.
type verifierAdapter struct{ v *verification.Service }

func (a verifierAdapter) Record(ctx context.Context, agentPublicID string, matchPublicID *string, responseMs int) {
	_ = a.v.Record(ctx, agentPublicID, matchPublicID, responseMs)
}

func (a verifierAdapter) CheckEligible(ctx context.Context, agentPublicID string) error {
	e, err := a.v.CheckEligibility(ctx, agentPublicID)
	if err != nil {
		return err
	}
	if !e.Eligible {
		return httpx.NewError(http.StatusUnprocessableEntity, "verification_pending", "Agent flagged for review: "+e.Reason)
	}
	return nil
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

// raterAdapter bridges match.Rater to rating.Service, mapping the match's seat
// view onto the rating input.
type raterAdapter struct{ r *rating.Service }

func (a raterAdapter) Rate(ctx context.Context, rr match.RatingResult) error {
	var res rating.MatchResult
	res.MatchPublicID = rr.MatchPublicID
	res.WinnerSeat = rr.WinnerSeat
	for _, p := range rr.Players {
		if p.Seat == 0 || p.Seat == 1 {
			res.Agents[p.Seat] = p.AgentPublicID
			res.CoinsDelta[p.Seat] = p.CoinsDelta
		}
	}
	return a.r.Rate(ctx, res)
}
