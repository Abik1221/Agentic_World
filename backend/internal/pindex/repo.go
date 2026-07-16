package pindex

import (
	"context"
	"time"
)

// Repo persists P-Index state and assembles developer inputs. All rating math stays
// in the engine; the repo only reads facts and writes results.
type Repo interface {
	// ActiveConfig returns the single active scoring config.
	ActiveConfig(ctx context.Context) (Config, error)

	// Inputs assembles a developer's scoring inputs for the season, as of asOf.
	// Returns zero-value inputs (all dimensions score 0) for a developer with no
	// rated activity — never an error for "nothing yet".
	Inputs(ctx context.Context, userPublicID string, season int, asOf time.Time) (DeveloperInputs, error)

	// Save writes the computed P-Index: upserts developer_pindex (updating the
	// running peak), appends a developer_pindex_history row (breakdown + delta +
	// inputs hash), and emits pindex.updated — all in one transaction.
	Save(ctx context.Context, userPublicID string, season int, res Result, inputsHash string, asOf time.Time) error

	// EnqueueDirtyByAgents marks the OWNERS of the given agents for recompute.
	EnqueueDirtyByAgents(ctx context.Context, agentPublicIDs []string) error
	// EnqueueDirty marks developers (by public id) for recompute.
	EnqueueDirty(ctx context.Context, userPublicIDs []string) error
	// PeekDirty returns up to limit dirty developers with their claim tokens (the
	// enqueued_at at peek time), without clearing them.
	PeekDirty(ctx context.Context, limit int) ([]Dirty, error)
	// ClearDirty removes a developer from the dirty set ONLY if its token is
	// unchanged — a re-enqueue that landed mid-recompute bumps the token, so the
	// clear is a no-op and the developer stays dirty for the next tick (no lost
	// update). Returns whether the row was actually cleared.
	ClearDirty(ctx context.Context, userPublicID string, token time.Time) (bool, error)

	// Rank recomputes global_rank + percentile (+ running best_rank) for a season.
	Rank(ctx context.Context, season int) error

	// Get returns a developer's current P-Index snapshot for a season.
	Get(ctx context.Context, userPublicID string, season int) (Snapshot, bool, error)
	// History returns a developer's recent recompute history (newest first).
	History(ctx context.Context, userPublicID string, limit int) ([]HistoryEntry, error)
}

// Dirty is a pending recompute: a developer + the claim token (enqueued_at at peek
// time) used to detect a re-enqueue that races the recompute.
type Dirty struct {
	UserPublicID string
	Token        time.Time
}

// Snapshot is a stored developer_pindex row (the reputation surface reads this).
type Snapshot struct {
	UserPublicID  string    `json:"developer"`
	Season        int       `json:"season"`
	PIndex        float64   `json:"p_index"`
	Arena         float64   `json:"arena"`
	Consistency   float64   `json:"consistency"`
	Difficulty    float64   `json:"difficulty"`
	Activity      float64   `json:"activity"`
	GlobalRank    int       `json:"global_rank"`
	Percentile    float64   `json:"percentile"`
	HighestPIndex float64   `json:"highest_pindex"`
	BestRank      int       `json:"best_rank"`
	ConfigVersion int       `json:"config_version"`
	ComputedAt    time.Time `json:"computed_at"`
}

// HistoryEntry is one recompute in the audit/transparency trail.
type HistoryEntry struct {
	Season        int            `json:"season"`
	PIndex        float64        `json:"p_index"`
	Delta         float64        `json:"delta"`
	Breakdown     []Contribution `json:"breakdown"`
	ConfigVersion int            `json:"config_version"`
	ComputedAt    time.Time      `json:"computed_at"`
}
