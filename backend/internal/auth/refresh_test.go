package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// in-memory RefreshRepo for the service tests.
type memRepo struct {
	rows map[string]*RefreshRow
}

func newMemRepo() *memRepo { return &memRepo{rows: map[string]*RefreshRow{}} }

func (m *memRepo) Create(_ context.Context, r RefreshRow) error {
	c := r
	m.rows[r.ID] = &c
	return nil
}
func (m *memRepo) Get(_ context.Context, id string) (RefreshRow, error) {
	if r, ok := m.rows[id]; ok {
		return *r, nil
	}
	return RefreshRow{}, ErrRefreshInvalid
}
func (m *memRepo) MarkUsed(_ context.Context, id string, at time.Time) error {
	if r, ok := m.rows[id]; ok {
		t := at
		r.UsedAt = &t
	}
	return nil
}
func (m *memRepo) RevokeFamily(_ context.Context, fam string, at time.Time) error {
	for _, r := range m.rows {
		if r.FamilyID == fam {
			t := at
			r.RevokedAt = &t
		}
	}
	return nil
}

func (m *memRepo) RevokeAllForUser(_ context.Context, userPublicID string, at time.Time) (int, error) {
	n := 0
	for _, r := range m.rows {
		if r.UserPublicID == userPublicID && r.RevokedAt == nil {
			t := at
			r.RevokedAt = &t
			n++
		}
	}
	return n, nil
}

func newSvc() (*RefreshService, *memRepo) {
	repo := newMemRepo()
	s := NewRefreshService(repo, NewJWT("test-signing-key-at-least-32-bytes-long!", time.Hour), 24*time.Hour)
	return s, repo
}

