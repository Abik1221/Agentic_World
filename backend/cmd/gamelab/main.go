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
	stake := flag.Int64("stake", 0, "coins staked per seat, informational (0 = free practice table)")
	tier := flag.String("tier", "", "stake tier for a REAL staked table: low|mid|high (empty = free practice)")
	basePort := flag.Int("base-port", 9101, "first local port for the agent endpoints")
	runLabel := flag.String("label", "", "suffix for agent names, so repeat runs are distinguishable")
	latencyScale := flag.Float64("latency-scale", 1, "multiply every simulated decision latency (8 pushes a reasoning persona past a 60s shot clock)")
	latencyCap := flag.Float64("latency-cap-ms", 26000, "cap on a sampled latency; raise it when the point of the run is to blow the deadline")
	goDark := flag.Int("go-dark-after", 0, "from this round on, an agent stops answering entirely (0 = never)")
	goDarkSeat := flag.Int("go-dark-seat", -1, "which seat goes dark (-1 = all of them)")
	// A public base URL for seat 0, so one agent is genuinely DEPLOYED (reached over the internet
	// through a tunnel) while the rest stay local. That asymmetry is the point: it proves the
	// platform reaches an agent it shares no network with, and that nothing on the turn path
	// quietly assumes a docker hostname or a local port.
	publicURL := flag.String("public-url", "", "external base URL for seat 0 (e.g. a tunnel); empty = all agents local")
	churn := flag.Int("churn", 0, "run the queue-churn test for N ticks: a mixed population of autoplay, one-shot, underfunded and late-joining agents")
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
	if *churn > 0 && n < 5 {
		// autoplay x2, one-shot, underfunded, latecomer — the smallest population in which the
		// four behaviours can actually interfere with each other.
		n = 5
	}
	agents := make([]*labAgent, 0, n)
	for i := 0; i < n; i++ {
		p := personas[i%len(personas)]
		ag := &labAgent{
			Persona: p,
			Port:    *basePort + i,
			Host:    agentHost,
			PublicURL: func() string {
				if i == 0 {
					return *publicURL
				}
				return ""
			}(),
			api: a,
			log: log.New(os.Stdout, fmt.Sprintf("[%-14s] ", p.Name), log.Ltime),
		}
		if err := ag.serve(); err != nil {
			lg.Fatalf("FATAL: agent %s could not listen on :%d: %v", p.Name, ag.Port, err)
		}
		lg.Printf("agent endpoint up: %s → %s", p.Name, ag.endpointURL())
		agents = append(agents, ag)
	}

	// A publicly-deployed agent must be REACHABLE before it registers.
	//
	// Without this the lab registered its tunnel URL roughly one second after binding the port,
	// and the platform's verification probe arrived before the tunnel had routed a single
	// request — onboarding failed with "endpoint not verified", which reads as a platform fault
	// and is not one. A real deployment is already serving by the time a developer registers it;
	// the lab was modelling a sequence nobody actually performs.
	//
	// Measured: the first request through a fresh tunnel takes ~2s, subsequent ones ~0.5s.
	for _, ag := range agents {
		if ag.PublicURL == "" {
			continue
		}
		if err := waitPublicReachable(ag.endpointURL(), 90*time.Second); err != nil {
			lg.Fatalf("FATAL: %s is registered at %s but that URL never became reachable: %v\n"+
				"The platform verifies an endpoint by calling it, so an unreachable deployment "+
				"cannot be onboarded — check the tunnel/proxy before blaming the platform.",
				ag.Persona.Name, ag.endpointURL(), err)
		}
		lg.Printf("public endpoint reachable: %s", ag.endpointURL())
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

	// STAKED or free, and the difference is the whole point of the flag.
	//
	// Push-play is free by construction: no stake, no escrow, no settlement. It exercises
	// decisions, latency and chat but can never show whether an absent agent actually
	// forfeits its coins — which is the rule the arena is built on. With a stake the lab
	// funds both wallets and pairs the agents on a real table, so escrow, rake, payout and
	// forfeit all run for real.
	if *churn > 0 {
		// Churn needs a real stake, or none of the money-driven removals can happen.
		t := *tier
		if t == "" {
			t = "low"
		}
		if err := runChurn(a, lg, agents, t, *churn); err != nil {
			lg.Fatalf("CHURN FAILED: %v", err)
		}
		lg.Printf("churn passed")
		return
	}
	if *tier != "" {
		if err := runStakedTable(a, lg, agents, *tier); err != nil {
			lg.Printf("WARN: staked table could not start (%v) — falling back to free push-play", err)
			startFreePushPlay(a, lg, agents)
		}
	} else {
		startFreePushPlay(a, lg, agents)
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

// startFreePushPlay opens one free practice match per agent — the original lab behaviour.
func startFreePushPlay(a *api, lg *log.Logger, agents []*labAgent) {
	for _, ag := range agents {
		matchID, err := a.startSandboxPushPlay(ag.AgentKey, "medium")
		if err != nil {
			lg.Printf("WARN: %s could not start a match: %v", ag.Persona.Name, err)
			continue
		}
		lg.Printf("MATCH STARTED  %-14s  %s  (free practice)", ag.Persona.Name, matchID)
		lg.Printf("   watch: %s/watch   ·   trace: %s/traces/%s", webBase(), webBase(), matchID)
	}
}

// runStakedTable funds two agents and seats them on a real staked table.
//
// Deliberately the SAME endpoints a developer's agent uses — dev checkout to fund, then
// lobby create/join — rather than writing rows directly. A harness that seeds the database
// proves nothing about the paths that run in production; this one exercises escrow, the
// stake gate, settlement and the absence forfeit exactly as a real table would.
//
// Needs at least two agents: a stake is a contest, and a table with one seat never starts.
func runStakedTable(a *api, lg *log.Logger, agents []*labAgent, tier string) error {
	if len(agents) < 2 {
		return fmt.Errorf("need 2 agents for a staked table, have %d", len(agents))
	}
	host, guest := agents[0], agents[1]

	// Fund generously so a run of several matches does not stall on an empty wallet
	// mid-way and look like a platform failure. The tier decides the real stake; this is
	// simply enough to cover it many times over.
	const funding = int64(20000)
	for _, ag := range []*labAgent{host, guest} {
		// Two steps, because there are two wallets: checkout credits the owner's
		// treasury, allocate moves it into the agent's playing wallet. Skipping the
		// second leaves a rich owner with an agent that cannot stake a single coin.
		if err := a.fundAgent(ag.DashToken, ag.AgentID, funding); err != nil {
			return fmt.Errorf("fund %s: %w", ag.Persona.Name, err)
		}
		if err := a.allocateToAgent(ag.DashToken, ag.AgentID, funding); err != nil {
			return fmt.Errorf("allocate to %s: %w", ag.Persona.Name, err)
		}
		bal, err := a.walletBalance(ag.AgentKey)
		if err != nil {
			lg.Printf("   (%s funded; balance read failed: %v)", ag.Persona.Name, err)
			continue
		}
		lg.Printf("FUNDED  %-14s  %d coins", ag.Persona.Name, bal)
	}

	matchID, err := a.createStakedTable(host.AgentKey, tier)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if err := a.joinStakedTable(guest.AgentKey, matchID); err != nil {
		return fmt.Errorf("join: %w", err)
	}

	lg.Printf("STAKED MATCH STARTED  %s   %s vs %s   tier=%s",
		matchID, host.Persona.Name, guest.Persona.Name, tier)
	lg.Printf("   watch: %s/watch   ·   trace: %s/traces/%s", webBase(), webBase(), matchID)
	return nil
}

// waitPublicReachable polls an agent's own public URL until it answers, or gives up.
//
// Polls /health rather than /play: /play needs a real turn payload, and a 400 from it would say
// nothing about whether the deployment is routable. This is the same question the platform's
// verifier asks, asked first, so a tunnel that is not yet routing fails HERE with a message about
// the tunnel instead of surfacing later as "endpoint not verified".
func waitPublicReachable(playURL string, timeout time.Duration) error {
	healthURL := strings.TrimSuffix(playURL, "/play") + "/health"
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, healthURL, nil)
		if err != nil {
			return err
		}
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			last = err
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("no 200 from %s within %s (last: %v)", healthURL, timeout, last)
}
