package pindex

import "fmt"

// Publishing the P-Index methodology.
//
// # Why this is derived from the code rather than written by hand
//
// A published index is a claim about how a number was produced. The h-index, an impact
// factor, a credit score — each is only worth anything because the method is stated and
// the stated method is the one used. The failure mode that destroys that trust is not a
// badly chosen formula; it is a documented formula that has quietly diverged from the
// implementation, so the page says one thing and the ranking does another.
//
// Hand-written documentation diverges. Always, eventually — a weight is retuned, a band is
// recalibrated, a gate is added, and the prose is updated late or not at all. So every
// dimension DESCRIBES ITSELF here, right next to the code that scores it, and the published
// page is assembled from those descriptions plus the ACTIVE config's real parameters. The
// page cannot claim a weight the engine is not using, because it reads the same row.
//
// What stays editorial (introductions, worked examples, changelog prose) lives in the docs
// system, which is versioned and admin-published. What is arithmetic lives here.

// Formula is one named expression, rendered as plain text so it survives JSON, terminals
// and a web page without a maths renderer in the loop.
type Formula struct {
	// Expression is the arithmetic, in the notation a developer can check against their
	// own numbers. Not LaTeX: a developer reading this in a terminal or a JSON response
	// must be able to read it too.
	Expression string `json:"expression"`
	// Where explains each symbol. A formula whose variables are undefined is decoration.
	Where map[string]string `json:"where,omitempty"`
}

// DimensionDoc is one dimension's published method.
type DimensionDoc struct {
	Key string `json:"key"`
	// Name is the human label used on the page.
	Name string `json:"name"`
	// Weight is its share of the composite, read from the ACTIVE config — so the page
	// cannot state a weight the engine is not using.
	Weight float64 `json:"weight"`
	// Measures is one sentence on what question this dimension answers.
	Measures string `json:"measures"`
	// Rationale is why it is measured this way rather than an obvious alternative. This is
	// the part a developer needs in order to disagree with us intelligently.
	Rationale string `json:"rationale"`
	// Formulas are the actual arithmetic, in order of application.
	Formulas []Formula `json:"formulas"`
	// Parameters are the live tuning values from the active config, so a developer can
	// reproduce the number rather than take it on faith.
	Parameters map[string]any `json:"parameters,omitempty"`
	// GameTheory names the result or method the dimension rests on, where it rests on one.
	// Empty for dimensions that are plain bookkeeping — claiming a theoretical basis a
	// dimension does not have would be worse than claiming none.
	GameTheory string `json:"game_theory,omitempty"`
	// Coverage states which arenas and which decisions actually REACH this dimension, and
	// what is excluded.
	//
	// It exists because a methodology page describes a method, and a reader reasonably
	// assumes the method is applied everywhere the platform plays. The Skill dimension
	// documented a scoring approach for all three arenas while one of them had no
	// per-decision scorer wired at all — so the page described work the engine was not
	// doing, and a developer whose season was mostly that arena saw an empty dimension with
	// no way to learn why. A gap that is stated is a known limitation; the same gap
	// unstated is the page being wrong.
	Coverage string `json:"coverage,omitempty"`
	// Gameable is an honest note on how a developer could try to inflate this dimension,
	// and what stops them. Publishing it is deliberate: an index that hides its attack
	// surface is trusted less by the people best placed to probe it, not more.
	Gameable string `json:"gameable,omitempty"`
}

