package devprofile

import (
	"context"
	"strings"
	"testing"
)

type profileSpy struct {
	// Pointers, mirroring the port: nil records "this field was not part of the
	// patch", which is the distinction the whole change exists to preserve.
	name, bio, avatar *string
	calls             int
}

func (p *profileSpy) SetProfile(_ context.Context, _ string, name, bio, avatar *string) error {
	p.calls++
	p.name, p.bio, p.avatar = name, bio, avatar
	return nil
}

// spyRepo satisfies the whole Repo interface by embedding it (nil) and overriding the
// two methods under test. Any other call would panic, which is the point: it proves
// SetProfile touches nothing else.
type spyRepo struct {
	Repo
	spy *profileSpy
}

// Named field, not embedded: embedding both Repo and *profileSpy makes SetProfile
// ambiguous and the type stops satisfying Repo at all.
func (r spyRepo) SetProfile(ctx context.Context, u string, name, bio, avatar *string) error {
	return r.spy.SetProfile(ctx, u, name, bio, avatar)
}

// SetProfile reads the row back so the caller renders what was stored rather than what
// it sent. The spy answers that read with a fixed identity.
func (r spyRepo) ResolveHandle(_ context.Context, handle string) (Identity, bool, error) {
	return Identity{UserPublicID: handle, DisplayName: "stored", Bio: "stored bio"}, true, nil
}

func svcWithProfileSpy(spy *profileSpy) *Service { return &Service{repo: spyRepo{spy: spy}} }

func ptr(s string) *string { return &s }

func TestSetProfileTrimsAndStores(t *testing.T) {
	spy := &profileSpy{}
	if _, err := svcWithProfileSpy(spy).SetProfile(context.Background(), "usr_1",
		ptr("  Atlas  "), ptr(" plays cards "), ptr("https://cdn.example.com/a.png")); err != nil {
		t.Fatal(err)
	}
	if *spy.name != "Atlas" || *spy.bio != "plays cards" {
		t.Fatalf("not trimmed: name=%q bio=%q", *spy.name, *spy.bio)
	}
}

// The returned identity is the STORED one, not an echo of the request. A client that
// renders the response must not be shown a value the database rejected or altered.
func TestSetProfileReturnsTheStoredIdentity(t *testing.T) {
	id, err := svcWithProfileSpy(&profileSpy{}).SetProfile(context.Background(), "usr_1", ptr("Atlas"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if id.DisplayName != "stored" || id.Bio != "stored bio" {
		t.Fatalf("returned an echo of the request rather than the stored row: %+v", id)
	}
}

// The bug this guards: editing one field must not erase the other two.
//
// A nil field has to reach the repo as nil so the SQL leaves that column alone. If the
// service ever normalised nil to "" the repo could not tell "clear it" from "I did not
// send it", and renaming an agent would delete its photo — which is exactly what
// happened while these were plain strings.
func TestSetProfileLeavesOmittedFieldsAlone(t *testing.T) {
	spy := &profileSpy{}
	if _, err := svcWithProfileSpy(spy).SetProfile(context.Background(), "usr_1", ptr("Atlas"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if spy.name == nil || *spy.name != "Atlas" {
		t.Fatalf("display name did not reach the repo: %v", spy.name)
	}
	if spy.bio != nil {
		t.Fatalf("an omitted bio reached the repo as %q — that would clear a stored bio", *spy.bio)
	}
	if spy.avatar != nil {
		t.Fatalf("an omitted avatar reached the repo as %q — that would delete a stored photo", *spy.avatar)
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
		if _, err := svcWithProfileSpy(spy).SetProfile(context.Background(), "usr_1", nil, nil, ptr(bad)); err == nil {
			t.Fatalf("accepted a dangerous avatar URL: %q", bad)
		}
		if spy.calls != 0 {
			t.Fatalf("wrote a rejected avatar to the database: %q", bad)
		}
	}
}

func TestSetProfileAllowsClearing(t *testing.T) {
	// An explicit empty string means "remove it", and must still be accepted — that is
	// the difference between clearing a field and not mentioning it.
	spy := &profileSpy{}
	if _, err := svcWithProfileSpy(spy).SetProfile(context.Background(), "usr_1", ptr(""), ptr(""), ptr("")); err != nil {
		t.Fatalf("a developer must be able to remove their name or photo: %v", err)
	}
	if spy.calls != 1 {
		t.Fatal("clearing should still reach the repo")
	}
	if spy.avatar == nil || *spy.avatar != "" {
		t.Fatalf("an explicit clear must arrive as a pointer to empty, got %v", spy.avatar)
	}
}

func TestSetProfileBoundsLength(t *testing.T) {
	long := strings.Repeat("x", 500)
	spy := &profileSpy{}
	if _, err := svcWithProfileSpy(spy).SetProfile(context.Background(), "usr_1", ptr(long), nil, nil); err == nil {
		t.Fatal("accepted an over-long display name")
	}
	if _, err := svcWithProfileSpy(spy).SetProfile(context.Background(), "usr_1", ptr("ok"), ptr(long), nil); err == nil {
		t.Fatal("accepted an over-long bio")
	}
	if spy.calls != 0 {
		t.Fatal("wrote an over-long value to the database")
	}
}
