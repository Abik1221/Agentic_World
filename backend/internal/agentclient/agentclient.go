// Package agentclient is the platform's hardened outbound HTTP client for
// calling developer-hosted agent endpoints (GET /health, POST /handshake). It is
// the ONLY place the platform makes requests to attacker-controlled URLs, so it
// is defensive by construction:
//
//   - SSRF guard: a net.Dialer.Control hook rejects connections to loopback,
//     private, link-local (incl. the 169.254.169.254 cloud-metadata address),
//     CGNAT, multicast, and unspecified IPs. The check runs at CONNECT time on the
//     concrete resolved IP, so DNS rebinding (resolve public, connect private)
//     cannot bypass it.
//   - No redirects: a 3xx cannot be used to bounce the request to an internal host.
//   - Bounded body: responses are read through an io.LimitReader so a hostile
//     endpoint cannot exhaust memory.
//   - Bounded time + retries: each attempt has a clamped deadline; transient
//     failures retry with a small backoff.
//
// AllowPrivate disables the IP guard for local dev/e2e only (a starter agent on
// localhost); it must never be set in production.
package agentclient

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"syscall"
	"time"
)

// SignatureVersion is the scheme id carried in the X-Arena-Signature header. The
// SDKs branch on it so the scheme can evolve without breaking older agents.
const SignatureVersion = "v1"

// SignRequest computes the canonical HMAC-SHA256 signature the platform sends on
// every call to a developer endpoint, and that the SDKs verify. The canonical
// string binds the timestamp, per-request nonce, method, path, and a hash of the
// body, so a captured request cannot be replayed to a different route/time. It is
// exported so the reference SDKs (and tests) share EXACTLY this construction.
//
//	signingString = timestamp \n nonce \n METHOD \n path \n hex(sha256(body))
//	signature     = hex(hmacSHA256(secret, signingString))
//
// The agent verifies by recomputing with its shared secret (constant-time compare),
// rejecting stale timestamps (clock-skew window) and already-seen nonces (replay).
func SignRequest(secret, timestamp, nonce, method, path string, body []byte) string {
	bodyHash := sha256.Sum256(body)
	var b strings.Builder
	b.WriteString(timestamp)
	b.WriteByte('\n')
	b.WriteString(nonce)
	b.WriteByte('\n')
	b.WriteString(strings.ToUpper(method))
	b.WriteByte('\n')
	b.WriteString(path)
	b.WriteByte('\n')
	b.WriteString(hex.EncodeToString(bodyHash[:]))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(b.String()))
	return hex.EncodeToString(mac.Sum(nil))
}

// perAttemptTimeout decides how long one attempt may take.
//
// DEADLINE PROPAGATION, the standard RPC contract: the CALLER owns the deadline and the
// transport honours it. cfg.Timeout is a fallback for callers that set none — not a
// ceiling that silently overrides one they did set.
//
// Getting this backwards is what made the adaptive-window work inert. Play clients are
// built once at startup with a fixed Timeout, and this method used to apply it
// unconditionally, so a caller computing a 2m22s window for a slow local model still had
// its turn cut at the configured constant. The window was correct, adaptive and tested —
// and could never reach the code that decides when to give up.
//
// MaxTimeout remains an absolute ceiling. A caller cannot pin a goroutine and a socket
// indefinitely by handing in an enormous deadline, however it was computed.
func (c *Client) perAttemptTimeout(ctx context.Context) time.Duration {
	dl, ok := ctx.Deadline()
	if !ok {
		return c.cfg.Timeout // no caller deadline: the configured fallback, as before
	}
	remaining := time.Until(dl)
	if remaining <= 0 {
		// Already past it. Return a positive sliver so WithTimeout produces a context that
		// fails cleanly on its own terms rather than one that was never valid.
		return time.Millisecond
	}
	if remaining > c.cfg.MaxTimeout {
		return c.cfg.MaxTimeout
	}
	return remaining
}

// Config tunes the client. Zero values fall back to sensible defaults in New.
type Config struct {
	// Timeout is the per-attempt deadline used when the CALLER supplies none. A caller
	// that sets a deadline on its context overrides this in both directions — see
	// perAttemptTimeout.
	Timeout      time.Duration
	MaxTimeout   time.Duration // hard ceiling on Timeout
	Retries      int           // additional attempts after the first, on transient errors
	Backoff      time.Duration // base delay between attempts
	MaxBodyBytes int64         // cap on a response body
	AllowPrivate bool          // DEV ONLY: permit private/loopback endpoint IPs
}

func (c Config) withDefaults() Config {
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Second
	}
	if c.MaxTimeout <= 0 {
		c.MaxTimeout = 15 * time.Second
	}
	if c.Timeout > c.MaxTimeout {
		c.Timeout = c.MaxTimeout
	}
	if c.Retries < 0 {
		c.Retries = 0
	}
	if c.Backoff <= 0 {
		c.Backoff = 50 * time.Millisecond
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = 64 << 10 // 64 KiB
	}
	return c
}

