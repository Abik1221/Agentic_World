// Command gamespec renders the developer game reference from internal/gamespec —
// the single source of truth that pulls every role/phase/action/event value from
// the live engine constants. It writes two artifacts into the SDK docs:
//
//	sdk/docs/games.md       human + LLM readable reference (fed into llms-full.txt)
//	sdk/docs/gamespec.json  the same data, machine-readable, for tooling
//
// Run from the backend module root:
//
//	go run ./cmd/gamespec            # writes ../sdk/docs
//	go run ./cmd/gamespec -out DIR   # writes DIR
//
// CI regenerates and fails on drift (see .github/workflows/sdk-ci.yml). After
// running this, run sdk/docs/gen_llms.py to refresh llms.txt / llms-full.txt.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-arena/arena/internal/gamespec"
)

func main() {
	out := flag.String("out", filepath.Join("..", "sdk", "docs"), "output directory for games.md + gamespec.json")
	flag.Parse()

	games := gamespec.All()

	if err := os.WriteFile(filepath.Join(*out, "games.md"), []byte(renderMarkdown(games)), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "write games.md:", err)
		os.Exit(1)
	}
	buf, err := json.MarshalIndent(map[string]any{"games": games}, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal gamespec.json:", err)
		os.Exit(1)
	}
	buf = append(buf, '\n')
	if err := os.WriteFile(filepath.Join(*out, "gamespec.json"), buf, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "write gamespec.json:", err)
		os.Exit(1)
	}
	fmt.Println("wrote", filepath.Join(*out, "games.md"))
	fmt.Println("wrote", filepath.Join(*out, "gamespec.json"))
}

func renderMarkdown(games []gamespec.Game) string {
	var b strings.Builder
	b.WriteString("# Game APIs\n\n")
	b.WriteString("<!-- GENERATED FILE — do not edit by hand.\n")
	b.WriteString("     Source: backend/internal/gamespec (values come from the live engine constants).\n")
	b.WriteString("     Regenerate: `cd backend && go run ./cmd/gamespec` then `python sdk/docs/gen_llms.py`. -->\n\n")
	b.WriteString("Each turn the platform sends your seat a `game` field and a **redacted view** — " +
		"only what your seat may legitimately see. You return the move for that game. The official " +
		"SDKs parse the body into a typed view (`parse_view` / `parseView`) and serialize your move.\n\n")
	b.WriteString("The engine is **server-authoritative**: every move is validated against the rules, " +
		"and an illegal or late reply is replaced by a deterministic fallback — so a bad reply can " +
		"never wedge a match, and you can always ship a simple agent first and refine it later.\n\n")

	// Quick index.
	b.WriteString("| Game | Players | Status |\n| --- | --- | --- |\n")
	for _, g := range games {
		b.WriteString(fmt.Sprintf("| [%s](#%s) | %s | %s |\n", g.Title, strings.ToLower(g.Title), players(g), g.Status))
	}
	b.WriteString("\n")

	for _, g := range games {
		renderGame(&b, g)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func players(g gamespec.Game) string {
	if g.MinPlayers == g.MaxPlayers {
		return fmt.Sprintf("%d", g.MinPlayers)
	}
	return fmt.Sprintf("%d–%d", g.MinPlayers, g.MaxPlayers)
}

func renderGame(b *strings.Builder, g gamespec.Game) {
	fmt.Fprintf(b, "## %s\n\n", g.Title)
	fmt.Fprintf(b, "*%s*\n\n", g.Tagline)
	fmt.Fprintf(b, "%s\n\n", g.Overview)
	fmt.Fprintf(b, "**Players:** %s · **Status:** %s · **Per decision:** %s\n\n", players(g), g.Status, g.TurnBudget)

	fmt.Fprintf(b, "### How you win\n\n%s\n\n", g.WinCondition)

	b.WriteString("### Turn view\n\n")
	fields(b, g.ViewFields)

	b.WriteString("### Your move\n\n")
	fmt.Fprintf(b, "```json\n%s\n```\n\n", g.MoveSchema)
	fields(b, g.MoveFields)

	if len(g.Phases) > 0 {
		b.WriteString("### Phases\n\n| Phase | Meaning |\n| --- | --- |\n")
		for _, p := range g.Phases {
			fmt.Fprintf(b, "| `%s` | %s |\n", p.Value, p.Desc)
		}
		b.WriteString("\n")
	}

	if len(g.Roles) > 0 {
		b.WriteString("### Roles\n\n| Role | Description |\n| --- | --- |\n")
		for _, r := range g.Roles {
			fmt.Fprintf(b, "| `%s` | %s |\n", r.Value, r.Desc)
		}
		b.WriteString("\n")
	}

	if len(g.Actions) > 0 {
		b.WriteString("### Actions\n\n| Action | Legal in | Description |\n| --- | --- | --- |\n")
		for _, a := range g.Actions {
			fmt.Fprintf(b, "| `%s` | %s | %s |\n", a.Value, phaseList(a.Phases), a.Desc)
		}
		b.WriteString("\n")
	}

	// Rendered after the tables and before Events: the tables say WHAT an action is, and
	// these say what actually happens when you use it. Reading order matters for the LLM
	// corpus too — llms-full.txt is this file, so a rule explained out of order is a rule
	// an agent applies out of order.
	if len(g.Deep) > 0 {
		b.WriteString("### Rules in depth\n\n")
		for _, d := range g.Deep {
			fmt.Fprintf(b, "#### %s\n\n%s\n\n", d.Title, strings.TrimSpace(d.Body))
		}
	}

	b.WriteString("### Events\n\n")
	b.WriteString("Between turns the platform pushes `/event` notifications (each `{seq, type, payload}`; " +
		"order by `seq`) so you can build memory. `/game-end` delivers the final `result`. Both are " +
		"one-way — do not block.\n\n")
	b.WriteString("| Event `type` | Meaning |\n| --- | --- |\n")
	for _, e := range g.Events {
		fmt.Fprintf(b, "| `%s` | %s |\n", e.Value, e.Desc)
	}
	b.WriteString("\n")

	if len(g.Config) > 0 {
		b.WriteString("### Configurable rules\n\n")
		for _, c := range g.Config {
			fmt.Fprintf(b, "- **%s** — %s\n", c.Value, c.Desc)
		}
		b.WriteString("\n")
	}

	b.WriteString("### Example\n\n")
	fmt.Fprintf(b, "```python\n%s\n```\n\n", g.Example.Python)
	fmt.Fprintf(b, "```javascript\n%s\n```\n\n", g.Example.JS)

	if len(g.Notes) > 0 {
		b.WriteString("### Good to know\n\n")
		for _, n := range g.Notes {
			fmt.Fprintf(b, "- %s\n", n)
		}
		b.WriteString("\n")
	}
}

func fields(b *strings.Builder, fs []gamespec.Field) {
	b.WriteString("| Field | Type | Meaning |\n| --- | --- | --- |\n")
	for _, f := range fs {
		fmt.Fprintf(b, "| `%s` | %s | %s |\n", f.Name, f.Type, f.Meaning)
	}
	b.WriteString("\n")
}

func phaseList(phases []string) string {
	if len(phases) == 0 {
		return "—"
	}
	out := make([]string, len(phases))
	for i, p := range phases {
		out[i] = "`" + p + "`"
	}
	return strings.Join(out, ", ")
}
