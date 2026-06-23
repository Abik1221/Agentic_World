package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Captcha verifies a human-proof token once, during claim verification, to blunt
// automated multi-account farming.
type Captcha interface {
	Verify(ctx context.Context, token, remoteIP string) error
}

// DevCaptcha accepts any request (local/offline). NEVER used in prod.
type DevCaptcha struct{}

func (DevCaptcha) Verify(context.Context, string, string) error { return nil }

// HCaptcha verifies tokens against hCaptcha's siteverify endpoint.
type HCaptcha struct {
	Secret string
	Client *http.Client
}

// NewHCaptcha builds a production captcha verifier with a sane HTTP timeout.
func NewHCaptcha(secret string) *HCaptcha {
	return &HCaptcha{Secret: secret, Client: &http.Client{Timeout: 5 * time.Second}}
}

func (h *HCaptcha) Verify(ctx context.Context, token, remoteIP string) error {
	if token == "" {
		return ErrCaptchaFailed
	}
	form := url.Values{"secret": {h.Secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.hcaptcha.com/siteverify", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := h.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var out struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if !out.Success {
		return ErrCaptchaFailed
	}
	return nil
}