// Client calls agent endpoints. Safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client
}

// ErrBlockedAddress is returned (wrapped) when the SSRF guard refuses an IP.
var ErrBlockedAddress = errors.New("agentclient: connection to a disallowed address was blocked")

// New builds a hardened client from cfg.
func New(cfg Config) *Client {
	cfg = cfg.withDefaults()

	dialer := &net.Dialer{
		Timeout:   cfg.MaxTimeout,
		KeepAlive: 30 * time.Second,
		// Control runs after DNS resolution with the concrete ip:port about to be
		// dialed — the correct place to stop DNS-rebinding SSRF.
		Control: func(_, address string, _ syscall.RawConn) error {
			if cfg.AllowPrivate {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || blockedIP(ip) {
				return fmt.Errorf("%w: %s", ErrBlockedAddress, host)
			}
			return nil
		},
	}

	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   cfg.MaxTimeout,
		ExpectContinueTimeout: time.Second,
		DisableKeepAlives:     false,
	}

	return &Client{
		cfg: cfg,
		http: &http.Client{
			Transport: transport,
			Timeout:   cfg.MaxTimeout,
			// Never follow redirects: a 3xx must not bounce us to an internal host.
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("agentclient: redirects are not allowed")
			},
		},
	}
}

// Target is the resolved endpoint the platform will call. Token is the decrypted
// bearer credential (empty if the developer set none). AgentID is the owner’s
// agent public id — sent on handshake so the endpoint can prove it is that agent.
type Target struct {
	EndpointURL string
	Token       string
	AgentID     string
}

// HealthBody is the expected GET /health payload.
type HealthBody struct {
	Status  string `json:"status"`
	Agent   string `json:"agent"`
	Version string `json:"version"`
}

// HealthResult is the outcome of a health probe.
type HealthResult struct {
	OK        bool
	Status    int
	LatencyMs int
	Body      HealthBody
	Err       string
}

// HandshakeBody is the expected POST /handshake payload.
type HandshakeBody struct {
	Accepted       bool     `json:"accepted"`
	SDKVersion     string   `json:"sdkVersion"`
	SupportedGames []string `json:"supportedGames"`
	AgentID        string   `json:"agent_id"`
	AgentIDCamel   string   `json:"agentId"`
	Challenge      string   `json:"challenge"`
}

// HandshakeResult is the outcome of a handshake.
type HandshakeResult struct {
	OK             bool
	Status         int
	LatencyMs      int
	Accepted       bool
	SDKVersion     string
	SupportedGames []string
	AgentID        string
	Challenge      string
	Err            string
}

// Health probes {origin}/health. A healthy result is HTTP 200 with a body whose
// status is "healthy".
func (c *Client) Health(ctx context.Context, t Target) (HealthResult, error) {
	u, err := siblingURL(t.EndpointURL, "health")
	if err != nil {
		return HealthResult{}, err
	}
	start := nowFromCtx(ctx)
	status, raw, err := c.do(ctx, http.MethodGet, u, "", nil)
	res := HealthResult{Status: status, LatencyMs: elapsedMs(start)}
	if err != nil {
		res.Err = err.Error()
		return res, err
	}
	var body HealthBody
	if err := json.Unmarshal(raw, &body); err != nil {
		res.Err = "invalid health JSON"
		return res, nil
	}
	res.Body = body
	res.OK = status == http.StatusOK && strings.EqualFold(body.Status, "healthy")
	return res, nil
}

// Handshake POSTs to {origin}/handshake with the bearer token. A successful
// handshake is HTTP 200 with accepted=true. The request carries the owner's
// agent_id and a one-time challenge; if the endpoint echoes a different
// challenge, the handshake is rejected. A missing echo is tolerated (older
// SDKs); a mismatched agent_id is left for the caller to reject.
func (c *Client) Handshake(ctx context.Context, t Target) (HandshakeResult, error) {
	u, err := siblingURL(t.EndpointURL, "handshake")
	if err != nil {
		return HandshakeResult{}, err
	}
	challenge := newRequestID()
	payload, _ := json.Marshal(map[string]any{
		"platform":  "agent-arena",
		"protocol":  "1.0",
		"agent_id":  t.AgentID,
		"challenge": challenge,
	})
	start := nowFromCtx(ctx)
	status, raw, err := c.do(ctx, http.MethodPost, u, t.Token, payload)
	res := HandshakeResult{Status: status, LatencyMs: elapsedMs(start)}
	if err != nil {
		res.Err = err.Error()
		return res, err
	}
	var body HandshakeBody
	if err := json.Unmarshal(raw, &body); err != nil {
		res.Err = "invalid handshake JSON"
		return res, nil
	}
	res.Accepted = body.Accepted
	res.SDKVersion = body.SDKVersion
	res.SupportedGames = body.SupportedGames
	if id := strings.TrimSpace(body.AgentID); id != "" {
		res.AgentID = id
	} else {
		res.AgentID = strings.TrimSpace(body.AgentIDCamel)
	}
	res.Challenge = strings.TrimSpace(body.Challenge)
	if res.Challenge != "" && res.Challenge != challenge {
		res.Err = "handshake challenge mismatch"
		return res, nil
	}
	res.OK = status == http.StatusOK && body.Accepted
	return res, nil
}

