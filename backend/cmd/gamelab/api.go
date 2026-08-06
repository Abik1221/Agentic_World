package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
func (a *api) startSandboxPushPlay(agentKey, difficulty string) (string, error) {
	var out struct {
		MatchID string `json:"match_id"`
		ID      string `json:"id"`
	}
	if err := a.mustDo("sandbox pushplay", http.MethodPost, "/v1/sandbox/pushplay", agentKey,
		map[string]any{"difficulty": difficulty}, &out,
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
