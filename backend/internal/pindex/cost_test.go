package pindex

import (
	"math"
	"testing"
)

func TestCostEfficiencyScore(t *testing.T) {
	const scale, cheap, expensive = 1000.0, 0.10, 2.00

	cases := []struct {
		name       string
		cost       float64
		wins       int
		wantApprox float64
	}{
		{"no wins → 0", 5.0, 0, 0},
		{"no cost → 0", 0, 3, 0},
		{"cheap per win → full", 0.30, 10, 1000}, // 0.03/win ≤ cheap
		{"expensive per win → 0", 20.0, 5, 0},    // 4.0/win ≥ expensive
		{"midpoint", 5.25, 5, 500},               // 1.05/win = midpoint of [0.10,2.00]
	}
	for _, c := range cases {
		got := CostEfficiencyScore(c.cost, c.wins, scale, cheap, expensive)
		if math.Abs(got-c.wantApprox) > 1.0 {
			t.Errorf("%s: CostEfficiencyScore(%v,%d) = %v, want ~%v", c.name, c.cost, c.wins, got, c.wantApprox)
		}
	}
}

func TestCostEfficiencyScore_DegenerateBand(t *testing.T) {
	if got := CostEfficiencyScore(0.5, 10, 1000, 0.10, 0.10); got != 1000 {
		t.Errorf("cost-per-win 0.05 ≤ cheap with degenerate band → full marks, got %v", got)
	}
	if got := CostEfficiencyScore(5.0, 1, 1000, 0.10, 0.10); got != 0 {
		t.Errorf("cost-per-win 5.0 > cheap with degenerate band → 0, got %v", got)
	}
}
