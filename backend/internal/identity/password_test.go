package identity

import "testing"

func TestPasswordHashRoundTrip(t *testing.T) {
	const pepper = "test-pepper"
	hash, err := hashPassword("correct horse battery staple", pepper)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if !verifyPassword(hash, "correct horse battery staple", pepper) {
		t.Fatal("verifyPassword failed for the correct password")
	}
	if verifyPassword(hash, "wrong password", pepper) {
		t.Fatal("verifyPassword succeeded for a wrong password")
	}
	if verifyPassword(hash, "correct horse battery staple", "wrong-pepper") {
		t.Fatal("verifyPassword succeeded with the wrong pepper")
	}
}

func TestPasswordHashUniquePerCall(t *testing.T) {
	const pepper = "p"
	a, _ := hashPassword("same-password", pepper)
	b, _ := hashPassword("same-password", pepper)
	if a == b {
		t.Fatal("identical passwords produced identical hashes (missing per-hash salt)")
	}
}

func TestLongPasswordIsFullySignificant(t *testing.T) {
	// bcrypt truncates at 72 bytes; our HMAC pre-hash must keep bytes past 72
	// meaningful, so two long passwords differing only after byte 72 must differ.
	const pepper = "p"
	base := ""
	for i := 0; i < 80; i++ {
		base += "a"
	}
	hash, err := hashPassword(base+"ONE", pepper)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if verifyPassword(hash, base+"TWO", pepper) {
		t.Fatal("passwords differing only past byte 72 were treated as equal")
	}
}

func TestNormalizeEmail(t *testing.T) {
	ok := map[string]string{
		"USER@Example.com":   "user@example.com",
		"  a@b.co  ":         "a@b.co",
		"Mixed.Case@Foo.Org": "mixed.case@foo.org",
	}
	for in, want := range ok {
		got, valid := normalizeEmail(in)
		if !valid || got != want {
			t.Errorf("normalizeEmail(%q) = (%q, %v), want (%q, true)", in, got, valid, want)
		}
	}
	for _, bad := range []string{"", "notanemail", "no@domain", "a b@c.com", "Name <a@b.com>", "a@@b.com"} {
		if _, valid := normalizeEmail(bad); valid {
			t.Errorf("normalizeEmail(%q) = valid, want invalid", bad)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	if err := validatePassword("short7!"); err == nil { // 7 chars
		t.Error("expected error for a password under 8 chars")
	}
	if err := validatePassword("longenough"); err != nil {
		t.Errorf("unexpected error for a valid password: %v", err)
	}
}

func TestValidAgentName(t *testing.T) {
	for _, good := range []string{"belo", "my-agent", "Agent_9", "abc"} {
		if !validAgentName(good) {
			t.Errorf("validAgentName(%q) = false, want true", good)
		}
	}
	for _, bad := range []string{"ab", "has space", "way-too-long-name-way-too-long-name", "dot.name", ""} {
		if validAgentName(bad) {
			t.Errorf("validAgentName(%q) = true, want false", bad)
		}
	}
}
