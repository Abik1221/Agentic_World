package antifraud

import "testing"

// pair builds a head-to-head where `winner` takes `winRate` of `games` and the
// loser's coins flow to the winner (so the winner is the net receiver).
func pair(a, b string, games int, winner string, winRate float64, flow int64) Pair {
	wins := int(float64(games) * winRate)
	p := Pair{A: a, B: b, Games: games}
	if winner == a {
		p.AWins, p.BWins = wins, games-wins
		p.NetFlowAToB = -flow // coins flow B→A ⇒ negative A→B
	} else {
		p.BWins, p.AWins = wins, games-wins
		p.NetFlowAToB = flow // coins flow A→B
	}
	return p
}

// A closed triangle (S,X,Y all play each other) funnelling coins to S is a ring:
// every node has degree 2 (survives the 2-core) and the sink captures the flow.
func TestDetectRings_FunnelTriangleFlagged(t *testing.T) {
	pairs := []Pair{
		pair("S", "X", 10, "S", 0.8, 1000), // X feeds S
		pair("S", "Y", 10, "S", 0.8, 1000), // Y feeds S
		pair("X", "Y", 10, "X", 0.8, 100),  // X and Y also play (closes the ring)
	}
	rings := DetectRings(pairs)
	if len(rings) != 1 {
		t.Fatalf("want 1 ring, got %d: %+v", len(rings), rings)
	}
	r := rings[0]
	if r.Sink != "S" {
		t.Fatalf("sink = %q, want S", r.Sink)
	}
	if len(r.Agents) != 3 {
		t.Fatalf("ring size = %d, want 3 (%v)", len(r.Agents), r.Agents)
	}
	if r.Score < ringSinkShare {
		t.Fatalf("score = %.2f, want ≥ %.2f", r.Score, ringSinkShare)
	}
}

// A single strong player beating many DISTINCT one-off opponents forms a star:
// the leaves have degree 1 and dissolve in the 2-core, so it is NOT flagged.
// This is the critical false-positive guard — skill must not read as collusion.
func TestDetectRings_StrongPlayerStarNotFlagged(t *testing.T) {
	pairs := []Pair{
		pair("Pro", "a", 10, "Pro", 0.9, 1000),
		pair("Pro", "b", 10, "Pro", 0.9, 1000),
		pair("Pro", "c", 10, "Pro", 0.9, 1000),
		pair("Pro", "d", 10, "Pro", 0.9, 1000),
	}
	if rings := DetectRings(pairs); len(rings) != 0 {
		t.Fatalf("a strong player's star must not be flagged; got %+v", rings)
	}
}

// A closed triangle with balanced (non-concentrated) coin flow is competition,
// not a funnel — no single sink dominates, so it is NOT flagged.
func TestDetectRings_NoConcentrationNotFlagged(t *testing.T) {
	pairs := []Pair{
		pair("A", "B", 10, "A", 0.75, 500),
		pair("B", "C", 10, "B", 0.75, 500),
		pair("C", "A", 10, "C", 0.75, 500), // rotates evenly → no sink
	}
	for _, r := range DetectRings(pairs) {
		if r.Score >= ringSinkShare {
			t.Fatalf("evenly-rotating triangle should not concentrate; got score %.2f", r.Score)
		}
	}
}

// Short or even series produce no edges and therefore no rings.
func TestDetectRings_BelowThresholdsIgnored(t *testing.T) {
	pairs := []Pair{
		pair("A", "B", 3, "A", 1.0, 1000),  // too few games
		pair("B", "C", 10, "B", 0.55, 100), // not lopsided enough
		pair("C", "A", 10, "C", 0.55, 100),
	}
	if rings := DetectRings(pairs); len(rings) != 0 {
		t.Fatalf("want no rings below thresholds, got %+v", rings)
	}
}
