package devprofile

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// followRepo is the slice of Repo the follow paths touch. The rest of the interface is
// unused here and would only be noise.
type followRepo struct {
	Repo
	handles   map[string]Identity
	following map[string]bool // "follower→followee"
	followers map[string]int
	failWrite error
}

func newFollowRepo() *followRepo {
	return &followRepo{
		handles: map[string]Identity{
			"alice": {UserPublicID: "usr_alice", Username: "alice", DisplayName: "Alice"},
			"bob":   {UserPublicID: "usr_bob", Username: "bob", DisplayName: "Bob"},
			// Resolvable by public id too, which is how announceFollow looks the
			// follower up.
			"usr_alice": {UserPublicID: "usr_alice", Username: "alice", DisplayName: "Alice"},
			"usr_bob":   {UserPublicID: "usr_bob", Username: "bob", DisplayName: "Bob"},
		},
		following: map[string]bool{},
		followers: map[string]int{},
	}
}

func (r *followRepo) ResolveHandle(_ context.Context, handle string) (Identity, bool, error) {
	id, ok := r.handles[handle]
	return id, ok, nil
}

func (r *followRepo) FollowCounts(_ context.Context, userPublicID string) (int, int, error) {
	return r.followers[userPublicID], 0, nil
}

func (r *followRepo) IsFollowing(_ context.Context, follower, followee string) (bool, error) {
	return r.following[follower+"→"+followee], nil
}

func (r *followRepo) Follow(_ context.Context, follower, followee string) error {
	if r.failWrite != nil {
		return r.failWrite
	}
	k := follower + "→" + followee
	if !r.following[k] {
		r.following[k] = true
		r.followers[followee]++
	}
	return nil
}

func (r *followRepo) Unfollow(_ context.Context, follower, followee string) error {
	k := follower + "→" + followee
	if r.following[k] {
		delete(r.following, k)
		r.followers[followee]--
	}
	return nil
}

// recordingAnnouncer captures what the followee would be told.
type recordingAnnouncer struct {
	calls []struct {
		user, kind, ref string
		payload         map[string]any
	}
	err error
}

func (a *recordingAnnouncer) Notify(_ context.Context, user, kind, ref string, payload []byte) error {
	var p map[string]any
	_ = json.Unmarshal(payload, &p)
	a.calls = append(a.calls, struct {
		user, kind, ref string
		payload         map[string]any
	}{user, kind, ref, p})
	return a.err
}

func newFollowSvc(repo Repo) *Service {
	return &Service{repo: repo, coinCents: 1}
}

