package devprofile

import (
	"context"
	"strings"
	"testing"
)

type profileSpy struct {
	name, bio, avatar string
	calls             int
}

func (p *profileSpy) SetProfile(_ context.Context, _, name, bio, avatar string) error {
	p.calls++
	p.name, p.bio, p.avatar = name, bio, avatar
	return nil
}

// spyRepo satisfies the whole Repo interface by embedding it (nil) and overriding the
// one method under test. Any other call would panic, which is the point: it proves
// SetProfile touches nothing else.
type spyRepo struct {
	Repo
	spy *profileSpy
}

// Named field, not embedded: embedding both Repo and *profileSpy makes SetProfile
// ambiguous and the type stops satisfying Repo at all.
func (r spyRepo) SetProfile(ctx context.Context, u, name, bio, avatar string) error {
	return r.spy.SetProfile(ctx, u, name, bio, avatar)
}

func svcWithProfileSpy(spy *profileSpy) *Service { return &Service{repo: spyRepo{spy: spy}} }

func TestSetProfileTrimsAndStores(t *testing.T) {
	spy := &profileSpy{}
	if err := svcWithProfileSpy(spy).SetProfile(context.Background(), "usr_1", "  Atlas  ", " plays cards ", "https://cdn.example.com/a.png"); err != nil {
		t.Fatal(err)
	}
	if spy.name != "Atlas" || spy.bio != "plays cards" {
		t.Fatalf("not trimmed: name=%q bio=%q", spy.name, spy.bio)
	}
}

// An avatar is rendered in OTHER developers' browsers. javascript: is script
// execution and data: is an exfiltration vector; neither has a legitimate use here,
// so the scheme is allow-listed rather than blocklisted.
func TestSetProfileRefusesDangerousAvatarSchemes(t *testing.T) {
	for _, bad := range []string{
		"javascript:alert(1)",
		"data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==",
		"http://insecure.example.com/a.png",
		"//evil.example.com/a.png",
		" javascript:alert(1)",
	} {
		spy := &profileSpy{}
		if err := svcWithProfileSpy(spy).SetProfile(context.Background(), "usr_1", "", "", bad); err == nil {
			t.Fatalf("accepted a dangerous avatar URL: %q", bad)
		}
		if spy.calls != 0 {
			t.Fatalf("wrote a rejected avatar to the database: %q", bad)
		}
	}
}

func TestSetProfileAllowsClearing(t *testing.T) {
	// Empty means "remove it", not "reject it".
	spy := &profileSpy{}
	if err := svcWithProfileSpy(spy).SetProfile(context.Background(), "usr_1", "", "", ""); err != nil {
		t.Fatalf("a developer must be able to remove their name or photo: %v", err)
	}
	if spy.calls != 1 {
		t.Fatal("clearing should still reach the repo")
	}
}

func TestSetProfileBoundsLength(t *testing.T) {
	long := strings.Repeat("x", 500)
	spy := &profileSpy{}
	if err := svcWithProfileSpy(spy).SetProfile(context.Background(), "usr_1", long, "", ""); err == nil {
		t.Fatal("accepted an over-long display name")
	}
	if err := svcWithProfileSpy(spy).SetProfile(context.Background(), "usr_1", "ok", long, ""); err == nil {
		t.Fatal("accepted an over-long bio")
	}
	if spy.calls != 0 {
		t.Fatal("wrote an over-long value to the database")
	}
}
