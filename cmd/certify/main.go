// Command certify runs the Sandbox Certification pipeline from the developer
// platform spec against a real game engine. It seats reference agents, plays the
// three automated certification matches, and prints the validation report.
//
//	go run ./cmd/certify -list                       # list supported games
//	go run ./cmd/certify -game monopoly -agent me    # certify one game
//	go run ./cmd/certify -all -agent me              # certify all three games
//	go run ./cmd/certify -all -json                  # machine-readable report
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/agent-arena/arena/internal/devplatform"
)

func main() {
	game := flag.String("game", "", "game id to certify (goofspiel|mafia|monopoly)")
	agent := flag.String("agent", "candidate-v1", "logical agent/version id being certified")
	all := flag.Bool("all", false, "certify every supported game")
	list := flag.Bool("list", false, "list supported games and exit")
	asJSON := flag.Bool("json", false, "emit the report(s) as JSON")
	flag.Parse()

	reg := devplatform.DefaultRegistry()
	cert := devplatform.NewCertifier(reg)

	if *list {
		for _, g := range reg.All() {
			fmt.Printf("%-10s %-9s seats %d-%d  info=%s  engine=%s\n  %s\n",
				g.ID, g.Name, g.MinSeats, g.MaxSeats, g.Info, g.EngineVersion, g.Summary)
		}
		return
	}

	var targets []devplatform.GameID
	switch {
	case *all:
		targets = reg.IDs()
	case *game != "":
		targets = []devplatform.GameID{devplatform.GameID(*game)}
	default:
		fmt.Fprintln(os.Stderr, "specify -game <id>, -all, or -list")
		os.Exit(2)
	}

	reports := make([]devplatform.CertificationReport, 0, len(targets))
	allCertified := true
	for _, id := range targets {
		rep, err := cert.Certify(id, *agent)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
		reports = append(reports, rep)
		if !rep.Certified {
			allCertified = false
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(reports)
	} else {
		for _, rep := range reports {
			fmt.Print(rep.String())
		}
		fmt.Println("---")
		for _, rep := range reports {
			fmt.Println("  " + rep.Summary())
		}
	}

	if !allCertified {
		os.Exit(1)
	}
}
