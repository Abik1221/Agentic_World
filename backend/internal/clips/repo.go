package clips

import (
	"context"
	"time"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
)

// EventLog supplies a match's event log for trigger detection (store.MatchRepo).
type EventLog interface {
	LoadEvents(ctx context.Context, matchPublicID string) ([]gs.Event, error)
}

// Generator renders a clip's preview asset and returns its CDN URL. DevGenerator
// runs offline; a real S3/render implementation is wired by configuration.
type Generator interface {
	Generate(ctx context.Context, m ClipMeta) (assetURL string, err error)
}

// ClipMeta is the input to asset generation.
type ClipMeta struct {
	ClipPublicID  string
	MatchPublicID string
	Trigger       string
	RoundSeq      int
}

// Repo persists clips.
type Repo interface {
	// CreateClips inserts clip rows idempotently (one per (match, trigger)) and
	// returns only the rows that were newly created (so each asset renders once).
	CreateClips(ctx context.Context, matchPublicID string, in []NewClip) ([]Created, error)
	// SetAsset records a rendered asset URL on a clip.
	SetAsset(ctx context.Context, clipPublicID, assetURL string) error
	// Trending returns ready (asset-present) clips ranked by shares then recency.
	Trending(ctx context.Context, limit, offset int) ([]ClipView, error)
}

// NewClip is a clip to create.
type NewClip struct {
	PublicID string
	Trigger  string
	RoundSeq int
}

// Created is a newly inserted clip needing an asset.
type Created struct {
	PublicID string
	Trigger  string
	RoundSeq int
}

// ClipView is a public clip row.
type ClipView struct {
	PublicID   string    `json:"clip_id"`
	MatchID    string    `json:"match_id"`
	Trigger    string    `json:"trigger"`
	RoundSeq   int       `json:"round_seq"`
	AssetURL   string    `json:"asset_url"`
	ShareCount int       `json:"share_count"`
	CreatedAt  time.Time `json:"created_at"`
}