// The read that did not exist. Without it the client had to assume "not following", which
// is why the button forgot itself on every page load.
func TestFollowStateReportsTheRelationshipAndCounts(t *testing.T) {
	repo := newFollowRepo()
	s := newFollowSvc(repo)
	ctx := context.Background()

	st, err := s.FollowState(ctx, "usr_alice", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if st.Following || st.Followers != 0 {
		t.Fatalf("before following: %+v, want following=false followers=0", st)
	}

	if _, err := s.Follow(ctx, "usr_alice", "bob"); err != nil {
		t.Fatal(err)
	}

	st, err = s.FollowState(ctx, "usr_alice", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Following {
		t.Fatal("after following, the state still reads not-following — the button would revert on reload")
	}
	if st.Followers != 1 {
		t.Fatalf("followers = %d, want 1", st.Followers)
	}
}

// A mutation returns the resulting state, so the button and the count beside it are set
// from ONE response. A client left to guess the new count drifts as soon as anyone else
// follows at the same time.
func TestMutationsReturnTheResultingState(t *testing.T) {
	repo := newFollowRepo()
	s := newFollowSvc(repo)
	ctx := context.Background()

	got, err := s.Follow(ctx, "usr_alice", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Following || got.Followers != 1 {
		t.Fatalf("follow returned %+v, want following=true followers=1", got)
	}

	got, err = s.Unfollow(ctx, "usr_alice", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if got.Following || got.Followers != 0 {
		t.Fatalf("unfollow returned %+v, want following=false followers=0", got)
	}
}

// Idempotent, and notified ONCE. A double-tap on a phone, or a retry after a dropped
// response, must confirm rather than toggle — and must not send a second notification.
func TestFollowIsIdempotentAndNotifiesOnce(t *testing.T) {
	repo := newFollowRepo()
	ann := &recordingAnnouncer{}
	s := newFollowSvc(repo)
	s.SetFollowAnnouncer(ann)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		st, err := s.Follow(ctx, "usr_alice", "bob")
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if !st.Following || st.Followers != 1 {
			t.Fatalf("call %d returned %+v, want following=true followers=1", i, st)
		}
	}
	if len(ann.calls) != 1 {
		t.Fatalf("%d notifications for three taps of the same button — a repeat follow must be silent", len(ann.calls))
	}
}

// The followee is told, with enough detail to name the follower and set the count.
func TestNewFollowerNotificationCarriesWhoAndHowMany(t *testing.T) {
	repo := newFollowRepo()
	ann := &recordingAnnouncer{}
	s := newFollowSvc(repo)
	s.SetFollowAnnouncer(ann)

	if _, err := s.Follow(context.Background(), "usr_alice", "bob"); err != nil {
		t.Fatal(err)
	}
	if len(ann.calls) != 1 {
		t.Fatalf("got %d notifications, want 1", len(ann.calls))
	}
	c := ann.calls[0]
	if c.user != "usr_bob" {
		t.Fatalf("notified %q — the FOLLOWEE must be the recipient", c.user)
	}
	if c.kind != KindDeveloperFollowed {
		t.Fatalf("kind = %q", c.kind)
	}
	// ref keyed on the follower, so refollowing does not stack duplicates.
	if c.ref != "follow:usr_alice" {
		t.Fatalf("ref = %q, want follow:usr_alice", c.ref)
	}
	if c.payload["follower_handle"] != "alice" || c.payload["follower_name"] != "Alice" {
		t.Fatalf("payload cannot name the follower: %+v", c.payload)
	}
	// The COUNT rides along so the receiving tab sets the number instead of incrementing
	// a value it may have loaded an hour ago.
	if n, ok := c.payload["followers"].(float64); !ok || n != 1 {
		t.Fatalf("payload followers = %v, want 1", c.payload["followers"])
	}
}

// Unfollowing sends nothing. "X stopped following you" is information the recipient can
// do nothing with, and no platform sends it.
func TestUnfollowIsSilent(t *testing.T) {
	repo := newFollowRepo()
	ann := &recordingAnnouncer{}
	s := newFollowSvc(repo)
	s.SetFollowAnnouncer(ann)
	ctx := context.Background()

	_, _ = s.Follow(ctx, "usr_alice", "bob")
	before := len(ann.calls)
	if _, err := s.Unfollow(ctx, "usr_alice", "bob"); err != nil {
		t.Fatal(err)
	}
	if len(ann.calls) != before {
		t.Fatal("unfollowing notified the followee")
	}
}

// A follow must not fail because a notification could not be written. The graph row is
// already committed by then, and the person pressing Follow has done what they came for.
func TestFollowSurvivesANotificationFailure(t *testing.T) {
	repo := newFollowRepo()
	s := newFollowSvc(repo)
	s.SetFollowAnnouncer(&recordingAnnouncer{err: errors.New("notifications table is on fire")})

	st, err := s.Follow(context.Background(), "usr_alice", "bob")
	if err != nil {
		t.Fatalf("follow failed because a notification failed: %v", err)
	}
	if !st.Following {
		t.Fatal("the follow did not take effect")
	}
}

// You cannot follow yourself, and your own profile is flagged so the client does not
// offer the button. The client cannot work this out — a public profile page is cacheable
// and the session cookie carries no handle.
func TestSelfFollowIsRefusedAndSelfIsFlagged(t *testing.T) {
	repo := newFollowRepo()
	s := newFollowSvc(repo)
	ctx := context.Background()

	if _, err := s.Follow(ctx, "usr_alice", "alice"); err == nil {
		t.Fatal("following yourself was allowed")
	}
	st, err := s.FollowState(ctx, "usr_alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsSelf {
		t.Fatal("is_self was false on the viewer's own profile")
	}
	if st.Following {
		t.Fatal("reported following yourself")
	}
}

// A signed-out viewer still sees the public counts; the relationship is simply false.
func TestSignedOutViewerGetsCountsButNoRelationship(t *testing.T) {
	repo := newFollowRepo()
	s := newFollowSvc(repo)
	ctx := context.Background()
	_, _ = s.Follow(ctx, "usr_alice", "bob")

	st, err := s.FollowState(ctx, "", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if st.Followers != 1 {
		t.Fatalf("followers = %d, want the public count 1", st.Followers)
	}
	if st.Following || st.IsSelf {
		t.Fatalf("signed-out viewer got following=%v is_self=%v", st.Following, st.IsSelf)
	}
}

// An unknown handle is a 404, not a silent zero — otherwise a typo'd profile link shows
// a plausible empty developer.
func TestUnknownHandleIsNotFound(t *testing.T) {
	s := newFollowSvc(newFollowRepo())
	if _, err := s.FollowState(context.Background(), "usr_alice", "nobody"); err == nil {
		t.Fatal("an unknown handle returned a state")
	}
	if _, err := s.Follow(context.Background(), "usr_alice", "nobody"); err == nil {
		t.Fatal("following an unknown handle succeeded")
	}
}
