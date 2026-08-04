package devprofile

import (
	"context"
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

// FOLLOWER AND FOLLOWING LISTS.
//
// The counts were public on every profile and there was no way to open them: the edges were
// in the database with an index in both directions, and the only way a developer could learn
// who followed them was to catch the notification as it arrived. These are the read paths
// behind the two numbers.
//
// PUBLIC, like the counts they expand. Nothing here is visible that the profile page did not
// already publish — a handle, a display name, an avatar, a P-Index — and requiring a session
// to see who follows a public developer would make the directory less useful without making
// anyone more private. The one thing deliberately NOT here is the reverse-privacy option: if
// hiding a follower list is ever wanted, it belongs as an account setting enforced in this
// service, not as an accident of who is signed in.

// FollowDirection is which side of the edge to list.
type FollowDirection string

const (
	// DirFollowers lists the developers who follow the subject.
	DirFollowers FollowDirection = "followers"
	// DirFollowing lists the developers the subject follows.
	DirFollowing FollowDirection = "following"
)

// MaxFollowPageSize bounds one page of a follow list.
const MaxFollowPageSize = 50

// FollowListPage is one page of a follow list.
type FollowListPage struct {
	// Subject is the handle or id the list belongs to, echoed so a client rendering two
	// lists cannot mix them up.
	Subject   string          `json:"subject"`
	Direction FollowDirection `json:"direction"`
	// Total is every row matching, ignoring paging — the number the pager walks and the
	// same number the profile header shows.
	Total   int            `json:"count"`
	Entries []DirectoryRow `json:"entries"`
	Limit   int            `json:"limit"`
	Offset  int            `json:"offset"`
	// NextCursor is 0 on the last page. Derived from the TOTAL rather than from "the page
	// came back full", so a page landing exactly on the last row does not advertise
	// another one that turns out to be empty.
	NextCursor int `json:"next_cursor,omitempty"`
}

func clampFollowPage(limit, offset int) (int, int) {
	if limit <= 0 || limit > MaxFollowPageSize {
		limit = 24
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// DeveloperFollowList serves one page of a developer's followers or following.
//
// handle is a @handle or a developer id; it is resolved the same way every other profile
// route resolves one, so /u/{handle}/followers works for either form.
func (s *Service) DeveloperFollowList(
	ctx context.Context, handle string, dir FollowDirection, limit, offset int,
) (FollowListPage, bool, error) {
	id, found, err := s.repo.ResolveHandle(ctx, handle)
	if err != nil || !found {
		return FollowListPage{}, found, err
	}
	limit, offset = clampFollowPage(limit, offset)

	total, err := s.repo.FollowListCount(ctx, id.UserPublicID, string(dir))
	if err != nil {
		return FollowListPage{}, true, err
	}
	rows, err := s.repo.FollowList(ctx, s.season(), id.UserPublicID, string(dir), limit, offset)
	if err != nil {
		return FollowListPage{}, true, err
	}
	if rows == nil {
		rows = []DirectoryRow{} // always marshal as [], never null
	}
	return FollowListPage{
		Subject: handle, Direction: dir, Total: total, Entries: rows,
		Limit: limit, Offset: offset, NextCursor: nextCursor(offset, limit, len(rows), total),
	}, true, nil
}

// AgentFollowerList serves one page of the developers following an agent.
func (s *Service) AgentFollowerList(
	ctx context.Context, agentPublicID string, limit, offset int,
) (FollowListPage, error) {
	if agentPublicID == "" {
		return FollowListPage{}, httpx.NewError(http.StatusBadRequest, "invalid_request", "agent id is required")
	}
	limit, offset = clampFollowPage(limit, offset)

	total, err := s.repo.AgentFollowerCount(ctx, agentPublicID)
	if err != nil {
		return FollowListPage{}, err
	}
	rows, err := s.repo.AgentFollowers(ctx, s.season(), agentPublicID, limit, offset)
	if err != nil {
		return FollowListPage{}, err
	}
	if rows == nil {
		rows = []DirectoryRow{}
	}
	return FollowListPage{
		Subject: agentPublicID, Direction: DirFollowers, Total: total, Entries: rows,
		Limit: limit, Offset: offset, NextCursor: nextCursor(offset, limit, len(rows), total),
	}, nil
}

// nextCursor is 0 when this page reached the end. Shared by both lists so they cannot
// disagree about when there is more.
func nextCursor(offset, limit, got, total int) int {
	if offset+got < total {
		return offset + limit
	}
	return 0
}
