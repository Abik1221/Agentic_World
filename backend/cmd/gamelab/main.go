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
	// Provider transport reliability is a result, not plumbing. Reported unconditionally at exit
	// so a run that quietly leaned on retries cannot be read as one that did not need them.
	defer func() {
		if calls, retried, gaveUp := TransportStats(); calls > 0 && (retried > 0 || gaveUp > 0) {
			fmt.Printf("TRANSPORT  %d model calls  ·  %d retried after an incomplete response  ·  %d abandoned\n",
				calls, retried, gaveUp)
		}
	}()
	game := flag.String("game", "goofspiel", "game to run: goofspiel|mafia|monopoly")
	tier := flag.String("tier", "", "stake tier for a REAL staked table: low|mid|high (empty = free practice)")
	stake := flag.Int64("stake", 0, "coins staked per seat, informational (0 = free practice table)")
	bindBatch := flag.Int("bind-batch", 0, "with -bind, one model call covers this many rounds (the agent plans ahead); produces fewer bindings than rounds, legitimately")
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
	// Seating agents this process does NOT host.
	//
	// -public-url already did this for seat 0, for the tunnel case. The benchmark needs it
	// for EVERY seat: the model harness (harness/bench) is a Python process per model, and
	// the whole point of the run is that those processes decide the moves. gamelab keeps the
	// parts that are hard and already correct — admin onboarding as kind='harness', manifest
	// verification, wallet funding, the group queue, batching — and stops pretending to be
	// the brain.
	//
	// A seat with a URL here is not served locally; the external process is expected to be
	// already listening, and onboarding's own verification probe is what proves it. That
	// probe is why an unreachable URL fails loudly at registration rather than as a forfeit
	// on the first turn.
	agentURLs := flag.String("agent-urls", "",
		"comma-separated base URLs, one per seat, for agents hosted OUTSIDE this process "+
			"(e.g. \"http://bench-opus5:9101,http://bench-gpt56:9102\"). An empty entry means "+
			"host that seat locally as usual. Overrides -public-url for the seats it names.")
	churn := flag.Int("churn", 0, "run the queue-churn test for N ticks: a mixed population of autoplay, one-shot, underfunded and late-joining agents")
	// Completion binding. Off by default: adding a proxy hop to an ordinary run would change
	// the shot-clock and forfeit behaviour the lab exists to measure.
	bindGw := flag.Bool("bind", false, "route every decision through the LLM Gateway as a structured play_card tool call, so the turn is completion-bound")
	bindStream := flag.Bool("bind-stream", false, "with -bind, request a STREAMED completion (exercises the SSE tool-call reassembly path)")
	substituteAt := flag.Int("substitute-at", 0, "with -bind, from this round on submit a card the model did NOT choose; the platform must reject the move (0 = never)")
	// Honest-but-unbound behaviour, for measuring the ranked threshold against a realistic
	// population rather than against a harness that binds every round by construction.
	bindFailPct := flag.Int("bind-fail-pct", 0, "with -bind, this %% of turns have their model call FAIL; the agent falls back to its strategy and plays on, unbound (honest, not cheating)")
	bindProvider := flag.String("bind-provider", "", "gateway upstream to route through (e.g. groq); default is the local stand-in on the anthropic path")
	bindModel := flag.String("bind-model", "llama-3.1-8b-instant", "model id to ask that provider for. COMMA-SEPARATED seats a different model per agent, e.g. \"a:free,b:free\" — that is what makes a run a model-vs-model comparison")
	matches := flag.Int("matches", 0, "play this many matches one after another in THIS process, then exit (0 = play one and keep serving). Batching in one process avoids re-onboarding and the port races that killing the process between runs causes")
	perMatch := flag.Duration("per-match-timeout", 8*time.Minute, "with -matches, give up waiting on a single match after this long and move to the next")
	// step of onboarding (see harness.go) and nothing about how the agents then play.
	flag.Parse()


	LatencyScale, LatencyCapMS = *latencyScale, *latencyCap
	GoDarkAfterRound, GoDarkSeat = *goDark, *goDarkSeat

	base := envOr("API_BASE", "http://localhost:8090")

	// Completion binding. The gateway lives on the platform itself, so the agent reaches it at
	// the same base URL — one fewer thing to configure wrongly, and it cannot drift from the
	// server the match is actually running on.
	BindThroughGateway, BindStream, SubstituteAtRound = *bindGw, *bindStream, *substituteAt
	BindFailPct, BindBatchRounds = *bindFailPct, *bindBatch
	BindProvider, BindKey, BindModel = *bindProvider, os.Getenv("PYYOL_PROVIDER_KEY"), *bindModel
	// Per-seat upstreams, so one match can pit one provider's model against another's.
	// Keys are read from the environment and never from a flag: a flag lands in the process
	// table and in shell history, and these are live provider credentials.
	if strings.Contains(*bindProvider, ",") {
		BindProviders = strings.Split(*bindProvider, ",")
		for i, p := range BindProviders {
			BindProviders[i] = strings.TrimSpace(p)
			// PYYOL_PROVIDER_KEY_<UPPER> per upstream, falling back to the shared key so a
			// single-provider run is unchanged.
			k := os.Getenv("PYYOL_PROVIDER_KEY_" + strings.ToUpper(BindProviders[i]))
			if k == "" {
				k = BindKey
			}
			BindKeys = append(BindKeys, k)
		}
	}
	// A comma-separated -bind-model seats a DIFFERENT model per agent, which is what turns a
	// run into a paired comparison instead of one model playing itself.
	if strings.Contains(*bindModel, ",") {
		for _, m := range strings.Split(*bindModel, ",") {
			if m = strings.TrimSpace(m); m != "" {
				BindModels = append(BindModels, m)
			}
		}
	}
	BindGatewayBase = base
	if BindThroughGateway {
		lg := log.New(os.Stdout, "", log.Ltime)
		lg.Printf("completion binding ON — every decision goes through %s as a %s play_card tool call",
			base, map[bool]string{true: "STREAMED", false: "single-object"}[BindStream])
		if SubstituteAtRound > 0 {
			lg.Printf("SUBSTITUTION ARMED from round %d — the platform MUST reject those moves; "+
				"a match that completes cleanly means enforcement is NOT working", SubstituteAtRound)
		}
	}
	agentHost := envOr("AGENT_HOST", "host.docker.internal")

	lg := log.New(os.Stdout, "", log.Ltime)
	lg.Printf("gamelab → platform %s   agents reachable at %s   game=%s stake=%d",
		base, agentHost, *game, *stake)

	a := newAPI(base)
	if err := a.waitHealthy(60 * time.Second); err != nil {
		lg.Fatalf("FATAL: %v", err)
	}
	lg.Printf("platform healthy")

	// Harness mode is resolved BEFORE any account is created, and a failure here is fatal.
	//
	// The bug this guards against is silent: without it, onboarding falls back to the public
	// signup, the agents come out kind='external', and the run lands on the public developer
	// leaderboard while /harness stays empty. Nothing in the log says so, and the matches are
	// real, so the only way to notice is to read the board afterwards and wonder.

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
	// One entry per seat, empty where the seat is hosted locally. Split once rather than
	// per iteration so a trailing comma or a short list is a defined shape (missing seats
	// simply fall through to local hosting) instead of an index panic mid-onboarding.
	externalURLs := splitSeatURLs(*agentURLs, n)

	agents := make([]*labAgent, 0, n)
	for i := 0; i < n; i++ {
		p := personas[i%len(personas)]
		ag := &labAgent{
			Persona: p,
			Index:   i,
			Port:    *basePort + i,
			Host:    agentHost,
			PublicURL: func() string {
				// -agent-urls wins where it names a seat: it is the more specific flag, and
				// a run that set both almost certainly meant the per-seat one.
				if externalURLs[i] != "" {
					return externalURLs[i]
				}
				if i == 0 {
					return *publicURL
				}
				return ""
			}(),
			api: a,
			log: log.New(os.Stdout, fmt.Sprintf("[%-14s] ", p.Name), log.Ltime),
		}
		// Only bind a port for a seat WE host. Serving a local endpoint for an externally
		// hosted seat would bind a port nobody calls, and — worse — would answer /health and
		// /handshake, so a misconfigured URL would pass verification against the wrong
		// process and the run would silently measure gamelab's built-in strategy instead of
		// the model. Not serving makes that failure loud.
		if externalURLs[i] == "" {
			if err := ag.serve(); err != nil {
				lg.Fatalf("FATAL: agent %s could not listen on :%d: %v", p.Name, ag.Port, err)
			}
			lg.Printf("agent endpoint up: %s → %s", p.Name, ag.endpointURL())
		} else {
			lg.Printf("agent endpoint EXTERNAL: %s → %s (this process will not serve it)",
				p.Name, ag.endpointURL())
		}
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
	// -bind ONCE REFUSED ANYTHING BUT GOOFSPIEL, and refusing was right at the time.
	//
	// The binding path asks for a single-object tool call, and it used to be shaped only for
	// Goofspiel's move. Combined with -game mafia or -game monopoly the harness ran a
	// Goofspiel match anyway — verifying a game nobody asked about while logging the game
	// they did. It only surfaced by reading round/prize/hand instead of trusting the header.
	// A verification tool that silently tests the wrong thing is worse than one that refuses.
	// MAFIA AND MONOPOLY ARE NOW BINDABLE (see bindgames.go). All three games can be
	// completion-bound, so there is no game left for -bind to silently substitute.
	// One function that starts ONE match, used by both the single run and the batch, so a
	// batch cannot drift from the path a normal run takes.
	startOne := func() error {
		if *tier != "" {
			if err := runStakedTable(a, lg, agents, *game, *tier); err != nil {
				// A HARNESS RUN MUST NOT FALL BACK. Free push-play seats HOUSE BOTS around
				// the agent, and a house bot produces no benchmark row — so the match yields
				// ONE attributed seat instead of two, and a paired comparison needs two in
				// the same match. The fallback therefore turns a benchmark table into a
				// non-event that still looks like a completed match afterwards.
				//
				// Measured, not feared: a 4-match batch produced 8 matches each seating one
				// harness agent against ag_house_challenger, 43 attributable model calls, and
				// ZERO comparisons. The board kept showing older data and nothing said why.
				//
				// Same rule createHarnessAccount already applies to signup: a harness run that
				// cannot do the right thing stops, rather than quietly doing a different thing
				// that reads as success.

				lg.Printf("WARN: staked table could not start (%v) — falling back to free push-play", err)
				startFreePushPlay(a, lg, agents, *game)
			}
			return nil
		}

		startFreePushPlay(a, lg, agents, *game)
		return nil
	}

	if *matches > 0 {
		runBatch(lg, *matches, *perMatch, startOne)
		// EXIT rather than falling through to select{}. Exiting is the whole point: a batch
		// that hung would have to be killed, and a killed process is what races the next one
		// for its ports.
		return
	}
	_ = startOne()
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
	account := map[string]any{
		"email":       ag.Email,
		"password":    "lab-passphrase-strong-2026",
		"agent_name":  acctName,
		"description": fmt.Sprintf("Deterministic lab agent (%s style), simulating %s latency.", ag.Persona.Style, ag.Persona.Model),
	}
	// THE ONLY STEP -harness CHANGES. A harness seat is created through the admin route so
	// it is kind='harness' from its first row; everything below this point — manifest,
	// endpoint verification, agent key, funding, lobby/queue, completion binding — is the
	// identical developer flow, which is what makes the two boards measure the same object.
	if err := a.mustDo("signup", http.MethodPost, "/v1/auth/signup", "", account,
		&signup, http.StatusCreated, http.StatusOK); err != nil {
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
func startFreePushPlay(a *api, lg *log.Logger, agents []*labAgent, game string) {
	for _, ag := range agents {
		matchID, err := a.startSandboxPushPlay(ag.AgentKey, game, "medium")
		if err != nil {
			lg.Printf("WARN: %s could not start a match: %v", ag.Persona.Name, err)
			continue
		}
		lg.Printf("MATCH STARTED  %-14s  %s  (free practice, %s)", ag.Persona.Name, matchID, game)
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
func runStakedTable(a *api, lg *log.Logger, agents []*labAgent, game, tier string) error {
	need := seatsFor(game)
	if len(agents) < need {
		return fmt.Errorf("need %d agents for a staked %s table, have %d", need, game, len(agents))
	}
	host, guest := agents[0], agents[1]

	// Fund generously so a run of several matches does not stall on an empty wallet
	// mid-way and look like a platform failure. The tier decides the real stake; this is
	// simply enough to cover it many times over.
	const funding = int64(20000)
	// FUND EVERY SEAT, not just the first two.
	//
	// This funded only host+guest, which was right when a staked table was Goofspiel 1v1. Group
	// games enqueue ALL agents, so seats 3+ arrived at the queue with Balance 0 and were refused
	// with HTTP 402 — the staked table then failed and the harness fell back to free push-play,
	// quietly running Goofspiel instead of the game that was asked for.
	for _, ag := range agents {
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

	// N-PLAYER GAMES DO NOT USE THE 1v1 LOBBY.
	//
	// createStakedTable/joinStakedTable are the two-seat goofspiel path. Mafia and Monopoly
	// matchmake through the group queue, where every agent enqueues and the matcher seats them.
	// Using the lobby for them is what made `-game mafia -tier low` produce a GOOFSPIEL match:
	// the harness logged "game=mafia", sized seats for mafia, then created a goofspiel table.
	if isGroupGame(game) {
		for _, ag := range agents {
			code, body, err := a.enqueueRanked(ag.AgentKey, game, tier)
			if err != nil {
				return fmt.Errorf("enqueue %s: %w", ag.Persona.Name, err)
			}
			if code != http.StatusOK && code != http.StatusCreated && code != http.StatusAccepted {
				return fmt.Errorf("enqueue %s: HTTP %d — %s", ag.Persona.Name, code, body)
			}
		}
		lg.Printf("ENQUEUED %d agents into the %s group queue at tier=%s — the matcher seats them",
			len(agents), game, tier)
		lg.Printf("   watch: %s   ·   agents play from their own endpoints", webBase())
		return nil
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

// splitSeatURLs turns the -agent-urls list into exactly n entries, one per seat.
//
// Short lists pad with "" (host that seat locally) and long ones are truncated, because the
// alternative — indexing a caller-supplied slice by seat — turns a trailing comma into a
// panic partway through onboarding, after accounts have been created and funded. A run that
// seats one fewer external agent than intended is recoverable; a half-onboarded run leaves
// wallets and agents behind.
//
// Entries are trimmed and their trailing slash removed so "http://h:9101/" and "http://h:9101"
// are the same seat, which matters because endpointURL appends "/play".
func splitSeatURLs(raw string, n int) []string {
	out := make([]string, n)
	if strings.TrimSpace(raw) == "" {
		return out
	}
	for i, part := range strings.Split(raw, ",") {
		if i >= n {
			break
		}
		out[i] = strings.TrimRight(strings.TrimSpace(part), "/")
	}
	return out
}
