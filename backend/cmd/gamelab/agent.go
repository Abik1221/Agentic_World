package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"net"
	"net/http"
	"time"
)

// agent.go — a simulated developer agent that behaves like an LLM-backed one WITHOUT
// calling any model.
//
// The point of the lab is to watch the PLATFORM, not to test a model, so every agent
// here is deterministic: same seed, same match, same decisions, same chat, same
// latencies. What it deliberately reproduces is the *shape* of a real LLM agent, because
// that shape is what stresses the platform:
//
//   - It THINKS SLOWLY. A real agent spends seconds per decision, so phase windows,
//     shot clocks, timeout-forfeits, long-poll waits, and the "who are we waiting on"
//     UI only behave realistically under real latency. Instant bots hide all of it.
//   - It TALKS. Table talk goes through the same POST /say an SDK agent uses, so the
//     live chat feed is exercised for real.
//   - It EXPLAINS itself. Every move carries a rationale referencing the actual numbers
//     it saw, which is what the decision inspector renders.
//   - It REPORTS TOKENS. Usage is attached to each move so per-decision cost, the
//     latency range, and the model board have real data to aggregate.
//
// Determinism comes from hashing (persona, matchID, round, seat) — never from a clock or
// a global RNG — so a rerun of the same match is byte-identical and a bug found here is
// reproducible.

// persona is a fixed playing style plus a declared model identity. Personas differ so a
// match is a contest rather than a mirror, and so the model board has more than one row.
type persona struct {
	Name     string
	Slug     string
	Provider string
	Model    string
	// Style selects the decision policy. See goofspielCard.
	Style string
	// ThinkMedianMS is the median decision latency this persona simulates. A "reasoning"
	// model is slower and more variable than a small fast one.
	ThinkMedianMS int
	// ThinkSpreadMS widens the sampled latency; a bigger spread means an occasional very
	// long pause, which is exactly what a real reasoning model does.
	ThinkSpreadMS int
}

var personas = []persona{
	{Name: "Atlas Prime", Slug: "atlas-prime", Provider: "Anthropic", Model: "claude-opus-4", Style: "value", ThinkMedianMS: 5200, ThinkSpreadMS: 4200},
	{Name: "Vega Reasoner", Slug: "vega-reasoner", Provider: "OpenAI", Model: "gpt-5.2", Style: "counter", ThinkMedianMS: 7800, ThinkSpreadMS: 6500},
	{Name: "Nimbus Flash", Slug: "nimbus-flash", Provider: "Google", Model: "gemini-3-flash", Style: "aggressive", ThinkMedianMS: 1900, ThinkSpreadMS: 1200},
	{Name: "Orion Steady", Slug: "orion-steady", Provider: "Anthropic", Model: "claude-sonnet-4.6", Style: "hoard", ThinkMedianMS: 3400, ThinkSpreadMS: 2600},
}

// hashSeed derives a stable uint64 from any set of parts. Every random-looking choice in
// this package goes through here, so nothing depends on wall-clock time or map order.
func hashSeed(parts ...any) uint64 {
	h := fnv.New64a()
	for _, p := range parts {
		fmt.Fprintf(h, "%v|", p)
	}
	return h.Sum64()
}

// unitFloat maps a seed to [0,1) deterministically.
func unitFloat(seed uint64) float64 {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, seed)
	return float64(binary.LittleEndian.Uint64(b)%1_000_000) / 1_000_000.0
}

// thinkTime samples this persona's decision latency for one specific decision.
//
// Log-normal-ish rather than uniform, because that is how model latency actually
// distributes: most calls near the median, a thin tail of slow ones. The tail matters —
// it is what pushes a seat close to its phase deadline and exercises the shot clock and
// the forfeit path. Capped so a lab run cannot hang forever.
func (p persona) thinkTime(matchID string, round, seat int) time.Duration {
	u := unitFloat(hashSeed("think", p.Slug, matchID, round, seat))
	// Map u through an inverse-normal-ish curve for a right-skewed spread.
	z := math.Log(1/(1-u+1e-9)) - 0.6
	ms := float64(p.ThinkMedianMS) + z*float64(p.ThinkSpreadMS)
	if ms < 400 {
		ms = 400
	}
	// Stress knobs. Off by default (scale 1, cap 26s) so an ordinary lab run still shows
	// play rather than forfeits; raised deliberately to exercise the shot clock, the
	// fallback path and the absence forfeit against a REAL server rather than a fake.
	ms *= LatencyScale
	if ms > LatencyCapMS {
		ms = LatencyCapMS
	}
	if ms < 400 {
		ms = 400
	}
	return time.Duration(ms) * time.Millisecond
}

