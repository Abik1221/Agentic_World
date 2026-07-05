package payments

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// StripeGateway is the production Gateway: it calls the Stripe REST API directly
// over HTTPS with form-encoded bodies (no SDK dependency). It is selected by main
// whenever STRIPE_SECRET_KEY is set. Card data never touches our servers — we only
// create hosted sessions and read back ids/urls.
type StripeGateway struct {
	secretKey string
	base      string
	client    *http.Client
}

// NewStripeGateway builds a live gateway with a sane HTTP timeout.
func NewStripeGateway(secretKey string) *StripeGateway {
	return &StripeGateway{
		secretKey: secretKey,
		base:      "https://api.stripe.com",
		client:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (g *StripeGateway) CreateCheckout(ctx context.Context, p CheckoutParams) (Checkout, error) {
	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("success_url", p.SuccessURL+"?session_id={CHECKOUT_SESSION_ID}")
	form.Set("cancel_url", p.CancelURL)
	form.Set("client_reference_id", p.UserPublicID)
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", "usd")
	form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(p.Pack.Cents, 10))
	form.Set("line_items[0][price_data][product_data][name]", p.Pack.Label)
	if p.ProcessingFeeCents > 0 {
		form.Set("line_items[1][quantity]", "1")
		form.Set("line_items[1][price_data][currency]", "usd")
		form.Set("line_items[1][price_data][unit_amount]", strconv.FormatInt(p.ProcessingFeeCents, 10))
		form.Set("line_items[1][price_data][product_data][name]", "Payment processing fee")
	}
	for _, prefix := range []string{"metadata", "payment_intent_data[metadata]"} {
		form.Set(prefix+"[user]", p.UserPublicID)
		if p.AgentPublicID != "" {
			form.Set(prefix+"[agent]", p.AgentPublicID)
		}
		form.Set(prefix+"[coins]", strconv.FormatInt(p.Pack.Coins, 10))
		form.Set(prefix+"[pack_cents]", strconv.FormatInt(p.Pack.Cents, 10))
		form.Set(prefix+"[processing_fee_cents]", strconv.FormatInt(p.ProcessingFeeCents, 10))
	}

	var out struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := g.do(ctx, http.MethodPost, "/v1/checkout/sessions", form, &out); err != nil {
		return Checkout{}, err
	}
	return Checkout{ID: out.ID, URL: out.URL}, nil
}

func (g *StripeGateway) EnsureConnectAccount(ctx context.Context, existingID, _ string) (string, error) {
	if existingID != "" {
		return existingID, nil
	}
	form := url.Values{}
	form.Set("type", "express")
	var out struct {
		ID string `json:"id"`
	}
	if err := g.do(ctx, http.MethodPost, "/v1/accounts", form, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (g *StripeGateway) CreateOnboardingLink(ctx context.Context, accountID, returnURL, refreshURL string) (string, error) {
	form := url.Values{}
	form.Set("account", accountID)
	form.Set("type", "account_onboarding")
	form.Set("return_url", returnURL)
	form.Set("refresh_url", refreshURL)
	var out struct {
		URL string `json:"url"`
	}
	if err := g.do(ctx, http.MethodPost, "/v1/account_links", form, &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

func (g *StripeGateway) ListRecentCheckouts(ctx context.Context, since time.Time) ([]CheckoutRecord, error) {
	q := url.Values{}
	q.Set("limit", "100")
	q.Set("created[gte]", strconv.FormatInt(since.Unix(), 10))
	var out struct {
		Data []struct {
			ID            string            `json:"id"`
			PaymentStatus string            `json:"payment_status"`
			Metadata      map[string]string `json:"metadata"`
		} `json:"data"`
	}
	if err := g.do(ctx, http.MethodGet, "/v1/checkout/sessions?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	var recs []CheckoutRecord
	for _, s := range out.Data {
		if s.PaymentStatus != "paid" {
			continue
		}
		coins, _ := strconv.ParseInt(s.Metadata["coins"], 10, 64)
		recs = append(recs, CheckoutRecord{
			SessionID:     s.ID,
			AgentPublicID: s.Metadata["agent"],
			UserPublicID:  s.Metadata["user"],
			Coins:         coins,
		})
	}
	return recs, nil
}

// do issues a Stripe API call. form is nil for GET. Non-2xx responses are decoded
// into the Stripe error envelope and returned as an error.
func (g *StripeGateway) do(ctx context.Context, method, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.secretKey)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return fmt.Errorf("stripe %s %s: %d %s", method, path, resp.StatusCode, e.Error.Message)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
