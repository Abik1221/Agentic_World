package devprofile

import (
	"context"
	"encoding/json"
	"log/slog"
)

// Telling someone they have a new follower.
//
// ONE dependency, not two, and that is deliberate. The obvious design is "write a row"
// plus "push it live" as separate sinks, but the arena already has a component that does
// both in the right order with the right guard — it writes the notification, and pushes
// to the open stream ONLY when the insert actually inserted. Splitting them here would
// have re-implemented that dedupe, and a second implementation is how a refollow ends up
// toasting an event whose row was suppressed as a duplicate.
//
// Durability first is what makes the two agree: the row is the record, the push only
// removes the wait. A follower count that moves live and then reverts on reload is worse
// than one that needed a refresh, because it looks like the follow failed.
//
// Best-effort throughout: a follow must never fail because a notification could not be
// written. By the time this runs the graph row is committed and the person pressing
// Follow has done what they came to do.

// FollowAnnouncer writes a notification and mirrors it onto the recipient's live stream,
// idempotently per (recipient, kind, ref). Satisfied by the same adapter the money flows
// use, so a new follower behaves exactly like every other notification.
type FollowAnnouncer interface {
	Notify(ctx context.Context, userPublicID, kind, ref string, payload []byte) error
}

// KindDeveloperFollowed is the event/notification kind. The SAME string is used for the
// persisted row and the live event, so the bell cannot describe an event differently
// from the toast that announced it.
const KindDeveloperFollowed = "developer_followed"

// SetFollowAnnouncer wires new-follower notifications. Nil leaves them off: the follow
// still records, it just arrives silently.
func (s *Service) SetFollowAnnouncer(a FollowAnnouncer) { s.follows = a }

// announceFollow notifies the FOLLOWEE that follower now follows them.
//
// The payload carries the follower's identity so the notification can name them, and the
// resulting follower COUNT so the receiving UI can set the number rather than
// incrementing a value it may have loaded an hour ago.
func (s *Service) announceFollow(ctx context.Context, followerUserPublicID, followeeUserPublicID string, followers int) {
	if s.follows == nil {
		return
	}
	// The follower's public identity, for "N started following you". Best-effort: an
	// unreadable name still yields a correct notification, just a less specific one.
	var name, handle, avatar string
	if id, found, err := s.repo.ResolveHandle(ctx, followerUserPublicID); err == nil && found {
		name, handle, avatar = id.DisplayName, id.Username, id.AvatarURL
	}
	payload, err := json.Marshal(map[string]any{
		"follower":        followerUserPublicID,
		"follower_name":   name,
		"follower_handle": handle,
		"follower_avatar": avatar,
		"followers":       followers,
	})
	if err != nil {
		return
	}

	// ref is the FOLLOWER, not a timestamp: one row per (recipient, kind, follower), so
	// unfollowing and refollowing does not stack duplicate notifications for one person
	// — and the announcer's own idempotency then suppresses the repeat push too.
	if err := s.follows.Notify(ctx, followeeUserPublicID, KindDeveloperFollowed, "follow:"+followerUserPublicID, payload); err != nil {
		slog.Warn("devprofile: new-follower notification failed",
			"followee", followeeUserPublicID, "follower", followerUserPublicID, "error", err)
	}
}
