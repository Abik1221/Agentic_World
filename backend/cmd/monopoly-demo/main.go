// Command monopoly-demo runs the Monopoly sandbox as a dedicated, runnable
// environment. By default it seats bot agents in every chair and plays a full
// game to completion, printing a transcript and final standings — proof that the
// pure engine + Table runtime actually play the game.
//
// With -interactive and -human, you take one seat and the sandbox plays the rest,
// so a single user gets a complete multiplayer match against a simulated table.
//
//	go run ./cmd/monopoly-demo                       # watch 4 bots play
//	go run ./cmd/monopoly-demo -players 6 -seed foo  # 6-bot game, fixed seed
//	go run ./cmd/monopoly-demo -interactive -human 0 # play seat 0 yourself
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/agent-arena/arena/internal/engine/monopoly"
)

func main() {
	players := flag.Int("players", 4, "number of players (2-8)")
	seedStr := flag.String("seed", "demo", "match seed (any string)")
	maxTurns := flag.Int("maxturns", 1000, "hard cap on dice rolls before a net-worth finish")
	human := flag.Int("human", -1, "seat index you control (-1 = all bots)")
	interactive := flag.Bool("interactive", false, "play your -human seat from the keyboard")
	verbose := flag.Bool("verbose", false, "print every event, not just the highlights")
	flag.Parse()

	cfg := monopoly.Config{Players: *players, StartingCash: 1500, MaxTurns: *maxTurns}
	seed := []byte(*seedStr)

	agents := make([]monopoly.Agent, *players)
	for i := range agents {
		agents[i] = monopoly.NewBot(botName(i), monopoly.DefaultStyles[i%len(monopoly.DefaultStyles)], seed, i)
	}
	if *human >= 0 && *human < *players {
		agents[*human] = nil // your seat
	}

	tbl := monopoly.NewTable(cfg, seed, agents)
	board := monopoly.Board()

	fmt.Printf("=== Monopoly sandbox — %d players, seed %q ===\n", *players, *seedStr)
	for s := 0; s < *players; s++ {
		fmt.Printf("  seat %d: %s\n", s, tbl.SeatName(s))
	}
	fmt.Println()

	logged := 0
	flush := func() {
		full := tbl.Log()
		for ; logged < len(full); logged++ {
			if line := describe(full[logged], tbl, board, *verbose); line != "" {
				fmt.Println(line)
			}
		}
	}

	if *interactive && *human >= 0 {
		playInteractive(tbl, board, *human, flush)
	} else {
		tbl.PlayOut()
		flush()
	}

	printStandings(tbl, board)
}

// playInteractive runs the table, pausing for keyboard input on the human seat.
// Besides the game actions it understands a few meta-commands: help/?, state,
// auto (hand your seat to the sandbox), and quit/q.
func playInteractive(tbl *monopoly.Table, board []monopoly.Space, human int, flush func()) {
	in := bufio.NewScanner(os.Stdin)
	fmt.Println("(type 'help' for commands, 'auto' to let the sandbox finish for you, 'quit' to exit)")
	for !tbl.Finished() {
		tbl.AdvanceBots()
		flush()
		if tbl.Finished() {
			break
		}
		seat, isHuman := tbl.PendingSeat()
		if !isHuman {
			continue
		}
		p := tbl.State().Players[seat]
		fmt.Printf("\n[your turn — seat %d] cash $%d, at %s\n", seat, p.Cash, board[p.Position].Name)
		fmt.Printf("legal: %s\n> ", strings.Join(tbl.LegalActions(seat), ", "))

		if !in.Scan() {
			fmt.Println("\n(input closed; auto-playing the rest)")
			tbl.PlayOut()
			flush()
			return
		}
		text := strings.TrimSpace(in.Text())

		switch text {
		case "": // bare Enter — just re-prompt, no noise
			continue
		case "q", "quit", "exit":
			fmt.Println("(quitting — final standings below)")
			return
		case "help", "?":
			printHelp()
			continue
		case "auto":
			fmt.Println("(handing your seat to the sandbox...)")
			tbl.PlayOut()
			flush()
			return
		case "state", "board":
			printSeatState(tbl, board, seat)
			continue
		}

		act, ok := parseAction(text)
		if !ok {
			fmt.Println("unknown command — type 'help' for options")
			continue
		}
		if err := tbl.Apply(seat, act); err != nil {
			fmt.Printf("rejected: %v\n", err)
		}
		flush()
	}
}

