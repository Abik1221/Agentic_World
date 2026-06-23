package antifraud_test

import (
	"testing"

	"github.com/agent-arena/arena/internal/antifraud"
)

func TestIsColludingLopsidedSeries(t *testing.T) {
	// A wins 9 of 10 and coins flow to A (NetFlowAToB negative) → collusion.
	ring := antifraud.Pair{A: "ag_a", B: "ag_b", Games: 10, AWins: 9, BWins: 1, NetFlowAToB: -400}
	if !antifraud.IsColluding(ring) {
		t.Fatal("expected a 9-1 coin-concentrated series to be flagged")
	}
	if antifraud.CollusionScore(ring) < 0.7 {
		t.Fatalf("collusion score too low: %v", antifraud.CollusionScore(ring))
	}
}

func TestCleanSeriesNotColluding(t *testing.T) {
	even := antifraud.Pair{A: "ag_a", B: "ag_b", Games: 10, AWins: 5, BWins: 5, NetFlowAToB: 0}
	if antifraud.IsColluding(even) {
		t.Fatal("an even series must not be flagged")
	}
	short := antifraud.Pair{A: "ag_a", B: "ag_b", Games: 2, AWins: 2, BWins: 0, NetFlowAToB: -100}
	if antifraud.IsColluding(short) {
		t.Fatal("too few games to flag")
	}
}

func TestHumanTimingDetected(t *testing.T) {
	human := antifraud.TimingStat{Count: 20, MeanMs: 2200, StdMs: 1100} // slow + variable
	if !antifraud.LooksHuman(human) {
		t.Fatal("slow, variable timing should look human")
	}
	if antifraud.HumanLikelihood(human) < 0.5 {
		t.Fatalf("human likelihood too low: %v", antifraud.HumanLikelihood(human))
	}
	bot := antifraud.TimingStat{Count: 50, MeanMs: 180, StdMs: 15} // fast + metronomic
	if antifraud.LooksHuman(bot) {
		t.Fatal("fast, consistent timing is a bot, not a human")
	}
}
