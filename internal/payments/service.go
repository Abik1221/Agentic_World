package payments

import (
	"context"
	"log/slog"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
)

// Config holds the payment surface's tunables. URLs are hosted pages we own;
// Stripe redirects the buyer back to them. The webhook secret verifies inbound
// events; an empty secret means inbound webhooks are rejected (ErrNotConfigured).
type Config struct {
	Packs             []Pack
	SuccessURL        string
	CancelURL         string
	ConnectReturnURL  string
	ConnectRefreshURL string
	WebhookSecret     string
	WebhookTolerance  time.Duration
	ReconcileLookback time.Duration
}

// Service orchestrates the payment flows over the Gateway, Coiner and Repo ports.
type Service struct {
	gw     Gateway
	coiner Coiner
	repo   Repo
	clock  platform.Clock
	cfg    Config
	log    *slog.Logger
	m      *metrics
	packs  map[string]Pack
}

// New builds the payments service and registers its collectors on reg.
func New(gw Gateway, coiner Coiner, repo Repo, clock platform.Clock, cfg Config, log *slog.Logger, reg *prometheus.Registry) *Service {
	if cfg.WebhookTolerance <= 0 {
		cfg.WebhookTolerance = 5 * time.Minute
	}
	if cfg.ReconcileLookback <= 0 {
		cfg.ReconcileLookback = 24 * time.Hour
	}
	packs := make(map[string]Pack, len(cfg.Packs))
	for _, p := range cfg.Packs {
		packs[p.Key] = p
	}
	return &Service{gw: gw, coiner: coiner, repo: repo, clock: clock, cfg: cfg, log: log, m: newMetrics(reg), packs: packs}
}

// Packs returns the configured price list (for a public pack-listing endpoint).
func (s *Service) Packs() []Pack { return s.cfg.Packs }

// Topup opens a hosted Checkout session for `packKey` that will credit
// `agentPublicID` on success. The caller must own the agent.
func (s *Service) Topup(ctx context.Context, userPublicID, agentPublicID, packKey string) (Checkout, error) {
	pack, ok := s.packs[packKey]
	if !ok {
		return Checkout{}, ErrUnknownPack
	}
	owner, err := s.repo.OwnerOfAgent(ctx, agentPublicID)
	if err != nil {
		return Checkout{}, httpx.ErrNotFound
	}
	if owner != userPublicID {
		return Checkout{}, ErrForbiddenAgent
	}
	return s.gw.CreateCheckout(ctx, CheckoutParams{
		Pack: pack, UserPublicID: userPublicID, AgentPublicID: agentPublicID,
		SuccessURL: s.cfg.SuccessURL, CancelURL: s.cfg.CancelURL,
	})
}

// Onboard returns a Stripe Connect Express KYC link for the user, creating and
// persisting a Connect account on first call. Payout EXECUTION stays gated behind
// the Stage 4 validation gate — onboarding only collects KYC (Tier 2 groundwork).
func (s *Service) Onboard(ctx context.Context, userPublicID string) (string, error) {
	existing, err := s.repo.StripeConnectID(ctx, userPublicID)
	if err != nil {
		return "", err
	}
	accountID, err := s.gw.EnsureConnectAccount(ctx, existing, userPublicID)
	if err != nil {
		return "", err
	}
	if accountID != existing {
		if err := s.repo.SetStripeConnectID(ctx, userPublicID, accountID); err != nil {
			return "", err
		}
	}
	return s.gw.CreateOnboardingLink(ctx, accountID, s.cfg.ConnectReturnURL, s.cfg.ConnectRefreshURL)
}

// HandleWebhook verifies, persists (idempotently), and processes one inbound
// Stripe event. It is safe to call repeatedly for the same event: the event log
// dedupes and every coin move is idempotent on its key, so a redelivery credits
// exactly once (and recovers an event that failed mid-processing last time).
func (s *Service) HandleWebhook(ctx context.Context, payload []byte, sigHeader string) error {
	if err := VerifySignature(payload, sigHeader, s.cfg.WebhookSecret, s.clock.Now(), s.cfg.WebhookTolerance); err != nil {
		s.m.webhookFailures.Inc()
		return err
	}
	ev, err := parseEvent(payload)
	if err != nil || ev.ID == "" {
		s.m.webhookFailures.Inc()
		return httpx.ErrBadRequest
	}
	// Persist-first: the event is logged by id before we act on it.
	alreadySeen, err := s.repo.InsertEvent(ctx, ev.ID, ev.Type, payload)
	if err != nil {
		return err
	}
	if alreadySeen {
		s.m.duplicate.Inc()
	}
	if err := s.process(ctx, ev); err != nil {
		return err
	}
	return s.repo.MarkProcessed(ctx, ev.ID)
}

