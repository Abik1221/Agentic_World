package auth

import (
	"context"
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
	// replay the ALREADY-USED token → theft → ErrRefreshReused
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
