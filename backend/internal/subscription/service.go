package subscription

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
)

// Config tunes Arena Pass economics and Stripe price wiring.
type Config struct {
	Plan            Plan
	StripePriceID   string
	SuccessURL      string
	CancelURL       string
	PortalReturnURL string
	DevMode         bool
}

// Service runs subscription checkout, portal, and webhook-driven grants.
type Service struct {
	gw     BillingGateway
	coiner Coiner
	repo   Repo
	cfg    Config
	log    *slog.Logger
}

func New(gw BillingGateway, coiner Coiner, repo Repo, cfg Config, log *slog.Logger) *Service {
	if cfg.Plan.MonthlyCoins <= 0 {
		cfg.Plan.MonthlyCoins = 1000
	}
	if cfg.Plan.PriceCents <= 0 {
		cfg.Plan.PriceCents = 999
	}
	if cfg.Plan.Currency == "" {
		cfg.Plan.Currency = "usd"
	}
	return &Service{gw: gw, coiner: coiner, repo: repo, cfg: cfg, log: log}
}

func (s *Service) Plans() []Plan { return []Plan{s.cfg.Plan} }

func (s *Service) Status(ctx context.Context, userPublicID string) (Status, error) {
	rec, err := s.repo.GetByUser(ctx, userPublicID)
	if err != nil {
		return Status{}, err
	}
	st := Status{
		Plan:               rec.PlanKey,
		Status:             rec.Status,
		Active:             rec.Status == StatusActive || rec.Status == StatusPastDue,
		MonthlyCoins:       rec.MonthlyCoins,
		CurrentPeriodEnd:   rec.CurrentPeriodEnd,
		ManageBillingAvail: rec.StripeCustomerID != "",
	}
	if st.Plan == "" {
		st.Plan = PlanArenaPass
		st.Status = StatusInactive
	}
	return st, nil
}

func (s *Service) Checkout(ctx context.Context, userPublicID string) (string, string, error) {
	if s.cfg.StripePriceID == "" && !s.cfg.DevMode {
		return "", "", httpx.NewError(503, "subscription_unavailable", "Arena Pass is not configured.")
	}
	customer, err := s.repo.StripeCustomer(ctx, userPublicID)
	if err != nil {
		return "", "", err
	}
	customer, err = s.gw.EnsureCustomer(ctx, customer, userPublicID)
	if err != nil {
		return "", "", err
	}
	if customer != "" {
		_ = s.repo.SetStripeCustomer(ctx, userPublicID, customer)
	}
	if s.cfg.DevMode && s.cfg.StripePriceID == "" {
		return s.cfg.SuccessURL + "?session_id=dev_sub", "dev_sub", nil
	}
	return s.gw.CreateSubscriptionCheckout(ctx, customer, userPublicID, s.cfg.StripePriceID, s.cfg.SuccessURL, s.cfg.CancelURL)
}

func (s *Service) Portal(ctx context.Context, userPublicID string) (string, error) {
	customer, err := s.repo.StripeCustomer(ctx, userPublicID)
	if err != nil || customer == "" {
		return "", httpx.NewError(400, "no_billing_account", "Subscribe first to manage billing.")
	}
	return s.gw.CreatePortalSession(ctx, customer, s.cfg.PortalReturnURL)
}

// ActivateDev activates Arena Pass offline (local dev only).
func (s *Service) ActivateDev(ctx context.Context, userPublicID string) error {
	if !s.cfg.DevMode {
		return httpx.ErrForbidden
	}
	end := time.Now().Add(30 * 24 * time.Hour)
	return s.repo.Upsert(ctx, Record{
		UserPublicID: userPublicID, PlanKey: PlanArenaPass, Status: StatusActive,
		CurrentPeriodEnd: &end, MonthlyCoins: s.cfg.Plan.MonthlyCoins,
	})
}

