package identity

import (
	"context"
	"testing"
)

type banRepo struct {
	Repo
	status map[string]string
}

func (r *banRepo) UserStatus(_ context.Context, id string) (string, error) {
	st, ok := r.status[id]
	if !ok {
		return "", ErrNotFound
	}
	return st, nil
}

func (r *banRepo) SetUserStatus(_ context.Context, id, next string) (string, error) {
	prev := r.status[id]
	if prev == "" {
		return "", ErrNotFound
	}
	r.status[id] = next
	return prev, nil
}

func (r *banRepo) ListBannedUserIDs(_ context.Context) ([]string, error) {
	var out []string
	for id, st := range r.status {
		if AccessBlocked(st) {
			out = append(out, id)
		}
	}
	return out, nil
}

func (r *banRepo) AgentIDsByOwner(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func TestAccessBlockedIsBanOnly(t *testing.T) {
	if AccessBlocked("banned") != true {
		t.Fatal("banned must block login")
	}
	if AccessBlocked("suspended") {
		t.Fatal("suspend is a play-halt, not a login lock")
	}
	if AccessBlocked("active") || AccessBlocked("") {
		t.Fatal("active/empty must not block")
	}
}

func TestBanBlocksThenUnbanRestores(t *testing.T) {
	repo := &banRepo{status: map[string]string{"usr_1": "active"}}
	svc := &Service{repo: repo, bans: NewBanIndex()}

	if err := svc.Blocked(context.Background(), "usr_1"); err != nil {
		t.Fatalf("active user must pass: %v", err)
	}
	if _, err := svc.Ban(context.Background(), "usr_1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Blocked(context.Background(), "usr_1"); err != ErrAccountBanned {
		t.Fatalf("banned user must be locked out, got %v", err)
	}
	if repo.status["usr_1"] != "banned" {
		t.Fatalf("status = %q, want banned", repo.status["usr_1"])
	}
	if _, err := svc.Unban(context.Background(), "usr_1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Blocked(context.Background(), "usr_1"); err != nil {
		t.Fatalf("unban must restore access: %v", err)
	}
	if repo.status["usr_1"] != "active" {
		t.Fatalf("status = %q, want active", repo.status["usr_1"])
	}
}

func TestLoadBannedWarmsIndex(t *testing.T) {
	repo := &banRepo{status: map[string]string{"usr_bad": "banned", "usr_ok": "active"}}
	svc := &Service{repo: repo, bans: NewBanIndex()}
	if err := svc.LoadBanned(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !svc.bans.IsBanned("usr_bad") {
		t.Fatal("warmed index missed the banned user")
	}
	if svc.bans.IsBanned("usr_ok") {
		t.Fatal("active user must not be in the ban index")
	}
}