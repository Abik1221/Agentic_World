// Command gamelab stands up simulated developer agents against a RUNNING Pyyol server so
// the platform can be watched behaving like production — in the UI, end to end, with real
// coins moving.
//
// It is a lab for the PLATFORM, not for AI. The agents contain no model and make no
// network calls to one: every decision, every line of table talk, and every latency is a
// pure function of (persona, match, round, seat). Same run, same result. What they
// reproduce is the *shape* of an LLM agent — seconds of thinking per turn, opinionated
// chat, a rationale citing the numbers it saw, and a token report — because that shape is
// what actually stresses the product: shot clocks, phase deadlines, "waiting on seat N",
// long-poll reads, the live chat feed, the decision inspector, and the money path.
//
// Usage (the server must already be running):
//
//	API_BASE=http://localhost:8090 AGENT_HOST=pyyol-gamelab go run ./cmd/gamelab -game goofspiel
//
// AGENT_HOST is the address the PLATFORM uses to reach these agents. When the server runs
// in a container, that must be a name the server's network can resolve — not localhost.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// labEndpointSecret is the endpoint token the harness shares with the simulated agents it
// spawns. Named rather than inline so it is obvious this is one lab-local constant and
// not a credential that leaked into source.
const labEndpointSecret = "lab-endpoint-secret" // #nosec G101 -- local lab harness only

func main() {
	game := flag.String("game", "goofspiel", "game to run: goofspiel|mafia|monopoly")
	stake := flag.Int64("stake", 500, "coins staked per seat (0 = free practice table)")
	basePort := flag.Int("base-port", 9101, "first local port for the agent endpoints")
	runLabel := flag.String("label", "", "suffix for agent names, so repeat runs are distinguishable")
	latencyScale := flag.Float64("latency-scale", 1, "multiply every simulated decision latency (8 pushes a reasoning persona past a 60s shot clock)")
	latencyCap := flag.Float64("latency-cap-ms", 26000, "cap on a sampled latency; raise it when the point of the run is to blow the deadline")
	goDark := flag.Int("go-dark-after", 0, "from this round on, an agent stops answering entirely (0 = never)")
	goDarkSeat := flag.Int("go-dark-seat", -1, "which seat goes dark (-1 = all of them)")
	flag.Parse()

	LatencyScale, LatencyCapMS = *latencyScale, *latencyCap
	GoDarkAfterRound, GoDarkSeat = *goDark, *goDarkSeat

	base := envOr("API_BASE", "http://localhost:8090")
	agentHost := envOr("AGENT_HOST", "host.docker.internal")

	lg := log.New(os.Stdout, "", log.Ltime)
	lg.Printf("gamelab → platform %s   agents reachable at %s   game=%s stake=%d",
		base, agentHost, *game, *stake)

	a := newAPI(base)
	if err := a.waitHealthy(60 * time.Second); err != nil {
		lg.Fatalf("FATAL: %v", err)
	}
	lg.Printf("platform healthy")

	label := *runLabel
	if label == "" {
		label = fmt.Sprintf("%d", time.Now().Unix()%100000)
	}

	n := seatsFor(*game)
	agents := make([]*labAgent, 0, n)
	for i := 0; i < n; i++ {
		p := personas[i%len(personas)]
		ag := &labAgent{
			Persona: p,
			Port:    *basePort + i,
			Host:    agentHost,
			api:     a,
			log:     log.New(os.Stdout, fmt.Sprintf("[%-14s] ", p.Name), log.Ltime),
		}
		if err := ag.serve(); err != nil {
			lg.Fatalf("FATAL: agent %s could not listen on :%d: %v", p.Name, ag.Port, err)
		}
		lg.Printf("agent endpoint up: %s → %s", p.Name, ag.endpointURL())
		agents = append(agents, ag)
	}

	// Onboard each agent exactly as a developer would: sign up, submit a manifest that
	// declares the endpoint and the model, set the endpoint secret, then let the platform
	// verify the endpoint by calling it.
	for i, ag := range agents {
		if err := ag.onboard(label, i); err != nil {
			lg.Fatalf("FATAL: onboarding %s: %v", ag.Persona.Name, err)
		}
		lg.Printf("onboarded %-14s agent=%s verified endpoint, model=%s/%s",
			ag.Persona.Name, ag.AgentID, ag.Persona.Provider, ag.Persona.Model)
	}

	// Each agent needs an AGENT-scope key: it is the credential for acting and for table
	// talk (chat is the agent speaking, not its owner).
	for _, ag := range agents {
		key, err := a.createAgentKey(ag.DashToken, ag.AgentID)
		if err != nil {
			lg.Fatalf("FATAL: %s: %v", ag.Persona.Name, err)
		}
		ag.AgentKey = key
	}

	lg.Printf("")
	lg.Printf("──────────────────────────────────────────────────────────────")
	lg.Printf(" %d agents live, endpoints verified, keys issued.", len(agents))
	for _, ag := range agents {
		lg.Printf("   %-14s %s  (%s / %s, ~%.1fs median think)",
			ag.Persona.Name, ag.AgentID, ag.Persona.Provider, ag.Persona.Model,
			float64(ag.Persona.ThinkMedianMS)/1000)
	}
	lg.Printf("──────────────────────────────────────────────────────────────")

	// Start a driven match per agent. Push-play is free (no stake), which means no
	// funding step and no certification gate — the fastest way to see real decisions,
	// real thinking latency, and the live chat feed in the UI.
	for _, ag := range agents {
		matchID, err := a.startSandboxPushPlay(ag.AgentKey, "medium")
		if err != nil {
			lg.Printf("WARN: %s could not start a match: %v", ag.Persona.Name, err)
			continue
		}
		lg.Printf("MATCH STARTED  %-14s  %s", ag.Persona.Name, matchID)
		lg.Printf("   watch: %s/watch   ·   trace: %s/traces/%s", webBase(), webBase(), matchID)
	}
	lg.Printf("")

	// Keep serving: the platform pushes turns to these endpoints for as long as the lab
	// runs. Matches are started by the caller (see the README in this folder) or by the
	// queue, and every turn is logged above with its thinking time and rationale.
	select {}
}