// Play POSTs a game state to the agent's play endpoint (endpoint.url) and
// decodes the returned action into out. This is the push-model primitive (M4):
// the platform drives the match and asks the remote agent to decide. It reuses
// the same hardened transport (SSRF guard, bearer auth, body cap, timeout).
func (c *Client) Play(ctx context.Context, t Target, request, out any) (int, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return 0, err
	}
	status, raw, err := c.do(ctx, http.MethodPost, t.EndpointURL, t.Token, body)
	if err != nil {
		return status, err
	}
	if status != http.StatusOK {
		return status, fmt.Errorf("agentclient: play returned status %d", status)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return status, fmt.Errorf("agentclient: invalid play response JSON: %w", err)
		}
	}
	return status, nil
}

// do performs one request with retries. It returns the status code and the
// (size-capped) response body. A non-2xx status is NOT an error — callers decide.
func (c *Client) do(ctx context.Context, method, rawURL, token string, body []byte) (int, []byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.cfg.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return 0, nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * c.cfg.Backoff):
			}
		}
		status, raw, err := c.attempt(ctx, method, rawURL, token, body)
		if err == nil && !retryableStatus(status) {
			return status, raw, nil
		}
		if err != nil && !retryable(err) {
			return status, raw, err // permanent (e.g. blocked address) — do not retry
		}
		lastErr = err
		if err == nil {
			lastErr = fmt.Errorf("upstream status %d", status)
		}
	}
	return 0, nil, lastErr
}

func (c *Client) attempt(ctx context.Context, method, rawURL, token string, body []byte) (int, []byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.perAttemptTimeout(ctx))
	defer cancel()

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, rawURL, rdr)
	if err != nil {
		return 0, nil, err
	}
	nonce := newRequestID()
	timestamp := time.Now().UTC().Format(time.RFC3339)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "AgentArena-Verifier/1.0")
	req.Header.Set("X-Arena-Request-Id", nonce)
	req.Header.Set("X-Arena-Timestamp", timestamp)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		// Static bearer (back-compat) PLUS a per-request HMAC signature. The SDKs
		// verify the signature (constant-time), reject stale timestamps, and
		// dedupe nonces — so a leaked/replayed request can't be reused. The shared
		// secret is the agent's endpoint token.
		req.Header.Set("Authorization", "Bearer "+token)
		sig := SignRequest(token, timestamp, nonce, method, req.URL.Path, body)
		req.Header.Set("X-Arena-Signature", SignatureVersion+"="+sig)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	// Read at most MaxBodyBytes+1 so we can detect (and reject) an oversized body.
	limited := io.LimitReader(resp.Body, c.cfg.MaxBodyBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if int64(len(raw)) > c.cfg.MaxBodyBytes {
		return resp.StatusCode, nil, fmt.Errorf("agentclient: response body exceeds %d bytes", c.cfg.MaxBodyBytes)
	}
	return resp.StatusCode, raw, nil
}

// siblingURL derives {dir(endpoint)}/name, so an endpoint of
// https://a.example.com/play yields .../health and .../handshake. Endpoints under
// a path prefix (…/agents/atlas/play) resolve to sibling paths under that prefix.
func siblingURL(endpointURL, name string) (string, error) {
	u, err := url.Parse(endpointURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("agentclient: invalid endpoint url %q", endpointURL)
	}
	dir := path.Dir(u.Path)
	if dir == "." || dir == "" {
		dir = "/"
	}
	u.Path = path.Join(dir, name)
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// blockedIP reports whether ip is in a range the platform must never dial.
func blockedIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	switch {
	case ip.IsLoopback(), // 127/8, ::1
		ip.IsPrivate(),          // RFC1918 + fc00::/7
		ip.IsLinkLocalUnicast(), // 169.254/16 (incl. 169.254.169.254 metadata), fe80::/10
		ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(),
		ip.IsMulticast(),
		ip.IsUnspecified(): // 0.0.0.0, ::
		return true
	}
	// Carrier-grade NAT 100.64.0.0/10 (RFC 6598) — not covered by IsPrivate.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}

// retryable reports whether an error is worth another attempt. A blocked address
// is permanent; timeouts and transient network errors are retryable.
func retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrBlockedAddress) {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	// Deadline exceeded and net errors are transient.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || (status >= 500 && status <= 599)
}

func newRequestID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "req_" + hex.EncodeToString(b)
}

// nowFromCtx / elapsedMs measure latency without importing a clock; time.Now is
// acceptable here (this is I/O timing, not game logic).
func nowFromCtx(_ context.Context) time.Time { return time.Now() }
func elapsedMs(start time.Time) int          { return int(time.Since(start).Milliseconds()) }
