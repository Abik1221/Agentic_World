package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// StripeBillingGateway calls Stripe REST for subscriptions and portal.
type StripeBillingGateway struct {
	secretKey string
	base      string
	client    *http.Client
}

func NewStripeBillingGateway(secretKey string) *StripeBillingGateway {
	return &StripeBillingGateway{
		secretKey: secretKey,
		base:      "https://api.stripe.com",
		client:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (g *StripeBillingGateway) EnsureCustomer(ctx context.Context, existingID, userPublicID string) (string, error) {
	if existingID != "" {
		return existingID, nil
	}
	form := url.Values{}
	form.Set("metadata[user]", userPublicID)
	var out struct {
		ID string `json:"id"`
	}
	if err := g.do(ctx, http.MethodPost, "/v1/customers", form, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (g *StripeBillingGateway) CreateSubscriptionCheckout(ctx context.Context, customerID, userPublicID, priceID, successURL, cancelURL string) (string, string, error) {
	form := url.Values{}
	form.Set("mode", "subscription")
	form.Set("customer", customerID)
	form.Set("success_url", successURL+"?session_id={CHECKOUT_SESSION_ID}")
	form.Set("cancel_url", cancelURL)
	form.Set("line_items[0][price]", priceID)
	form.Set("line_items[0][quantity]", "1")
	form.Set("subscription_data[metadata][user]", userPublicID)
	form.Set("metadata[user]", userPublicID)

	var out struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := g.do(ctx, http.MethodPost, "/v1/checkout/sessions", form, &out); err != nil {
		return "", "", err
	}
	return out.URL, out.ID, nil
}

func (g *StripeBillingGateway) CreatePortalSession(ctx context.Context, customerID, returnURL string) (string, error) {
	form := url.Values{}
	form.Set("customer", customerID)
	form.Set("return_url", returnURL)
	var out struct {
		URL string `json:"url"`
	}
	if err := g.do(ctx, http.MethodPost, "/v1/billing_portal/sessions", form, &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

func (g *StripeBillingGateway) do(ctx context.Context, method, path string, form url.Values, out any) error {
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
