// Package social is the lightweight social graph: follows plus the
// notification fan-out that runs when a match finalizes. Notifications are
// enqueued off the hot path and written idempotently per (recipient, kind, ref),
// so a redelivery never double-notifies. See Stage 8.
package social

import "context"

// Repo persists follows and notifications and reads the follow graph.
type Repo interface {
	Follow(ctx context.Context, userPublicID, agentPublicID string) error
	Unfollow(ctx context.Context, userPublicID, agentPublicID string) error
	// MatchParticipants returns both seats of a match (agent + owner + result delta).
	MatchParticipants(ctx context.Context, matchPublicID string) ([]Participant, error)
	// FollowerUserIDs returns the public ids of users following an agent.
	FollowerUserIDs(ctx context.Context, agentPublicID string) ([]string, error)
	// InsertNotification writes a notification idempotently; inserted is false on a
	// duplicate (recipient, kind, ref).
	InsertNotification(ctx context.Context, recipientUserPublicID, kind, ref string, payload []byte) (inserted bool, err error)
}

// Participant is one seat of a finished match.
type Participant struct {
	AgentPublicID string
	OwnerPublicID string
	CoinsDelta    int64
}