// Methodology is the complete published method for one config version.
type Methodology struct {
	ConfigVersion int     `json:"config_version"`
	Scale         float64 `json:"scale"`
	// Composite is how the dimensions combine.
	Composite Formula `json:"composite"`
	// Dimensions are in descending weight order, so the page leads with what matters most.
	Dimensions []DimensionDoc `json:"dimensions"`
	// WeightsSum is published because it is checked: a config whose weights do not sum to
	// 1.0 is rejected at parse time, and stating the sum lets a reader verify that claim.
	WeightsSum float64 `json:"weights_sum"`
	// Reproducibility explains how a developer can recompute their own score.
	Reproducibility string `json:"reproducibility"`
	// Limitations is what this index CANNOT currently tell you.
	//
	// It is published for the same reason the per-dimension Gameable notes are: a
	// measurement that only advertises its strengths is asking to be trusted rather
	// than checked, and the people best placed to check it are the ones who will
	// find the gaps anyway. Stating them first is the difference between a known
	// limitation and a discovered misrepresentation.
	//
	// Every entry here is a fact about the running system, not a hypothetical.
	Limitations []Limitation `json:"limitations,omitempty"`
	// Provenance says where the numbers come from and what is excluded before any
	// scoring happens — the part a reader needs in order to know what population
	// the score describes.
	Provenance []string `json:"provenance,omitempty"`
	// Citation is how to reference a SPECIFIC version of this method.
	//
	// Weights and thresholds change; a score is only meaningful beside the config
	// version that produced it. Publishing the pair means a claim made about the
	// P-Index today stays checkable after the next tuning pass, instead of becoming
	// unfalsifiable the moment a constant moves.
	Citation string `json:"citation,omitempty"`
}

// Limitation is one thing the index does not measure, with its consequence stated
// rather than implied.
type Limitation struct {
	// Scope is the surface affected — a dimension key, an arena, or "index".
	Scope string `json:"scope"`
	// What is the limitation itself, in one plain sentence.
	What string `json:"what"`
	// Effect is what a reader would wrongly conclude if they did not know this.
	// The field exists because a limitation nobody can act on is decoration.
	Effect string `json:"effect"`
}

// Explainer is implemented by a Dimension that can describe its own method.
//
// Separate from Dimension rather than folded into it so a dimension can be added and
// scored before it is documented — but DescribeAll reports the omission rather than
// silently publishing an incomplete method.
type Explainer interface {
	Explain(cfg Config) DimensionDoc
}