func printHelp() {
	fmt.Println("commands:")
	fmt.Println("  roll                      roll the dice")
	fmt.Println("  buy | decline             when you land on an unowned property")
	fmt.Println("  bid <amt> | pass          during an auction")
	fmt.Println("  build <pos> | sell <pos>  add/remove a house (pos = board index 0-39)")
	fmt.Println("  mortgage <pos> | unmortgage <pos>")
	fmt.Println("  pay | card | rolljail     to get out of jail")
	fmt.Println("  bankrupt                  give up when you cannot pay")
	fmt.Println("  end                       end your turn")
	fmt.Println("  state | auto | quit")
}

func printSeatState(tbl *monopoly.Table, board []monopoly.Space, seat int) {
	s := tbl.State()
	p := s.Players[seat]
	fmt.Printf("  you: cash $%d, net worth $%d, at %s (idx %d)\n", p.Cash, s.NetWorth(seat), board[p.Position].Name, p.Position)
	var owned []string
	for idx := 0; idx < len(s.Holdings); idx++ {
		if s.Holdings[idx].Owner != seat {
			continue
		}
		tag := fmt.Sprintf("%d:%s", idx, board[idx].Name)
		if h := s.Holdings[idx]; h.Houses == 5 {
			tag += "[hotel]"
		} else if h.Houses > 0 {
			tag += fmt.Sprintf("[%dh]", h.Houses)
		}
		owned = append(owned, tag)
	}
	if len(owned) == 0 {
		fmt.Println("  you own nothing yet")
	} else {
		fmt.Printf("  you own: %s\n", strings.Join(owned, ", "))
	}
}

func parseAction(line string) (monopoly.Action, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return monopoly.Action{}, false
	}
	arg := func() int {
		if len(fields) > 1 {
			n, _ := strconv.Atoi(fields[1])
			return n
		}
		return 0
	}
	switch fields[0] {
	case "roll":
		return monopoly.Action{Kind: monopoly.ActRoll}, true
	case "buy":
		return monopoly.Action{Kind: monopoly.ActBuy}, true
	case "decline":
		return monopoly.Action{Kind: monopoly.ActDecline}, true
	case "bid":
		return monopoly.Action{Kind: monopoly.ActBid, Amount: arg()}, true
	case "pass":
		return monopoly.Action{Kind: monopoly.ActPass}, true
	case "build":
		return monopoly.Action{Kind: monopoly.ActBuild, Property: arg()}, true
	case "sell":
		return monopoly.Action{Kind: monopoly.ActSellHouse, Property: arg()}, true
	case "mortgage":
		return monopoly.Action{Kind: monopoly.ActMortgage, Property: arg()}, true
	case "unmortgage":
		return monopoly.Action{Kind: monopoly.ActUnmortgage, Property: arg()}, true
	case "pay":
		return monopoly.Action{Kind: monopoly.ActPayJail}, true
	case "card":
		return monopoly.Action{Kind: monopoly.ActUseJailCard}, true
	case "rolljail":
		return monopoly.Action{Kind: monopoly.ActRollJail}, true
	case "bankrupt":
		return monopoly.Action{Kind: monopoly.ActBankrupt}, true
	case "end":
		return monopoly.Action{Kind: monopoly.ActEndTurn}, true
	}
	return monopoly.Action{}, false
}

