package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// api.go — a thin client for the platform's public REST API. The lab deliberately talks
// to the SAME endpoints a real developer's tooling uses (signup, manifest, verify, queue,
// say) rather than reaching into the database, so what it exercises is the real product
// surface.

type api struct {
	base string
	http *http.Client
}

func newAPI(base string) *api {
	return &api{base: base, http: &http.Client{Timeout: 60 * time.Second}}
}

// do performs a request and decodes a JSON response into out (which may be nil).
// Returns the status code, and an error only for transport/decode problems — a non-2xx
// is reported through the code plus a body excerpt so callers can decide.
func (a *api) do(method, path, bearer string, body, out any) (int, string, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, "", err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.base+path, rdr)
	if err != nil {
		return 0, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := a.http.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if out != nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, out)
	}
	excerpt := string(raw)
	if len(excerpt) > 300 {
		excerpt = excerpt[:300] + "…"
	}
	return res.StatusCode, excerpt, nil
}

func (a *api) mustDo(what, method, path, bearer string, body, out any, wantCodes ...int) error {
	code, excerpt, err := a.do(method, path, bearer, body, out)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	for _, w := range wantCodes {
		if code == w {
			return nil
		}
	}
	return fmt.Errorf("%s: got HTTP %d, want %v — %s", what, code, wantCodes, excerpt)
}

// say posts one line of public table talk as the agent. Uses the AGENT credential,
// because table talk is the agent speaking, not its owner.
func (a *api) say(agentKey, matchID, text, kind string) error {
	return a.mustDo("say", http.MethodPost, "/v1/match/"+matchID+"/say", agentKey,
		map[string]any{"text": text, "kind": kind}, nil,
		http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent)
}

// createAgentKey mints an AGENT-scope API key (the credential an agent uses to act and
// to talk). Created with the owner's dashboard token, exactly as the dashboard does it.
func (a *api) createAgentKey(dashToken, agentID string) (string, error) {
	var out struct {
		APIKey string `json:"api_key"`
	}
	if err := a.mustDo("create agent key", http.MethodPost, "/v1/agent/keys", dashToken,
		map[string]any{"agent_id": agentID, "label": "gamelab"}, &out,
		http.StatusCreated, http.StatusOK); err != nil {
		return "", err
	}
	if out.APIKey == "" {
		return "", fmt.Errorf("create agent key: empty api_key in response")
	}
	return out.APIKey, nil
}

// startSandboxPushPlay opens a free push-play table and asks the platform to drive this
// agent's seat from its endpoint. No coins move, so it needs no funding — it is the
// fastest way to watch real decisions, real latency, and the live chat feed.
// startSandboxPushPlay opens a free practice table for `game` and drives this agent's seat.
//
// EACH GAME HAS ITS OWN PUSH-PLAY ROUTE, and this used to call only the goofspiel one.
// /v1/sandbox/pushplay is Goofspiel's; Mafia and Monopoly have /v1/mafia/pushplay and
// /v1/monopoly/pushplay, which seat house bots around the developer and drive the real phase
// machine. Ignoring them meant `-game monopoly` onboarded four monopoly personas, logged
// "game=monopoly", and then started GOOFSPIEL matches — so every practice-mode verification of
// those two games was verifying goofspiel.
func (a *api) startSandboxPushPlay(agentKey, game, difficulty string) (string, error) {
	var out struct {
		MatchID string `json:"match_id"`
		ID      string `json:"id"`
	}
	path, body := "/v1/sandbox/pushplay", map[string]any{"difficulty": difficulty}
	switch strings.ToLower(game) {
	case "mafia":
		// The per-game routes take no difficulty: the roster is house bots by construction.
		path, body = "/v1/mafia/pushplay", map[string]any{}
	case "monopoly":
		path, body = "/v1/monopoly/pushplay", map[string]any{}
	}
	if err := a.mustDo("sandbox pushplay", http.MethodPost, path, agentKey, body, &out,
		http.StatusCreated, http.StatusOK, http.StatusAccepted); err != nil {
		return "", err
	}
	return firstNonEmpty(out.MatchID, out.ID), nil
}