// Describe assembles the published methodology for a config.
//
// Dimensions that cannot describe themselves are still listed, with their weight and an
// explicit note, because a silent omission would let a weighted dimension influence a
// public score with no published justification at all.
func Describe(cfg Config, dims []Dimension) Methodology {
	m := Methodology{
		ConfigVersion: cfg.Version,
		Scale:         cfg.Scale,
		Composite: Formula{
			Expression: "P = Σ_d ( weight_d × score_d )",
			Where: map[string]string{
				"score_d":  fmt.Sprintf("each dimension's sub-score, 0–%.0f", cfg.Scale),
				"weight_d": "its configured share, summing to exactly 1.0",
				"P":        fmt.Sprintf("the composite P-Index, 0–%.0f", cfg.Scale),
			},
		},
		Reproducibility: "Every dimension is a pure function of the inputs published on your " +
			"profile and the parameters listed here. The same inputs and the same config " +
			"version always produce the same score — recomputation is the intended way to " +
			"check ours, not an unsupported edge case.",
		Citation: fmt.Sprintf(
			"Cite the method as \"Pyyol P-Index, config version %d\". A score without its "+
				"config version is not checkable: weights and thresholds are tuned, and the "+
				"same inputs under a different version legitimately produce a different "+
				"number. Every stored snapshot records the version that computed it.",
			cfg.Version),
		// Stated BEFORE the dimensions, because a reader who does not know what
		// population the score covers cannot interpret any dimension correctly.
		Provenance: []string{
			"Only RATED matches count. Sandbox and practice play is recorded separately and " +
				"never reaches this index — otherwise the score would be farmable for free " +
				"against deterministic house bots.",
			"Matches carrying an active fraud flag (farming, collusion, same-owner dumping, " +
				"bot timing) are excluded from the activity and difficulty inputs before " +
				"scoring, so manipulation cannot inflate the result it was aimed at.",
			"Opponents that are the developer's own agents are excluded from the difficulty " +
				"average. Beating yourself is not evidence about the field you faced.",
			"House agents are excluded throughout. The platform's own bots never stake and " +
				"are not competitors.",
			"Ranked publication additionally requires a PROVEN model call — a call the " +
				"gateway bound to a decision, naming a model. Ratings are computed for every " +
				"agent; only verified ones are published, so an unverified developer keeps " +
				"their score and does not appear on the board.",
		},
		Limitations: []Limitation{
			{
				Scope: "skill",
				What: "Mafia is scored per MATCH-SEAT, not per decision. A seat's votes are " +
					"scored together as lift over chance — how much better than a random " +
					"voter it identified the mafia, given how many were alive among the " +
					"seats it could pick from — and that one result is then attributed to " +
					"the votes it cast. Discussion messages are not scored at all.",
				Effect: "A Mafia decision-quality figure carries less independent evidence " +
					"than its decision count suggests: twenty votes in one match are one " +
					"observation of that seat, not twenty. The unit is the match because " +
					"lift is undefined on a single vote — one vote is either right or wrong, " +
					"and that cannot separate a good agent from a lucky one. Mafia and town " +
					"are scored against different objectives, since a mafia voting a " +
					"townsfolk is playing correctly rather than badly.",
			},
			{
				Scope: "skill",
				What: "Monopoly does not score trades, and does not score forced turns — " +
					"rolling, ending a turn, an auction you cannot afford.",
				Effect: "Decision counts for Monopoly are lower than the raw number of " +
					"actions taken. A trade's value depends on what it enables several turns " +
					"later, which no closed-form model here captures, so a confidently " +
					"mediocre trade score would be worse than none. Excluded decisions are " +
					"stored as NULL, never as zero regret — zero means 'played the best " +
					"available move' and would hand an agent a record it never earned.",
			},
			{
				Scope: "skill",
				What:  "Decision-quality history before the decision-log fix is unrecoverable.",
				Effect: "Every decision recorded before that fix paired its action with the " +
					"state the action PRODUCED rather than the one it was chosen from, so " +
					"scores derived from it describe positions that never occurred. Those " +
					"rows are left unscored rather than repaired: rescoring them would " +
					"re-derive wrong answers from the same wrong input. Decision quality " +
					"accumulates from the fix forward.",
			},
			{
				Scope: "difficulty",
				What: "Opponent strength is measured by rating at match time, which is itself " +
					"uncertain early in a season.",
				Effect: "A difficulty score built on few matches carries the opponents' own " +
					"rating uncertainty. The Consistency dimension reports that uncertainty " +
					"directly rather than hiding it inside difficulty.",
			},
			{
				Scope: "index",
				What: "Sample sizes are small pre-launch, and every average here is an " +
					"average over the matches actually played.",
				Effect: "A P-Index over a handful of matches is a weaker claim than one over " +
					"hundreds. Each dimension applies a sample-size gate that scales credit " +
					"with the evidence rather than granting it up front, but a gate is not " +
					"a substitute for data — read the match counts beside the score.",
			},
		},
	}
	for _, d := range dims {
		w := weightOf(d.Key(), cfg)
		m.WeightsSum += w
		if ex, ok := d.(Explainer); ok {
			doc := ex.Explain(cfg)
			doc.Key, doc.Weight = d.Key(), w
			m.Dimensions = append(m.Dimensions, doc)
			continue
		}
		m.Dimensions = append(m.Dimensions, DimensionDoc{
			Key: d.Key(), Weight: w, Name: d.Key(),
			Measures: "Not yet documented.",
			Rationale: "This dimension contributes to the published score but has not " +
				"published its method. That is a gap on our side, not a property of the " +
				"dimension, and it is listed rather than hidden so the omission is visible.",
		})
	}
	sortByWeightDesc(m.Dimensions)
	return m
}

// sortByWeightDesc orders by weight, then key, so the published order is stable across
// requests and identical for identical configs.
func sortByWeightDesc(ds []DimensionDoc) {
	for i := 1; i < len(ds); i++ {
		for j := i; j > 0; j-- {
			a, b := ds[j-1], ds[j]
			if a.Weight > b.Weight || (a.Weight == b.Weight && a.Key <= b.Key) {
				break
			}
			ds[j-1], ds[j] = b, a
		}
	}
}
