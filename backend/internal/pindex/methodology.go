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
