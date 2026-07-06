package agentclient

import (
	"context"
	"encoding/json"
	"net/http"
)

// ProtocolVersion is sent on every lifecycle call so agents/SDKs can branch on it.
const ProtocolVersion = "1.0"

// The push-protocol lifecycle (Onavion Beta). The platform calls the developer's
// hosted server; the developer's endpoint URL points at the /turn handler and the
// others are siblings:
//
//	GET  {endpoint}/health      liveness (Health)
//	POST {endpoint}/handshake   capability check at verify time (Handshake)
//	POST {endpoint}/initialize  match start — seat/role/config (Initialize)
//	POST {endpoint}            → the /turn handler: decide a move (Turn == Play)
//	POST {endpoint}/event       async webhook: a game event happened (Event)
//	POST {endpoint}/game-end    async webhook: final result (GameEnd)
//
// /turn is synchronous (the engine blocks on it, bounded by a timeout). /event and
// /game-end are one-way notifications, delivered async by the platform's webhook
// dispatcher off the internal event outbox — the engine never blocks on them.
// Every call is HMAC-signed (see SignRequest). Payloads carry a game-specific body.

// InitializeRequest is POSTed to {endpoint}/initialize once when a match starts.
type InitializeRequest struct {
	Protocol   string          `json:"protocol"`
	MatchID    string          `json:"match_id"`
	Game       string          `json:"game"`
	Seat       int             `json:"seat"`
	Role       string          `json:"role,omitempty"` // e.g. Mafia role; omitted when hidden/none
	Players    int             `json:"players"`
	Config     json.RawMessage `json:"config,omitempty"` // game-specific setup
	DeadlineMs int64           `json:"deadline_ms,omitempty"`
}

// InitializeResponse is the agent's (optional) acknowledgement.
type InitializeResponse struct {
	Ready       bool   `json:"ready"`
	DisplayName string `json:"display_name,omitempty"`
}

// EventNotification is POSTed to {endpoint}/event for each public game event.
type EventNotification struct {
	Protocol string          `json:"protocol"`
	MatchID  string          `json:"match_id"`
	Game     string          `json:"game"`
	Seq      int             `json:"seq"`
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

// GameEndNotification is POSTed to {endpoint}/game-end when the match finishes.
type GameEndNotification struct {
	Protocol string          `json:"protocol"`
	MatchID  string          `json:"match_id"`
	Game     string          `json:"game"`
	Result   json.RawMessage `json:"result,omitempty"` // game-specific final result + rewards
}

// Initialize notifies the agent a match is starting. Best-effort: the agent may
// also initialise lazily on the first /turn, so a non-fatal error is returned for
// logging but should not abort the match.
func (c *Client) Initialize(ctx context.Context, t Target, req InitializeRequest) (InitializeResponse, error) {
	req.Protocol = ProtocolVersion
	u, err := siblingURL(t.EndpointURL, "initialize")
	if err != nil {
		return InitializeResponse{}, err
	}
	body, _ := json.Marshal(req)
	status, raw, err := c.do(ctx, http.MethodPost, u, t.Token, body)
	if err != nil {
		return InitializeResponse{}, err
	}
	var out InitializeResponse
	if status == http.StatusOK && len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out, nil
}

// Turn asks the agent to decide. It POSTs the endpoint URL itself (the agent's
// /turn handler); request is the redacted view + legal actions, out receives the
// move. Alias of the historical Play primitive so callers read as the lifecycle.
func (c *Client) Turn(ctx context.Context, t Target, request, out any) (int, error) {
	return c.Play(ctx, t, request, out)
}

// Event delivers one public game event (one-way webhook). The error is returned so
// the async dispatcher can retry; the engine never blocks on delivery.
func (c *Client) Event(ctx context.Context, t Target, n EventNotification) error {
	n.Protocol = ProtocolVersion
	u, err := siblingURL(t.EndpointURL, "event")
	if err != nil {
		return err
	}
	body, _ := json.Marshal(n)
	_, _, err = c.do(ctx, http.MethodPost, u, t.Token, body)
	return err
}

// GameEnd delivers the final result (one-way webhook).
func (c *Client) GameEnd(ctx context.Context, t Target, n GameEndNotification) error {
	n.Protocol = ProtocolVersion
	u, err := siblingURL(t.EndpointURL, "game-end")
	if err != nil {
		return err
	}
	body, _ := json.Marshal(n)
	_, _, err = c.do(ctx, http.MethodPost, u, t.Token, body)
	return err
}
