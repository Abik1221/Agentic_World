package commentary_test

import (
	"strings"
	"testing"

	"github.com/agent-arena/arena/internal/commentary"
)

func TestLineIsDeterministic(t *testing.T) {
	r := commentary.Round{Round: 5, TotalRounds: 13, Prize: 7, PrizePool: 7, CardA: 9, CardB: 4, Winner: 0, ScoreA: 20, ScoreB: 11}
	if commentary.Line(r) != commentary.Line(r) {
		t.Fatal("Line must be deterministic for the same round")
	}
}

func TestLinePatterns(t *testing.T) {
	cases := []struct {
		name string
		r    commentary.Round
		want string // substring the line must contain
	}{
		{"tie carries", commentary.Round{Round: 2, TotalRounds: 13, Prize: 6, PrizePool: 6, CardA: 5, CardB: 5, Winner: commentary.Tie, ScoreA: 3, ScoreB: 3}, "carries over"},
		{"big card small prize", commentary.Round{Round: 3, TotalRounds: 13, Prize: 2, PrizePool: 2, CardA: 13, CardB: 1, Winner: 0, ScoreA: 2, ScoreB: 0}, "torches"},
		{"big pot", commentary.Round{Round: 6, TotalRounds: 13, Prize: 9, PrizePool: 24, CardA: 8, CardB: 6, Winner: 0, ScoreA: 30, ScoreB: 10}, "massive"},
		{"to the wire", commentary.Round{Round: 12, TotalRounds: 13, Prize: 4, PrizePool: 4, CardA: 7, CardB: 6, Winner: 0, ScoreA: 40, ScoreB: 39}, "wire"},
		{"plain", commentary.Round{Round: 4, TotalRounds: 13, Prize: 5, PrizePool: 5, CardA: 6, CardB: 3, Winner: 0, ScoreA: 12, ScoreB: 7}, "takes the"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commentary.Line(tc.r)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("Line = %q, want substring %q", got, tc.want)
			}
		})
	}
}

func TestDramaticFlags(t *testing.T) {
	carried := commentary.Round{Round: 2, TotalRounds: 13, Prize: 6, PrizePool: 12, CardA: 8, CardB: 5, Winner: 0, ScoreA: 12, ScoreB: 4}
	if !commentary.Dramatic(carried) {
		t.Fatal("a carried pot should be dramatic")
	}
	dull := commentary.Round{Round: 4, TotalRounds: 13, Prize: 5, PrizePool: 5, CardA: 6, CardB: 3, Winner: 0, ScoreA: 12, ScoreB: 7}
	if commentary.Dramatic(dull) {
		t.Fatal("an ordinary round should not be dramatic")
	}
}
