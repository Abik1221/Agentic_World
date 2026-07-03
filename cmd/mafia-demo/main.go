// Command mafia-demo runs the Mafia sandbox as a dedicated, runnable environment.
// By default it seats deterministic bots in all 12 chairs and plays a full game to
// a team victory, printing the public transcript and a final role reveal.
//
// With -interactive and -human you take one seat (the sandbox plays the rest, each
// bot seeing only its own redacted view), so a single user gets a complete team
// match of Town vs Mafia.
//
//	go run ./cmd/mafia-demo                        # watch 12 bots play
//	go run ./cmd/mafia-demo -seed foo -verbose     # include night/role detail
//	go run ./cmd/mafia-demo -interactive -human 1  # play seat 1 yourself
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/agent-arena/arena/internal/engine/mafia"
)

func main() {
	seedStr := flag.String("seed", "demo", "match seed (any string)")
	maxDays := flag.Int("maxdays", 60, "day cap before a majority decision")
	human := flag.Int("human", -1, "seat you control (1-12, or -1 for all bots)")
	interactive := flag.Bool("interactive", false, "play your -human seat from the keyboard")
	verbose := flag.Bool("verbose", false, "also print phase headers and your own night results")
	flag.Parse()

	seed := []byte(*seedStr)
	seats := mafia.StandardSeats()

	var agents map[int]mafia.Agent
	if *human >= 1 {
		agents = map[int]mafia.Agent{}
		for _, seat := range seats {
			if seat != *human {
				agents[seat] = mafia.NewBot(botName(seat), seed, seat)
			}
		}
	}
	tbl := mafia.NewTable(seats, seed, agents, *maxDays)

	fmt.Printf("=== Mafia sandbox — 12 seats, seed %q ===\n", *seedStr)
	fmt.Println("Roles are hidden. Town must vote out all Mafia; Mafia must reach parity.")
	fmt.Println()

	logged := 0
	flush := func() {
		full := tbl.Log()
		for ; logged < len(full); logged++ {
			if line := describe(full[logged], tbl, *verbose); line != "" {
				fmt.Println(line)
			}
		}
	}

	if *interactive && *human >= 1 {
		playInteractive(tbl, *human, flush, *verbose)
	} else {
		tbl.PlayOut()
		flush()
	}

	printReveal(tbl)
}

func playInteractive(tbl *mafia.Table, human int, flush func(), verbose bool) {
	in := bufio.NewScanner(os.Stdin)
	fmt.Println("(type 'help' for commands, 'view' to see your role, 'auto' to finish, 'quit' to exit)")
	for !tbl.Finished() {
		tbl.AdvanceBots()
		flush()
		if tbl.Finished() {
			break
		}
		if ok, _ := pendingHuman(tbl, human); !ok {
			continue
		}
		v := tbl.ViewFor(human)
		fmt.Printf("\n[your turn — seat %d, %s/%s, day %d %s]\n", human, v.Role, v.Team, v.Day, v.Phase)
		fmt.Printf("legal: %s\n> ", strings.Join(tbl.LegalActions(human), ", "))
		if !in.Scan() {
			fmt.Println("\n(input closed; auto-playing the rest)")
			tbl.PlayOut()
			flush()
			return
		}
		text := strings.TrimSpace(in.Text())
		switch text {
		case "":
			continue
		case "q", "quit", "exit":
			fmt.Println("(quitting — reveal below)")
			return
		case "help", "?":
			printHelp()
			continue
		case "view":
			printView(tbl.ViewFor(human))
			continue
		case "auto":
			fmt.Println("(handing your seat to the sandbox...)")
			tbl.PlayOut()
			flush()
			return
		}
		act, ok := parseAction(text)
		if !ok {
			fmt.Println("unknown command — type 'help'")
			continue
		}
		if err := tbl.Apply(human, act); err != nil {
			fmt.Printf("rejected: %v\n", err)
		}
		flush()
	}
}

// pendingHuman reports whether the human seat is the one being waited on.
func pendingHuman(tbl *mafia.Table, human int) (bool, int) {
	for _, seat := range tbl.PendingActors() {
		if seat == human {
			return true, seat
		}
	}
	return false, 0
}