// LatencyScale multiplies every simulated decision latency. 1 is the honest persona
// model; 8 pushes a reasoning persona's tail past a 60s shot clock, which is what proves
// the platform waits the full window instead of cutting the agent off early.
var LatencyScale = 1.0

// LatencyCapMS bounds the sample so a lab run cannot hang. Raised alongside LatencyScale
// when the point of the run IS to blow the deadline.
var LatencyCapMS = 26000.0

// GoDarkAfterRound makes an agent stop answering entirely from this round on: the handler
// hangs rather than replying, which is what a crashed or wedged agent looks like from the
// platform's side. 0 disables it.
//
// A hang, not a 500, on purpose — an error is answered instantly and exercises the
// transport-error path, while silence is what actually drives the shot clock to expire
// and the absence forfeit to arm.
var GoDarkAfterRound = 0

// GoDarkSeat selects which seat goes dark. -1 means every seat.
var GoDarkSeat = -1

// tokens fabricates a plausible usage report: prompt grows with how much history the
// view carried, completion tracks how long the agent "thought".
func (p persona) tokens(viewBytes int, think time.Duration) map[string]any {
	prompt := 420 + viewBytes/3
	completion := 40 + int(think.Milliseconds()/22)
	reasoning := 0
	if p.ThinkSpreadMS > 3000 { // the reasoning-heavy personas report reasoning tokens
		reasoning = completion * 3
	}
	return map[string]any{
		"provider":          p.Provider,
		"model":             p.Model,
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"reasoning_tokens":  reasoning,
		"total_tokens":      prompt + completion + reasoning,
	}
}

// labAgent is one simulated agent: an HTTP endpoint the platform pushes turns to, plus
// the credentials it needs to talk back (chat).
type labAgent struct {
	Persona   persona
	AgentID   string
	OwnerID   string
	Email     string
	DashToken string
	AgentKey  string
	Port      int
	// Host is the address the PLATFORM uses to reach this agent. In the lab the backend
	// runs in a container, so this is the container-network hostname, not localhost.
	Host string
	api  *api
	srv  *http.Server
	log  *log.Logger
}

func (a *labAgent) endpointURL() string { return fmt.Sprintf("http://%s:%d/play", a.Host, a.Port) }

// serve starts the agent's endpoint. /health and /handshake are what manifest
// verification calls; /play is the turn protocol.
func (a *labAgent) serve() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "healthy", "agent": a.Persona.Name, "version": "1.0.0"})
	})
	mux.HandleFunc("/handshake", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"accepted": true, "sdkVersion": "1.9.0",
			// Advertise only what this agent actually plays. Listing mafia here while /play answered
			// it with an empty object is what let fallback-driven matches look like agent matches.
			"supportedGames": []string{"goofspiel", "monopoly"},
		})
	})
	mux.HandleFunc("/play", a.handlePlay)
	// The platform also delivers async lifecycle calls; accept and log them so the lab
	// shows that the whole protocol is exercised, not just the turn call.
	for _, path := range []string{"/initialize", "/event", "/game-end"} {
		p := path
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if p == "/game-end" {
				a.log.Printf("GAME END received: %s", compactJSON(body))
			}
			writeJSON(w, map[string]any{"ok": true})
		})
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", a.Port))
	if err != nil {
		return err
	}
	a.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := a.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			a.log.Printf("endpoint stopped: %v", err)
		}
	}()
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "<unmarshalable>"
	}
	if len(b) > 400 {
		return string(b[:400]) + "…"
	}
	return string(b)
}