// health waits for the platform to accept traffic.
func (a *api) waitHealthy(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if code, _, err := a.do(http.MethodGet, "/healthz", "", nil, nil); err == nil && code == http.StatusOK {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("platform did not become healthy within %s", timeout)
}

// ── Staked tables ────────────────────────────────────────────────────────────
//
// Push-play is free by construction, so the lab could exercise decisions, latency and
// chat but never the MONEY path — no stake, no escrow, no settlement, and therefore no
// way to see whether an absent agent actually forfeits its coins to the winner. These
// three calls close that gap using the same endpoints a real developer's agent uses.

// fundAgent credits an agent's wallet through the dev checkout confirmation.
//
// This is the offline DevGateway path the platform already exposes for local work: no
// real charge, coins credited immediately. It is mounted only when DevMode is on (no
// Stripe key, non-prod) and re-checks DevMode inside the handler, so it cannot become a
// minting endpoint in production. Requires the USER token, not the agent key — funding a
// wallet is an owner action, and the handler verifies the caller owns the agent.
func (a *api) fundAgent(dashToken, agentPublicID string, coins int64) error {
	return a.mustDo("fund agent", http.MethodPost, "/v1/admin/dev/confirm-checkout", dashToken,
		map[string]any{
			// The DevGateway does not look this up; it only has to be unique so repeated
			// funding calls are not collapsed as one idempotent top-up.
			"session_id": fmt.Sprintf("lab_%s_%d", agentPublicID, coins),
			"agent":      agentPublicID,
			"coins":      coins,
		}, nil, http.StatusOK, http.StatusCreated)
}

// allocateToAgent moves coins from the OWNER's treasury into the agent's playing wallet.
//
// The second half of funding, and easy to miss: dev checkout credits the owner's TREASURY
// (Topup keys on the user id, exactly like the real checkout.session.completed webhook),
// not the agent. An agent whose owner is rich but whose own wallet is empty still cannot
// stake — the platform correctly answers "Balance 0 is below the required 550". Two
// distinct wallets, two distinct steps, and both are the real developer flow.
//
// Owner-scoped: funding an agent is an owner action, so this takes the dashboard token
// rather than the agent key.
func (a *api) allocateToAgent(dashToken, agentPublicID string, coins int64) error {
	return a.mustDo("allocate to agent", http.MethodPost, "/v1/wallet/allocate", dashToken,
		map[string]any{
			"agent":  agentPublicID,
			"amount": coins,
			// Idempotent: a retried allocate must not move the treasury twice.
			"idempotency_key": fmt.Sprintf("lab_alloc_%s_%d", agentPublicID, coins),
		}, nil, http.StatusOK, http.StatusCreated)
}

// walletBalance reads an agent's current coin balance, so the harness can prove what the
// money path did rather than assume it.
func (a *api) walletBalance(agentKey string) (int64, error) {
	var out struct {
		Balance int64 `json:"balance"`
		Coins   int64 `json:"coins"`
	}
	if err := a.mustDo("wallet", http.MethodGet, "/v1/wallet", agentKey, nil, &out,
		http.StatusOK); err != nil {
		return 0, err
	}
	if out.Balance != 0 {
		return out.Balance, nil
	}
	return out.Coins, nil
}

// createStakedTable opens a staked heads-up table and returns its match id. The stake is
// escrowed from the creator immediately, which is why fundAgent must run first.
// tier is required rather than a raw bid: the platform enforces fixed stake tiers per
// game so a table cannot be opened at an arbitrary amount, and the harness must go through
// the same gate. Coins per tier come from game_stakes (goofspiel low = 500).
func (a *api) createStakedTable(agentKey, tier string) (string, error) {
	var out struct {
		MatchID string `json:"match_id"`
		ID      string `json:"id"`
	}
	if err := a.mustDo("create staked table", http.MethodPost, "/v1/lobby/create", agentKey,
		map[string]any{"tier": tier}, &out, http.StatusCreated, http.StatusOK); err != nil {
		return "", err
	}
	return firstNonEmpty(out.MatchID, out.ID), nil
}

// joinStakedTable seats the second agent, escrowing its stake and starting the match.
func (a *api) joinStakedTable(agentKey, matchID string) error {
	return a.mustDo("join staked table", http.MethodPost, "/v1/lobby/join", agentKey,
		map[string]any{"match_id": matchID}, nil, http.StatusOK, http.StatusCreated)
}

// ── ranked queue, for the churn test ──────────────────────────────────────────

// enqueueRanked puts an agent into the ranked queue for `game` at a tier.
//
// THE QUEUE DEPENDS ON THE GAME, and this used to ignore it: it always posted to /v1/queue,
// which is the TWO-PLAYER goofspiel queue. So `-game mafia -tier low` silently produced a
// goofspiel match — the harness accepted the flag, logged "game=mafia", sized seats for mafia,
// and then verified something else entirely.
//
// That is worse than an unsupported flag. Two Mafia "verifications" in this session were
// actually goofspiel matches, and the only reason it surfaced was reading the round/prize/hand
// fields in the output rather than trusting the header. A verification tool that silently
// tests the wrong thing is worse than one that refuses.
//
// N-player games (mafia, monopoly) use /v1/group-queue and MUST send the game; goofspiel uses
// /v1/queue, which infers it. See GROUP_GAMES in the backend.
func (a *api) enqueueRanked(agentKey, game, tier string) (int, string, error) {
	if isGroupGame(game) {
		return a.do(http.MethodPost, "/v1/group-queue", agentKey,
			map[string]any{"game": game, "tier": tier}, nil)
	}
	return a.do(http.MethodPost, "/v1/queue", agentKey, map[string]any{"tier": tier}, nil)
}

// isGroupGame reports whether `game` matchmakes through the N-player group queue.
func isGroupGame(game string) bool {
	switch strings.ToLower(game) {
	case "mafia", "monopoly":
		return true
	}
	return false
}

// queueStatus reports whether an agent is currently queued, and its state.
//
// Returns ("", nil) when the agent has no entry at all — which is the state a non-autoplay
// agent MUST reach after its match, and the single most important observation in the churn
// test. A silent re-queue and a deliberate one look identical from the outside otherwise.
func (a *api) queueStatus(agentKey string) (status, matchID string, err error) {
	var out struct {
		Status  string `json:"status"`
		MatchID string `json:"match_id"`
	}
	code, body, err := a.do(http.MethodGet, "/v1/queue", agentKey, nil, &out)
	if err != nil {
		return "", "", err
	}
	if code == http.StatusNotFound {
		return "", "", nil // no entry: the agent is not in the queue
	}
	if code != http.StatusOK {
		return "", "", fmt.Errorf("queue status: HTTP %d — %s", code, body)
	}
	return out.Status, out.MatchID, nil
}

// leaveQueue removes an agent's entry (the explicit "I do not want another match").
func (a *api) leaveQueue(agentKey string) error {
	code, body, err := a.do(http.MethodDelete, "/v1/queue", agentKey, nil, nil)
	if err != nil {
		return err
	}
	if code != http.StatusOK && code != http.StatusNoContent && code != http.StatusNotFound {
		return fmt.Errorf("leave queue: HTTP %d — %s", code, body)
	}
	return nil
}

// agentBalance reads an agent's coin balance, so the test can watch a seat run itself broke.
func (a *api) agentBalance(agentKey string) (int64, error) { return a.walletBalance(agentKey) }

// setAutoplay turns autoplay on for an agent — the "keep playing after this match" setting.
//
// The churn test's whole autoplay half depends on this. Without it the harness enqueued once by
// hand and then asserted only that a match happened, which passes on the manual entry alone and
// proves nothing about re-entry. An agent that plays exactly one match looks identical to one
// that re-queues correctly, unless you require MORE than one distinct match.
func (a *api) setAutoplay(agentKey string, enabled bool, mode string, bid int64, games []string) (int, string, error) {
	return a.do(http.MethodPut, "/v1/agent/autoplay", agentKey, map[string]any{
		"enabled": enabled, "mode": mode, "bid": bid, "games": games,
	}, nil)
}
