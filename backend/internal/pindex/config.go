package pindex

import (
	"encoding/json"
	"fmt"
)

// Config is the versioned, independently-configurable scoring parameter set (loaded
// from pindex_config.params). Every constant the engine uses lives here — none are
// hardcoded — so tuning a dimension is a config-version bump, not a code change.
type Config struct {
	Version int     `json:"-"`
	Scale   float64 `json:"scale"` // sub-score ceiling (1000)

	Weights struct {
		Arena       float64 `json:"arena"`
		Consistency float64 `json:"consistency"`
		Difficulty  float64 `json:"difficulty"`
		Activity    float64 `json:"activity"`
	} `json:"weights"`

	Norm struct {
		Low  float64 `json:"low"`  // rating mapped to 0
		High float64 `json:"high"` // rating mapped to Scale
	} `json:"norm"`

	Arena struct {
		MinMatches int `json:"min_matches"` // below this, an arena is provisional
		MatchCap   int `json:"match_cap"`   // weight saturates here
	} `json:"arena"`

	Consistency struct {
		RDLow      float64 `json:"rd_low"`      // Glicko RD mapped to full certainty
		RDHigh     float64 `json:"rd_high"`     // Glicko RD mapped to zero certainty
		SigmaLow   float64 `json:"sigma_low"`   // TrueSkill sigma mapped to full certainty
		SigmaHigh  float64 `json:"sigma_high"`  // TrueSkill sigma mapped to zero certainty
		MinMatches int     `json:"min_matches"` // sample size for full consistency credit
	} `json:"consistency"`

	Activity struct {
		MatchK          float64 `json:"match_k"`          // diminishing-returns constant
		DiversityTarget float64 `json:"diversity_target"` // arenas for full diversity credit
		RecencyDays     float64 `json:"recency_days"`     // active-window length (days)
	} `json:"activity"`
}

// ParseConfig decodes a pindex_config.params blob and validates the invariants the
// engine relies on (weights sum to 1, positive scale).
func ParseConfig(version int, params []byte) (Config, error) {
	var c Config
	if err := json.Unmarshal(params, &c); err != nil {
		return Config{}, fmt.Errorf("pindex: parse config v%d: %w", version, err)
	}
	c.Version = version
	if c.Scale <= 0 {
		c.Scale = 1000
	}
	sum := c.Weights.Arena + c.Weights.Consistency + c.Weights.Difficulty + c.Weights.Activity
	if sum < 0.999 || sum > 1.001 {
		return Config{}, fmt.Errorf("pindex: config v%d weights sum to %.3f, want 1.0", version, sum)
	}
	return c, nil
}
