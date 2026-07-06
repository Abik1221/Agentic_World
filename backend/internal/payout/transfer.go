package payout

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

	"github.com/agent-arena/arena/internal/platform"
)

// DevTransferrer simulates a payout without touching Stripe, so the full cash-out
// flow runs offline. NEVER used in prod (main selects StripeTransferrer when a key
// is configured).
type DevTransferrer struct{}

func (DevTransferrer) Transfer(_ context.Context, _ string, _ int64, _ string) (string, error) {
	return platform.NewID("tr_dev"), nil
}

// PayoutsEnabled is always true offline so the local flow runs end-to-end.
func (DevTransferrer) PayoutsEnabled(_ context.Context, _ string) (bool, error) { return true, nil }

// StripeTransferrer pays a connected account via the Stripe Transfers API. The
// idempotency key is sent as Stripe's Idempotency-Key header, so even a retried
// approval can never double-pay.
type StripeTransferrer struct {
	secretKey string
	base      string
	client    *http.Client
}

func NewStripeTransferrer(secretKey string) *StripeTransferrer {
	return &StripeTransferrer{secretKey: secretKey, base: "https://api.stripe.com", client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *StripeTransferrer) Transfer(ctx context.Context, connectAccountID string, amountCents int64, idemKey string) (string, error) {
	form := url.Values{}
	form.Set("amount", strconv.FormatInt(amountCents, 10))
	form.Set("currency", "usd")
	form.Set("destination", connectAccountID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/v1/transfers", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+t.secretKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Idempotency-Key", idemKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return "", err
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
		return "", fmt.Errorf("stripe transfer: %d %s", resp.StatusCode, e.Error.Message)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// PayoutsEnabled reads the connected account and reports whether it can receive
// payouts (KYC/capabilities complete). A missing account or API error is returned
// so Approve fails closed rather than attempting a doomed transfer.
func (t *StripeTransferrer) PayoutsEnabled(ctx context.Context, connectAccountID string) (bool, error) {
	if connectAccountID == "" {
		return false, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.base+"/v1/accounts/"+url.PathEscape(connectAccountID), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+t.secretKey)
	resp, err := t.client.Do(req)
	if err != nil {
		return false, err
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
		return false, fmt.Errorf("stripe account %s: %d %s", connectAccountID, resp.StatusCode, e.Error.Message)
	}
	var out struct {
		PayoutsEnabled bool `json:"payouts_enabled"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, err
	}
	return out.PayoutsEnabled, nil
}
