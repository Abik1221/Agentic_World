package clips

import "context"

// DevGenerator produces a deterministic placeholder asset URL without touching S3
// or a renderer, so the full clip pipeline runs offline. The real generator
// (static scoreboard image → S3 → CDN) is wired by configuration in prod.
type DevGenerator struct {
	BaseURL string // CDN-style prefix; defaults applied by NewDevGenerator
}

// NewDevGenerator builds an offline generator with a sane default CDN prefix.
func NewDevGenerator(baseURL string) DevGenerator {
	if baseURL == "" {
		baseURL = "https://cdn.local/clips"
	}
	return DevGenerator{BaseURL: baseURL}
}

func (g DevGenerator) Generate(_ context.Context, m ClipMeta) (string, error) {
	return g.BaseURL + "/" + m.ClipPublicID + ".png", nil
}