func parseAction(line string) (mafia.Action, bool) {
	f := strings.Fields(line)
	if len(f) == 0 {
		return mafia.Action{}, false
	}
	n := func() int {
		if len(f) > 1 {
			v, _ := strconv.Atoi(f[1])
			return v
		}
		return 0
	}
	switch f[0] {
	case "kill":
		return mafia.Action{Kind: mafia.ActNightKill, Target: n()}, true
	case "investigate":
		return mafia.Action{Kind: mafia.ActInvestigate, Target: n()}, true
	case "protect":
		return mafia.Action{Kind: mafia.ActProtect, Target: n()}, true
	case "profile":
		return mafia.Action{Kind: mafia.ActProfile, Target: n()}, true
	case "vote":
		return mafia.Action{Kind: mafia.ActVote, Target: n()}, true
	case "say":
		return mafia.Action{Kind: mafia.ActMessage, Tone: "info", Text: strings.Join(f[1:], " ")}, true
	}
	return mafia.Action{}, false
}

func describe(ev mafia.Event, tbl *mafia.Table, verbose bool) string {
	name := func(seat int) string { return fmt.Sprintf("seat %d (%s)", seat, tbl.SeatName(seat)) }
	switch p := ev.Payload.(type) {
	case mafia.PhasePayload:
		if verbose {
			return fmt.Sprintf("\n— Day %d: %s —", p.Day, strings.ToUpper(p.Phase))
		}
	case mafia.ModeratorPayload:
		return "  [mod] " + p.Text
	case mafia.MessagePayload:
		return "  " + name(p.From) + ": " + p.Text
	case mafia.VotePayload:
		return fmt.Sprintf("  %s votes %s", name(p.From), name(p.Target))
	case mafia.EliminatePayload:
		return fmt.Sprintf("  ** %s eliminated (%s) **", name(p.Target), p.Cause)
	case mafia.VictoryPayload:
		return fmt.Sprintf("\n=== %s WINS ===", strings.ToUpper(p.Team))
	case mafia.NightPayload:
		if verbose {
			return fmt.Sprintf("  (night) %s — %s [%s]", p.Actor, p.Text, p.Secret)
		}
	}
	return ""
}

func printReveal(tbl *mafia.Table) {
	s := tbl.State()
	fmt.Printf("\n--- Role reveal (winner: %s) ---\n", strings.ToUpper(tbl.Winner()))
	for _, seat := range tbl.Seats() {
		status := "alive"
		if !s.Alive[seat] {
			status = "dead"
		}
		fmt.Printf("  seat %2d  %-8s  %-10s  %s\n", seat, tbl.SeatName(seat), s.Roles[seat], status)
	}
}

func printView(v mafia.AgentView) {
	fmt.Printf("  you are seat %d: %s (%s), day %d, phase %s\n", v.Seat, v.Role, v.Team, v.Day, v.Phase)
	if len(v.Allies) > 0 {
		fmt.Printf("  your fellow mafia: %v\n", v.Allies)
	}
	var alive []int
	for seat, ok := range v.Alive {
		if ok {
			alive = append(alive, seat)
		}
	}
	fmt.Printf("  alive seats: %v\n", alive)
	if len(v.Private) > 0 {
		fmt.Println("  your private results:")
		for _, ev := range v.Private {
			if np, ok := ev.Payload.(mafia.NightPayload); ok {
				fmt.Printf("    - %s\n", np.Secret)
			}
		}
	}
}

func printHelp() {
	fmt.Println("commands (targets are seat numbers 1-12):")
	fmt.Println("  kill N | investigate N | protect N | profile N   (your night action)")
	fmt.Println("  say <text>                                       (discussion)")
	fmt.Println("  vote N                                           (voting)")
	fmt.Println("  view | auto | quit")
}

func botName(seat int) string {
	names := []string{"Athena", "Borg", "Cleo", "Dax", "Echo", "Foxtrot", "Gizmo", "Helix", "Iris", "Juno", "Kilo", "Lyra"}
	if seat-1 >= 0 && seat-1 < len(names) {
		return names[seat-1]
	}
	return "Bot" + strconv.Itoa(seat)
}
