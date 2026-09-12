package identity

import (
	"context"
	"strings"
	"sync"
)

// AccessBlocked reports whether a users.status value is a hard lock: no login,
// no refresh, no dashboard. Ban is that lock. Suspend stays a play-halt
// (ranked/config bus) so an operator can stop tables without kicking the
// developer out of their own console.
func AccessBlocked(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "banned")
}

// BanIndex is the in-process set of banned user public ids. Auth middleware
// consults it so a ban is visible on the next request, not after a cache TTL.
type BanIndex struct {
	mu     sync.RWMutex
	banned map[string]bool
}

func NewBanIndex() *BanIndex {
	return &BanIndex{banned: map[string]bool{}}
}

func (b *BanIndex) IsBanned(userPublicID string) bool {
	if b == nil || userPublicID == "" {
		return false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.banned[userPublicID]
}

func (b *BanIndex) Set(userPublicID string, banned bool) {
	if b == nil || userPublicID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if banned {
		b.banned[userPublicID] = true
		return
	}
	delete(b.banned, userPublicID)
}

func (b *BanIndex) Replace(ids []string) {
	if b == nil {
		return
	}
	next := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" {
			next[id] = true
		}
	}
	b.mu.Lock()
	b.banned = next
	b.mu.Unlock()
}

// LoadBanned warms the index from the database so a restart does not briefly
// admit a banned session.
func (s *Service) LoadBanned(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return nil
	}
	ids, err := s.repo.ListBannedUserIDs(ctx)
	if err != nil {
		return err
	}
	s.bans.Replace(ids)
	return nil
}

// Blocked implements auth.AccountGate. Hot path: the in-process index, then the
// users row so another arena instance's ban still lands. A missing row is not a
// ban — platform seats and brand-new tokens must not be locked out by a lookup
// miss. A confirmed banned row is cached so the next request does not pay the
// query again.
func (s *Service) Blocked(ctx context.Context, userPublicID string) error {
	if s == nil || userPublicID == "" {
		return nil
	}
	if s.bans.IsBanned(userPublicID) {
		return ErrAccountBanned
	}
	if s.repo == nil {
		return nil
	}
	st, err := s.repo.UserStatus(ctx, userPublicID)
	if err != nil {
		return nil
	}
	if AccessBlocked(st) {
		s.bans.Set(userPublicID, true)
		return ErrAccountBanned
	}
	return nil
}

func (s *Service) rejectIfBanned(ctx context.Context, userPublicID string) error {
	return s.Blocked(ctx, userPublicID)
}

// Ban persists users.status='banned', updates the hot index, and returns the
// prior status. Session revoke and socket kick live on the handler so this
// package does not import refresh or the agent gateway.
func (s *Service) Ban(ctx context.Context, userPublicID string) (prev string, err error) {
	userPublicID = strings.TrimSpace(userPublicID)
	if userPublicID == "" {
		return "", errInvalid("user id is required")
	}
	prev, err = s.repo.SetUserStatus(ctx, userPublicID, "banned")
	if err != nil {
		return "", err
	}
	s.bans.Set(userPublicID, true)
	return prev, nil
}

// Unban restores users.status='active' and drops the id from the hot index.
func (s *Service) Unban(ctx context.Context, userPublicID string) (prev string, err error) {
	userPublicID = strings.TrimSpace(userPublicID)
	if userPublicID == "" {
		return "", errInvalid("user id is required")
	}
	prev, err = s.repo.SetUserStatus(ctx, userPublicID, "active")
	if err != nil {
		return "", err
	}
	s.bans.Set(userPublicID, false)
	return prev, nil
}

// AgentIDsOf lists the owner's agents so a ban can kick live sockets.
func (s *Service) AgentIDsOf(ctx context.Context, userPublicID string) []string {
	if s == nil || s.repo == nil || userPublicID == "" {
		return nil
	}
	ids, err := s.repo.AgentIDsByOwner(ctx, userPublicID)
	if err != nil {
		return nil
	}
	return ids
}