// HandleStripeEvent processes subscription-related webhook events (called from payments).
func (s *Service) HandleStripeEvent(ctx context.Context, eventType string, payload []byte) error {
	switch eventType {
	case "customer.subscription.created", "customer.subscription.updated":
		return s.syncSubscription(ctx, payload)
	case "customer.subscription.deleted":
		return s.cancelSubscription(ctx, payload)
	case "invoice.paid":
		return s.grantInvoice(ctx, payload)
	default:
		return nil
	}
}

func (s *Service) syncSubscription(ctx context.Context, payload []byte) error {
	var env struct {
		Data struct {
			Object struct {
				ID               string            `json:"id"`
				Customer         string            `json:"customer"`
				Status           string            `json:"status"`
				CurrentPeriodEnd int64             `json:"current_period_end"`
				Metadata         map[string]string `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return err
	}
	obj := env.Data.Object
	user := obj.Metadata["user"]
	if user == "" {
		return nil
	}
	status := mapStripeStatus(obj.Status)
	var end *time.Time
	if obj.CurrentPeriodEnd > 0 {
		t := time.Unix(obj.CurrentPeriodEnd, 0).UTC()
		end = &t
	}
	return s.repo.Upsert(ctx, Record{
		UserPublicID: user, StripeCustomerID: obj.Customer,
		StripeSubscriptionID: obj.ID, PlanKey: PlanArenaPass,
		Status: status, CurrentPeriodEnd: end, MonthlyCoins: s.cfg.Plan.MonthlyCoins,
	})
}

func (s *Service) cancelSubscription(ctx context.Context, payload []byte) error {
	var env struct {
		Data struct {
			Object struct {
				Metadata map[string]string `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return err
	}
	user := env.Data.Object.Metadata["user"]
	if user == "" {
		return nil
	}
	return s.repo.Upsert(ctx, Record{
		UserPublicID: user, PlanKey: PlanArenaPass, Status: StatusCanceled,
		MonthlyCoins: s.cfg.Plan.MonthlyCoins,
	})
}

func (s *Service) grantInvoice(ctx context.Context, payload []byte) error {
	var env struct {
		Data struct {
			Object struct {
				ID       string            `json:"id"`
				Customer string            `json:"customer"`
				Status   string            `json:"status"`
				Metadata map[string]string `json:"metadata"`
				Lines    struct {
					Data []struct {
						Price struct {
							Recurring *struct{} `json:"recurring"`
						} `json:"price"`
					} `json:"data"`
				} `json:"lines"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return err
	}
	obj := env.Data.Object
	if obj.Status != "paid" && obj.Status != "" {
		return nil
	}
	user := obj.Metadata["user"]
	if user == "" {
		return nil
	}
	seen, err := s.repo.GrantExists(ctx, obj.ID)
	if err != nil || seen {
		return err
	}
	coins := s.cfg.Plan.MonthlyCoins
	if err := s.coiner.Topup(ctx, user, coins, "sub:"+obj.ID); err != nil {
		return err
	}
	return s.repo.RecordGrant(ctx, user, obj.ID, coins)
}

func mapStripeStatus(stripeStatus string) string {
	switch stripeStatus {
	case "active", "trialing":
		return StatusActive
	case "past_due", "unpaid":
		return StatusPastDue
	case "canceled", "incomplete_expired":
		return StatusCanceled
	default:
		return StatusInactive
	}
}

// ParseDevConfirm activates dev subscription from billing return.
func (s *Service) ConfirmDev(ctx context.Context, userPublicID, sessionID string) error {
	if !s.cfg.DevMode || sessionID != "dev_sub" {
		return httpx.ErrBadRequest
	}
	if err := s.ActivateDev(ctx, userPublicID); err != nil {
		return err
	}
	// Grant first month immediately in dev.
	return s.coiner.Topup(ctx, userPublicID, s.cfg.Plan.MonthlyCoins, "sub:dev:"+strconv.FormatInt(time.Now().Unix(), 10))
}