func TestRefresh_RotateHappyPath(t *testing.T) {
	s, _ := newSvc()
	ctx := context.Background()
	raw, err := s.Issue(ctx, "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	acc, next, uid, err := s.Rotate(ctx, raw)
	if err != nil || acc == "" || next == "" || uid != "usr_1" {
		t.Fatalf("rotate: hasAcc=%v hasNext=%v uid=%q err=%v", acc != "", next != "", uid, err)
	}
	// the new access JWT must verify as the same user
	p, err := s.jwt.Parse(acc)
	if err != nil || p.UserPublicID != "usr_1" {
		t.Fatalf("access jwt invalid: %v", err)
	}
	// the new refresh token rotates again fine
	if _, _, _, err := s.Rotate(ctx, next); err != nil {
		t.Fatalf("second rotate failed: %v", err)
	}
}

func TestRefresh_ReuseRevokesFamily(t *testing.T) {
	s, _ := newSvc()
	ctx := context.Background()
	raw, _ := s.Issue(ctx, "usr_1")
	_, next, _, err := s.Rotate(ctx, raw) // raw is now used
	if err != nil {
		t.Fatal(err)
	}
	// Replay the ALREADY-USED token → theft → ErrRefreshReused.
	//
	// Advanced past the reuse interval first. Immediately after rotation a repeat
	// presentation is the concurrency race one dashboard navigation produces — several
	// requests holding the same token — and treating THAT as theft is what signed users out
	// when they clicked between sections. The theft response is unchanged; what changed is
	// that it no longer fires on a benign race. See TestRefresh_ConcurrentUseIsNotTheft.
	base := time.Now()
	s.now = func() time.Time { return base.Add(s.reuseGrace + time.Minute) }
	if _, _, _, err := s.Rotate(ctx, raw); err != ErrRefreshReused {
		t.Fatalf("expected ErrRefreshReused, got %v", err)
	}
	// the whole family is now revoked → the freshly-minted `next` is dead too
	if _, _, _, err := s.Rotate(ctx, next); err != ErrRefreshInvalid {
		t.Fatalf("expected family revoked (ErrRefreshInvalid), got %v", err)
	}
}

func TestRefresh_Expired(t *testing.T) {
	s, _ := newSvc()
	ctx := context.Background()
	raw, _ := s.Issue(ctx, "usr_1")
	s.now = func() time.Time { return time.Now().Add(48 * time.Hour) } // past the 24h window
	if _, _, _, err := s.Rotate(ctx, raw); err != ErrRefreshExpired {
		t.Fatalf("expected ErrRefreshExpired, got %v", err)
	}
}

func TestRefresh_TamperedSecretRejected(t *testing.T) {
	s, _ := newSvc()
	ctx := context.Background()
	raw, _ := s.Issue(ctx, "usr_1")
	id, _, _ := splitToken(raw)
	if _, _, _, err := s.Rotate(ctx, id+".wrong-secret"); err != ErrRefreshInvalid {
		t.Fatalf("expected ErrRefreshInvalid for bad secret, got %v", err)
	}
}

func TestRefresh_RevokeAllForUserKillsEveryFamily(t *testing.T) {
	s, _ := newSvc()
	ctx := context.Background()
	a, err := s.Issue(ctx, "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Issue(ctx, "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Issue(ctx, "usr_2")
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.RevokeAllForUser(ctx, "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("revoked %d rows, want at least the two families for usr_1", n)
	}
	if _, _, _, err := s.Rotate(ctx, a); err != ErrRefreshInvalid {
		t.Fatalf("first family should be dead, got %v", err)
	}
	if _, _, _, err := s.Rotate(ctx, b); err != ErrRefreshInvalid {
		t.Fatalf("second family should be dead, got %v", err)
	}
	if _, _, _, err := s.Rotate(ctx, other); err != nil {
		t.Fatalf("another user's session must survive: %v", err)
	}
}

func TestRefresh_RevokeLogout(t *testing.T) {
	s, _ := newSvc()
	ctx := context.Background()
	raw, _ := s.Issue(ctx, "usr_1")
	if err := s.Revoke(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Rotate(ctx, raw); err != ErrRefreshInvalid {
		t.Fatalf("expected revoked token to be invalid, got %v", err)
	}
}

// TestRefresh_ConcurrentUseIsNotTheft reproduces the bug that signed users out of the
// dashboard when they clicked between sections.
//
// One navigation fires several requests at once — the page, its RSC payload, prefetches —
// and each passes through the edge middleware. With the access JWT expired they all present
// the SAME refresh token. Strict single-use rotates on the first and reads every other as
// theft, which revoked the whole family: clicking a sidebar link signed the user out of
// every device.
//
// Within the grace window a repeat presentation must therefore succeed and revoke nothing.
func TestRefresh_ConcurrentUseIsNotTheft(t *testing.T) {
	s, repo := newSvc()
	rt, err := s.Issue(context.Background(), "usr_1")
	if err != nil {
		t.Fatal(err)
	}

	// First request of the navigation rotates.
	if _, _, _, err := s.Rotate(context.Background(), rt); err != nil {
		t.Fatalf("first rotate: %v", err)
	}
	// The others arrive microseconds later holding the same token.
	for i := 0; i < 3; i++ {
		acc, nr, uid, err := s.Rotate(context.Background(), rt)
		if err != nil {
			t.Fatalf("concurrent rotate %d must succeed, got %v", i, err)
		}
		if acc == "" || nr == "" || uid != "usr_1" {
			t.Errorf("concurrent rotate %d returned an unusable session", i)
		}
	}

	// And nothing was revoked — the whole point. A revoked family is the user signed out
	// everywhere, which is what the bug did.
	for id, row := range repo.rows {
		if row.RevokedAt != nil {
			t.Errorf("row %s was revoked by a benign concurrent refresh", id)
		}
	}
}

// TestRefresh_ReuseAfterGraceStillRevokes pins the security half.
//
// The grace window buys concurrency, not amnesty. A token replayed long after it rotated is
// still treated as leaked, and the family still dies — otherwise the reuse interval would
// have quietly removed the theft response rather than narrowed it.
func TestRefresh_ReuseAfterGraceStillRevokes(t *testing.T) {
	s, repo := newSvc()
	rt, err := s.Issue(context.Background(), "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Rotate(context.Background(), rt); err != nil {
		t.Fatal(err)
	}

	// Well past the window.
	base := time.Now()
	s.now = func() time.Time { return base.Add(s.reuseGrace + time.Minute) }

	if _, _, _, err := s.Rotate(context.Background(), rt); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("late reuse should be theft, got %v", err)
	}
	revoked := 0
	for _, row := range repo.rows {
		if row.RevokedAt != nil {
			revoked++
		}
	}
	if revoked == 0 {
		t.Error("late reuse must revoke the family")
	}
}
