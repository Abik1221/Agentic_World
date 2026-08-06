package pindex

import (
	"testing"
	"time"
)

func skillCfg(weight float64) Config {
	var c Config
	c.Scale = 1000
	c.Weights.Arena = 1 - weight
	c.Weights.Skill = weight
	c.Skill.MinDecisions = 100
	c.Skill.WBlunder = 0.5
	return c
}

// THE safety property for shipping this.
//
// The P-Index is public and drives reputation. Adding a dimension must not move a single
// developer's score until an operator deliberately weights it — otherwise the platform
// silently re-ranks everyone overnight on a scorer that has never been measured against
// live play. At weight 0 the dimension is computed, stored and shown, and contributes
// exactly nothing.
func TestSkillAtZeroWeightDoesNotMoveThePIndex(t *testing.T) {
	now := time.Now()
	in := DeveloperInputs{
		UserPublicID: "usr_1", Season: 18,
		Arenas:       []ArenaInput{{Game: "goofspiel", Rating: 2000, RD: 60, Algo: "glicko2", Matches: 40}},
		TotalMatches: 40, DistinctArenas: 1, AvgOppRating: 1800, AvgOppRatingOnWin: 1850,
		BenchDecisions: 400, LegalRate: 0.98, FallbackRate: 0.02, AvgLatencyMS: 900,
		LastMatchAt: now.Add(-24 * time.Hour), AsOf: now,
	}
	cfg := skillCfg(0)

	// Two developers, wildly different decision quality, everything else identical.
	weak := in
	weak.SkillDecisions, weak.SkillQuality, weak.SkillBlunderRate = 500, 0.10, 0.60
	strong := in
	strong.SkillDecisions, strong.SkillQuality, strong.SkillBlunderRate = 500, 0.95, 0.01

	e := NewEngine()
	if a, b := e.Compute(weak, cfg).PIndex, e.Compute(strong, cfg).PIndex; a != b {
		t.Fatalf("at weight 0 the skill dimension moved the P-Index: %.6f vs %.6f", a, b)
	}
	// …but it is still computed and visible, which is the point of shipping it inert.
	res := e.Compute(strong, cfg)
	if got := res.Sub(keySkill); got <= 0 {
		t.Fatalf("skill sub-score is %.2f — the dimension must be MEASURED while inert, or "+
			"there is no data to choose a weight from later", got)
	}
}

// Once weighted, it has to actually discriminate — a dimension that ranks a 95%-quality
// agent level with a 10%-quality one is decoration.
func TestSkillSeparatesGoodPlayFromBadOnceWeighted(t *testing.T) {
	cfg := skillCfg(0.3)
	base := DeveloperInputs{UserPublicID: "u", Season: 1, AsOf: time.Now()}

	weak := base
	weak.SkillDecisions, weak.SkillQuality, weak.SkillBlunderRate = 200, 0.20, 0.50
	strong := base
	strong.SkillDecisions, strong.SkillQuality, strong.SkillBlunderRate = 200, 0.90, 0.02

	e := NewEngine()
	ws, ss := e.Compute(weak, cfg).Sub(keySkill), e.Compute(strong, cfg).Sub(keySkill)
	if !(ss > ws) {
		t.Fatalf("strong play scored %.1f, weak play %.1f", ss, ws)
	}
}

// The mean hides thrown games. Two agents with identical mean quality are not equal if
// one of them periodically throws a round it had already won.
func TestBlundersArePenalisedBeyondMeanQuality(t *testing.T) {
	cfg := skillCfg(0.3)
	base := DeveloperInputs{UserPublicID: "u", Season: 1, AsOf: time.Now(), SkillDecisions: 300, SkillQuality: 0.70}

	steady := base
	steady.SkillBlunderRate = 0.00
	spiky := base
	spiky.SkillBlunderRate = 0.25 // same mean, but a quarter of decisions throw the value

	e := NewEngine()
	if a, b := e.Compute(steady, cfg).Sub(keySkill), e.Compute(spiky, cfg).Sub(keySkill); !(a > b) {
		t.Fatalf("steady play scored %.1f and blunder-prone play %.1f at identical mean "+
			"quality — the blunder rate is not being counted", a, b)
	}
}

// Sample size must gate the score, or three lucky decisions outrank a season of evidence.
func TestSkillIsGatedBySampleSize(t *testing.T) {
	cfg := skillCfg(0.3)
	base := DeveloperInputs{UserPublicID: "u", Season: 1, AsOf: time.Now(), SkillQuality: 1.0}

	thin := base
	thin.SkillDecisions = 3
	thick := base
	thick.SkillDecisions = 100 // == MinDecisions

	e := NewEngine()
	a, b := e.Compute(thin, cfg).Sub(keySkill), e.Compute(thick, cfg).Sub(keySkill)
	if !(b > a) {
		t.Fatalf("3 perfect decisions scored %.1f against 100 perfect decisions' %.1f", a, b)
	}
	if none := e.Compute(base, cfg).Sub(keySkill); none != 0 {
		t.Fatalf("an agent with no scored decisions scored %.1f, want 0", none)
	}
}

// The weight-sum validator has to include the new dimension, or a config that adds skill
// weight without removing it elsewhere would quietly produce P-Indexes above the scale.
func TestConfigValidatorCountsSkillWeight(t *testing.T) {
	// Sums to 1.1 once skill is counted — must be rejected.
	bad := []byte(`{"scale":1000,"weights":{"arena":0.5,"consistency":0.2,"difficulty":0.1,
		"activity":0.1,"intelligence":0.1,"skill":0.1}}`)
	if _, err := ParseConfig(9, bad); err == nil {
		t.Fatal("a config whose weights sum to 1.1 was accepted; skill is not in the validator")
	}
	// A pre-existing config with no skill key must still be valid and unchanged.
	old := []byte(`{"scale":1000,"weights":{"arena":0.4,"consistency":0.2,"difficulty":0.1,
		"activity":0.1,"intelligence":0.2}}`)
	cfg, err := ParseConfig(1, old)
	if err != nil {
		t.Fatalf("an existing config stopped validating when skill was added: %v", err)
	}
	if cfg.Weights.Skill != 0 {
		t.Fatalf("skill weight defaulted to %.3f, want 0", cfg.Weights.Skill)
	}
}
