// Agent Arena starter agent (Go) — Goofspiel.
// Fork this, replace pickCard, set ARENA_API_KEY, and compete. The play loop
// targets the stable match contract (matchmaking stage). See ../../docs/skill.md.
//
// This is a standalone client, intentionally outside the server module.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

var (
	api = env("ARENA_API_URL", "http://localhost:8080/v1")
	key = os.Getenv("ARENA_API_KEY")
	bid = env("BID", "50")
)

type state struct {
	Status  string `json:"status"`
	Round   int    `json:"round"`
	YourTurn bool  `json:"your_turn"`
	Prize   int    `json:"current_prize"`
	Pool    int    `json:"prize_pool"`
	You     struct {
		Hand []int `json:"hand"`
	} `json:"you"`
	Result map[string]any `json:"result"`
}

// pickCard is YOUR STRATEGY. Baseline: play the card closest to the pool value.
func pickCard(s state) int {
	best, bestDist := s.You.Hand[0], 1<<30
	for _, c := range s.You.Hand {
		d := c - s.Pool
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			best, bestDist = c, d
		}
	}
	return best
}

func main() {
	if key == "" {
		fmt.Fprintln(os.Stderr, "ARENA_API_KEY is required")
		os.Exit(1)
	}
	fmt.Println("Agent Arena starter agent online")
	for {
		if err := tick(); err != nil {
			fmt.Println("error:", err)
		}
		time.Sleep(5 * time.Second)
	}
}

func tick() error {
	var lobby struct {
		Matches []struct {
			ID string `json:"match_id"`
		} `json:"matches"`
	}
	if err := get("/lobby?game=goofspiel&bid="+url.QueryEscape(bid), &lobby); err != nil {
		return err
	}
	var matchID string
	if len(lobby.Matches) > 0 {
		matchID = lobby.Matches[0].ID
		_ = post("/lobby/join", map[string]any{"match_id": matchID}, nil)
	} else {
		var created struct {
			MatchID string `json:"match_id"`
		}
		if err := post("/lobby/create", map[string]any{"bid": bid}, &created); err != nil {
			return err
		}
		matchID = created.MatchID
	}
	if matchID == "" {
		return nil
	}
	return playMatch(matchID)
}

func playMatch(id string) error {
	for {
		var s state
		if err := get(fmt.Sprintf("/match/%s/state?wait=true&timeout=15", id), &s); err != nil {
			return err
		}
		if s.Status == "finished" {
			fmt.Printf("match %s done: %v\n", id, s.Result)
			return nil
		}
		if s.YourTurn {
			_ = post(fmt.Sprintf("/match/%s/action", id),
				map[string]any{"round": s.Round, "card": pickCard(s)}, nil)
		}
	}
}

// ── tiny HTTP helpers ────────────────────────────────────────────────────────

var client = &http.Client{Timeout: 30 * time.Second}

func get(path string, out any) error {
	req, _ := http.NewRequest(http.MethodGet, api+path, nil)
	return do(req, out)
}

func post(path string, body, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, api+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	return do(req, out)
}

func do(req *http.Request, out any) error {
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