// describe renders one event as a human-readable transcript line. With verbose
// off, only the dramatic moments are printed.
func describe(ev monopoly.Event, tbl *monopoly.Table, board []monopoly.Space, verbose bool) string {
	name := func(seat int) string { return tbl.SeatName(seat) }
	prop := func(i int) string {
		if i >= 0 && i < len(board) {
			return board[i].Name
		}
		return "?"
	}
	switch p := ev.Payload.(type) {
	case monopoly.PropertyPurchasedPayload:
		return fmt.Sprintf("  %s buys %s for $%d", name(p.Seat), prop(p.Property), p.Price)
	case monopoly.RentPaidPayload:
		where := ""
		if p.Property >= 0 {
			where = " for " + prop(p.Property)
		}
		return fmt.Sprintf("  %s pays %s $%d%s", name(p.From), name(p.To), p.Amount, where)
	case monopoly.WentToJailPayload:
		return fmt.Sprintf("  %s goes to jail (%s)", name(p.Seat), p.Reason)
	case monopoly.AuctionResultPayload:
		if p.Seat == monopoly.Bank {
			return fmt.Sprintf("  %s went unsold at auction", prop(p.Property))
		}
		return fmt.Sprintf("  %s wins %s at auction for $%d", name(p.Seat), prop(p.Property), p.Amount)
	case monopoly.BuildPayload:
		kind := "house"
		if p.Houses == 5 {
			kind = "a hotel"
		}
		return fmt.Sprintf("  %s builds on %s (now %d %s)", name(p.Seat), prop(p.Property), p.Houses, kind)
	case monopoly.BankruptPayload:
		to := "the bank"
		if p.Creditor != monopoly.Bank {
			to = name(p.Creditor)
		}
		return fmt.Sprintf("  ** %s is BANKRUPT (estate to %s) **", name(p.Seat), to)
	case monopoly.MatchFinishedPayload:
		if p.Winner == monopoly.Tie {
			return "  === Match over: a tie ==="
		}
		return fmt.Sprintf("  === Match over: %s WINS ===", name(p.Winner))
	case monopoly.CardDrawnPayload:
		if verbose {
			return fmt.Sprintf("  %s draws (%s): %s", name(p.Seat), p.Deck, p.Text)
		}
	case monopoly.DiceRolledPayload:
		if verbose {
			d := ""
			if p.Doubles {
				d = " (doubles!)"
			}
			return fmt.Sprintf("  %s rolls %d+%d=%d%s", name(p.Seat), p.Die1, p.Die2, p.Total, d)
		}
	}
	return ""
}

func printStandings(tbl *monopoly.Table, board []monopoly.Space) {
	s := tbl.State()
	fmt.Printf("\n--- Final standings (after %d rolls) ---\n", s.TurnCount)
	for seat := 0; seat < len(s.Players); seat++ {
		p := s.Players[seat]
		status := fmt.Sprintf("$%d cash, net worth $%d", p.Cash, s.NetWorth(seat))
		if p.Bankrupt {
			status = "BANKRUPT"
		}
		marker := " "
		if tbl.Winner() == seat {
			marker = "*"
		}
		var owned []string
		for idx := 0; idx < len(s.Holdings); idx++ {
			h := s.Holdings[idx]
			if h.Owner != seat {
				continue
			}
			tag := board[idx].Name
			switch {
			case h.Houses == 5:
				tag += " [hotel]"
			case h.Houses > 0:
				tag += fmt.Sprintf(" [%dh]", h.Houses)
			}
			if h.Mortgaged {
				tag += " (mortgaged)"
			}
			owned = append(owned, tag)
		}
		fmt.Printf("%s seat %d %-9s — %s\n", marker, seat, tbl.SeatName(seat), status)
		if len(owned) > 0 {
			fmt.Printf("      owns: %s\n", strings.Join(owned, ", "))
		}
	}
}

func botName(seat int) string {
	names := []string{"Athena", "Borg", "Cleo", "Dax", "Echo", "Foxtrot", "Gizmo", "Helix"}
	if seat < len(names) {
		return names[seat]
	}
	return "Bot" + strconv.Itoa(seat)
}