// process applies the coin effect of an event. Crediting is keyed by the Checkout
// SESSION id so the webhook and reconciliation converge on one idempotency key.
func (s *Service) process(ctx context.Context, ev Event) error {
	switch ev.Type {
	case EventCheckoutCompleted:
		if ev.AgentPublicID == "" || ev.Coins <= 0 {
			s.log.Warn("checkout completed without usable metadata; skipping", "event", ev.ID, "session", ev.ObjectID)
			return nil
		}
		if err := s.coiner.Topup(ctx, ev.AgentPublicID, ev.Coins, "topup:"+ev.ObjectID); err != nil {
			return err
		}
		s.m.topups.Inc()
		s.m.topupCoins.Add(float64(ev.Coins))
		return nil

	case EventChargeRefunded, EventDisputeCreated:
		if ev.AgentPublicID == "" || ev.Coins <= 0 {
			return nil
		}
		// Reverse recovers what the agent still holds and books any shortfall as
		// chargeback debt (wallet never goes negative); it self-handles the
		// can't-fully-claw-back case, so there's no special error to branch on.
		return s.coiner.Reverse(ctx, ev.AgentPublicID, ev.Coins, "reversal:"+ev.ID)

	case EventPaymentSucceeded:
		// Crediting happens on checkout.session.completed (same purchase, one key).
		return nil
	default:
		return nil
	}
}

// Reconcile is the safety net for webhook loss. It (1) replays any locally stored
// but unprocessed events, then (2) asks Stripe for recently-completed sessions and
// credits any whose top-up never landed — both idempotently. Returns the number of
// events/sessions reconciled.
func (s *Service) Reconcile(ctx context.Context) (int, error) {
	reconciled := 0

	stored, err := s.repo.UnprocessedEvents(ctx, 500)
	if err != nil {
		return reconciled, err
	}
	for _, se := range stored {
		ev, err := parseEvent(se.Payload)
		if err != nil {
			s.log.Error("reconcile: unparseable stored event", "event", se.ID, "error", err)
			continue
		}
		if err := s.process(ctx, ev); err != nil {
			s.log.Error("reconcile: reprocess failed", "event", se.ID, "error", err)
			continue
		}
		if err := s.repo.MarkProcessed(ctx, ev.ID); err != nil {
			s.log.Error("reconcile: mark processed failed", "event", se.ID, "error", err)
			continue
		}
		reconciled++
	}

	recs, err := s.gw.ListRecentCheckouts(ctx, s.clock.Now().Add(-s.cfg.ReconcileLookback))
	if err != nil {
		return reconciled, err
	}
	for _, rec := range recs {
		if rec.AgentPublicID == "" || rec.Coins <= 0 {
			continue
		}
		// Idempotent: a no-op if the webhook already credited this session.
		if err := s.coiner.Topup(ctx, rec.AgentPublicID, rec.Coins, "topup:"+rec.SessionID); err != nil {
			s.m.reconcileUnmatched.Inc()
			s.log.Error("reconcile: credit failed for Stripe session", "session", rec.SessionID, "error", err)
			continue
		}
		reconciled++
	}
	return reconciled, nil
}

// ── metrics ──────────────────────────────────────────────────────────────────

type metrics struct {
	topups             prometheus.Counter
	topupCoins         prometheus.Counter
	webhookFailures    prometheus.Counter
	duplicate          prometheus.Counter
	reconcileUnmatched prometheus.Counter
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		topups:             counter("topups_total", "Successful coin top-ups credited."),
		topupCoins:         counter("topup_coins_total", "Total coins credited via top-ups."),
		webhookFailures:    counter("stripe_webhook_failures_total", "Inbound webhooks rejected (signature/parse)."),
		duplicate:          counter("stripe_webhook_duplicates_total", "Redelivered webhook events recognised by id."),
		reconcileUnmatched: counter("reconcile_unmatched_total", "Stripe sessions reconciliation could not credit (should be 0)."),
	}
	reg.MustRegister(m.topups, m.topupCoins, m.webhookFailures, m.duplicate, m.reconcileUnmatched)
	return m
}

func counter(name, help string) prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
}
