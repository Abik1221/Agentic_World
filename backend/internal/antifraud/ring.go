package antifraud

import "sort"

// Ring is a detected collusion ring: a closed group of agents (each entangled
// with at least two others) whose coins funnel toward a single "sink" agent.
// Unlike the pairwise IsColluding test, ring detection catches 3+ account rings
// that rotate/feed a winner while each individual head-to-head stays under the
// pairwise ban threshold.
type Ring struct {
	Agents []string `json:"agents"` // sorted member ids
	Sink   string   `json:"sink"`   // agent the coins concentrate toward
	Score  float64  `json:"score"`  // sink's share of in-ring net winnings, 0..1
}

const (
	ringMinMembers   = 3           // a ring is 3+ agents (pairs are handled by IsColluding)
	ringEdgeWinR     = 0.70        // looser than the pairwise ban (0.85): rings hide below it
	ringMinEdgeGames = minPairGames // an edge needs a long-enough series
	ringSinkShare    = 0.60        // a sink must capture ≥60% of the ring's net winnings
)

type ringEdge struct {
	a, b     string
	flowAToB int64 // net coins that moved A→B (positive ⇒ B is the net winner)
}

// DetectRings finds collusion rings from pairwise head-to-head records. It is pure
// and deliberately conservative (flags drive a hold + human review, never an
// automated ban):
//
//  1. An EDGE links two agents only when they played a long, lopsided series.
//  2. It keeps the 2-CORE — iteratively dropping any agent entangled with fewer
//     than two others. This is the key false-positive guard: a single strong
//     player beating many distinct opponents forms a star (leaves have degree 1)
//     and dissolves, while a coordinated ring (members also play each other)
//     survives.
//  3. A surviving connected component of ≥3 whose net coin flow CONCENTRATES on
//     one sink (≥ ringSinkShare) is reported as a ring.
func DetectRings(pairs []Pair) []Ring {
	// 1. Suspicious edges + adjacency.
	var edges []ringEdge
	adj := map[string]map[string]bool{}
	link := func(x, y string) {
		if adj[x] == nil {
			adj[x] = map[string]bool{}
		}
		adj[x][y] = true
	}
	for _, p := range pairs {
		total := p.AWins + p.BWins
		if p.Games < ringMinEdgeGames || total == 0 {
			continue
		}
		hi := p.AWins
		if p.BWins > hi {
			hi = p.BWins
		}
		if float64(hi)/float64(total) < ringEdgeWinR {
			continue
		}
		edges = append(edges, ringEdge{a: p.A, b: p.B, flowAToB: p.NetFlowAToB})
		link(p.A, p.B)
		link(p.B, p.A)
	}

	// 2. 2-core: drop nodes with surviving degree < 2 until a fixpoint.
	removed := map[string]bool{}
	for {
		changed := false
		for n, nbrs := range adj {
			if removed[n] {
				continue
			}
			deg := 0
			for m := range nbrs {
				if !removed[m] {
					deg++
				}
			}
			if deg < 2 {
				removed[n] = true
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	// 3. Connected components over the surviving 2-core (BFS).
	seen := map[string]bool{}
	var rings []Ring
	for start := range adj {
		if removed[start] || seen[start] {
			continue
		}
		var comp []string
		queue := []string{start}
		seen[start] = true
		for len(queue) > 0 {
			n := queue[0]
			queue = queue[1:]
			comp = append(comp, n)
			for m := range adj[n] {
				if removed[m] || seen[m] {
					continue
				}
				seen[m] = true
				queue = append(queue, m)
			}
		}
		if len(comp) < ringMinMembers {
			continue
		}

		// 4. In-component net coin flow → find the sink and its share.
		inComp := make(map[string]bool, len(comp))
		for _, n := range comp {
			inComp[n] = true
		}
		net := map[string]int64{}
		for _, e := range edges {
			if !inComp[e.a] || !inComp[e.b] {
				continue
			}
			net[e.b] += e.flowAToB
			net[e.a] -= e.flowAToB
		}
		var totalPos, sinkVal int64
		sink := ""
		for ag, v := range net {
			if v > 0 {
				totalPos += v
			}
			if v > sinkVal {
				sinkVal, sink = v, ag
			}
		}
		if totalPos <= 0 || sink == "" {
			continue
		}
		if share := float64(sinkVal) / float64(totalPos); share >= ringSinkShare {
			sort.Strings(comp)
			rings = append(rings, Ring{Agents: comp, Sink: sink, Score: share})
		}
	}
	sort.Slice(rings, func(i, j int) bool { return rings[i].Agents[0] < rings[j].Agents[0] })
	return rings
}
