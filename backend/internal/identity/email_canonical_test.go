package identity

import "testing"

// One mailbox, one account. A new account carries a referral reward, so "one inbox,
// unlimited sign-ups" is a farm rather than a curiosity.
func TestGmailAliasesCollapseToOneAddress(t *testing.T) {
	want := "nahom@gmail.com"
	for _, alias := range []string{
		"nahom@gmail.com",
		"NAHOM@Gmail.com",
		"n.a.h.o.m@gmail.com",
		"nahom+beta@gmail.com",
		"n.a.hom+test+more@googlemail.com",
		"nahom@googlemail.com",
	} {
		got, ok := normalizeEmail(alias)
		if !ok {
			t.Fatalf("%q was rejected outright", alias)
		}
		if got != want {
			t.Fatalf("%q normalised to %q, want %q — two accounts on one inbox", alias, got, want)
		}
	}
}

// The far worse failure: folding aliases at providers that do NOT treat them as
// aliases merges two real people into one account. Dots are significant almost
// everywhere except Gmail, and '+' is a legal local-part character (RFC 5321).
func TestOtherProvidersAreLeftExactlyAsTyped(t *testing.T) {
	cases := map[string]string{
		"first.last@outlook.com": "first.last@outlook.com",
		"firstlast@outlook.com":  "firstlast@outlook.com",
		"a.b@company.co.uk":      "a.b@company.co.uk",
		"user+tag@fastmail.com":  "user+tag@fastmail.com",
		"user@protonmail.com":    "user@protonmail.com",
	}
	for in, want := range cases {
		got, ok := normalizeEmail(in)
		if !ok {
			t.Fatalf("%q was rejected", in)
		}
		if got != want {
			t.Fatalf("%q normalised to %q; want it untouched (%q) — this would merge distinct mailboxes",
				in, got, want)
		}
	}
	// The two Outlook spellings must remain DIFFERENT accounts.
	a, _ := normalizeEmail("first.last@outlook.com")
	b, _ := normalizeEmail("firstlast@outlook.com")
	if a == b {
		t.Fatal("dotted and undotted Outlook addresses collapsed; they are different people")
	}
}

// Canonicalization must not resurrect addresses the validator rejects.
func TestInvalidAddressesStillFail(t *testing.T) {
	for _, bad := range []string{"", "not-an-email", "user@localhost", "user@", "@gmail.com", "a@b"} {
		if _, ok := normalizeEmail(bad); ok {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

// A local part that is nothing but a tag is not a real mailbox; do not turn it into
// an empty address that could collide with something else.
func TestTagOnlyLocalPartIsNotCollapsedToEmpty(t *testing.T) {
	got, ok := normalizeEmail("+tag@gmail.com")
	if ok && got == "@gmail.com" {
		t.Fatal("collapsed to a bare domain — that would collide with any other tag-only address")
	}
}