// onboard runs the real developer onboarding flow for one simulated agent.
func (ag *labAgent) onboard(label string, idx int) error {
	a := ag.api
	ag.Email = fmt.Sprintf("lab+%s-%d@pyyol.test", label, idx)

	var signup struct {
		DashboardToken string `json:"dashboard_token"`
		AgentID        string `json:"agent_id"`
		UserID         string `json:"user_id"`
		AgentKey       string `json:"agent_key"`
		APIKey         string `json:"api_key"`
	}
	// The account-level agent name is constrained to slug characters; the human-readable
	// persona name goes in the manifest, which is what the UI renders.
	acctName := fmt.Sprintf("%s-%s", ag.Persona.Slug, label)
	if len(acctName) > 32 {
		acctName = acctName[:32]
	}
	// Underscores are permitted, so the persona name stays readable in the UI without
	// collapsing to a lowercase slug.
	name := strings.ReplaceAll(ag.Persona.Name, " ", "_")
	if err := a.mustDo("signup", http.MethodPost, "/v1/auth/signup", "", map[string]any{
		"email":       ag.Email,
		"password":    "lab-passphrase-strong-2026",
		"agent_name":  acctName,
		"description": fmt.Sprintf("Deterministic lab agent (%s style), simulating %s latency.", ag.Persona.Style, ag.Persona.Model),
	}, &signup, http.StatusCreated, http.StatusOK); err != nil {
		return err
	}
	ag.AgentID, ag.DashToken = signup.AgentID, signup.DashboardToken
	ag.AgentKey = firstNonEmpty(signup.AgentKey, signup.APIKey)

	manifest := map[string]any{
		"manifestVersion": "1.0",
		"agent": map[string]any{
			"name": name, "description": "Deterministic lab agent with LLM-shaped latency",
			"version": "1.9.0", "visibility": "public",
		},
		"developer": map[string]any{"name": "Pyyol Lab", "organization": "Pyyol"},
		"games":     []string{"goofspiel", "mafia", "monopoly"},
		"endpoint":  map[string]any{"url": ag.endpointURL(), "authentication": "bearer-token"},
		"runtime":   map[string]any{"timeout": 30000, "maxMemory": "512MB"},
		"model":     map[string]any{"provider": ag.Persona.Provider, "model": ag.Persona.Model, "reasoning": true},
		"sdk":       map[string]any{"language": "Go", "version": "1.9.0"},
		"contact":   map[string]any{"email": ag.Email},
	}
	var submitted struct {
		ManifestID string `json:"manifest_id"`
		Status     string `json:"status"`
	}
	if err := a.mustDo("submit manifest", http.MethodPost,
		"/v1/agents/"+ag.AgentID+"/manifest", ag.DashToken, manifest, &submitted,
		http.StatusCreated, http.StatusOK); err != nil {
		return err
	}
	// #nosec G101 -- not a credential: this is the shared secret the lab harness agrees
	// with its own throwaway endpoints, on a local-only stack. Nothing here is reachable
	// from outside the docker network and the value is regenerated with the lab.
	if err := a.mustDo("endpoint secret", http.MethodPut,
		"/v1/agents/"+ag.AgentID+"/manifest/"+submitted.ManifestID+"/endpoint-secret", ag.DashToken,
		map[string]any{"token": labEndpointSecret}, nil, http.StatusOK); err != nil {
		return err
	}
	var report struct {
		Verified    bool `json:"verified"`
		HealthOK    bool `json:"health_ok"`
		HandshakeOK bool `json:"handshake_ok"`
	}
	if err := a.mustDo("verify endpoint", http.MethodPost,
		"/v1/agents/"+ag.AgentID+"/manifest/"+submitted.ManifestID+"/verify", ag.DashToken,
		nil, &report, http.StatusOK); err != nil {
		return err
	}
	if !report.Verified {
		return fmt.Errorf("endpoint not verified (health=%v handshake=%v) — is AGENT_HOST reachable from the server?",
			report.HealthOK, report.HandshakeOK)
	}
	return nil
}

func seatsFor(game string) int {
	switch strings.ToLower(game) {
	case "goofspiel":
		return 2
	case "monopoly":
		return 4
	case "mafia":
		return 4 // 4 humans; house bots fill the rest of the 12-seat roster
	default:
		return 2
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// readAllLimited reads a request body with a sane cap.
func readAllLimited(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(io.LimitReader(r.Body, 4<<20))
}

// webBase is the UI origin, used only to print watchable links in the log.
func webBase() string { return envOr("WEB_BASE", "http://localhost:3100") }
