// Command labprobe asks one model for one move, through the real labagent path.
//
// A certification run is thousands of matches. If a model cannot emit the move tool call in a
// shape movebind.Extract reads, every decision becomes the engine's fallback and the run
// measures the fallback rather than the model — at full token cost. One request per candidate
// is the cheapest possible way to find that out.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/labagent"
	"github.com/agent-arena/arena/internal/ladder"
	"github.com/agent-arena/arena/internal/remoteplay"
)

func main() {
	base := flag.String("base-url", "https://openrouter.ai/api/v1", "")
	models := flag.String("models", "", "comma-separated model ids")
	flag.Parse()
	key := os.Getenv("LAB_API_KEY")
	if key == "" {
		fmt.Println("LAB_API_KEY not set")
		os.Exit(1)
	}
	view := remoteplay.GoofspielView{
		Game: "goofspiel", Round: 1, CurrentPrize: 3, PrizePool: 3,
		YourHand: []int{1, 2, 3, 4, 5}, LegalActions: []int{1, 2, 3, 4, 5},
	}
	for _, m := range strings.Split(*models, ",") {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		src := labagent.NewLLMSource(labagent.Model{Model: m, BaseURL: *base, APIKey: key})
		d, err := src.Decider(context.Background(), "probe", ladder.DefaultSpec())
		if err != nil {
			fmt.Printf("  %-50s SETUP FAIL: %v\n", m, err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		start := time.Now()
		card, err := d.Decide(ctx, view)
		cancel()
		took := time.Since(start).Round(time.Millisecond)
		switch {
		case err != nil:
			fmt.Printf("  %-50s UNUSABLE (%s): %v\n", m, took, truncate(err.Error(), 80))
		case card < 1 || card > 5:
			fmt.Printf("  %-50s ILLEGAL card %d (%s)\n", m, card, took)
		default:
			fmt.Printf("  %-50s OK card=%d (%s)\n", m, card, took)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
