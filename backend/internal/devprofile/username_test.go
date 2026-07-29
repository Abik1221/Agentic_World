package devprofile

import (
	"context"
	"strings"
	"testing"
)

// stubRepo answers only what the username paths need: which handles already exist.
type unameRepo struct {
	taken map[string]bool
	Repo  // embedded so the unused methods are nil — these tests never call them
}

func (r unameRepo) ResolveHandle(_ context.Context, h string) (Identity, bool, error) {
	if r.taken[strings.ToLower(h)] {
		return Identity{}, true, nil
	}
	return Identity{}, false, nil
}

func svcWith(taken ...string) *Service {
	m := map[string]bool{}
	for _, t := range taken {
		m[strings.ToLower(t)] = true
	}
	return &Service{repo: unameRepo{taken: m}}
}

// The field must reject exactly what the save rejects. A green tick followed by a
// failed submit is the single most annoying failure this feature can have.
func TestLiveCheckAgreesWithTheWritePath(t *testing.T) {
	for _, name := range []string{"ab", "has space", "bad-dash", "usr_1234", "admin", "pyyol", strings.Repeat("x", 31)} {
		st, err := svcWith().CheckUsername(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		if st.Available {
			t.Fatalf("%q reported available by the live check", name)
		}
		if st.Reason == "" {
			t.Fatalf("%q rejected with no reason to show the user", name)
		}
		// The write path must refuse it too.
		if validateUsernameShape(strings.TrimPrefix(name, "@")) == "" {
			t.Fatalf("%q passes the write validator but the live check refused it", name)
		}
	}
}

func TestTakenNamesAreNotAvailable(t *testing.T) {
	s := svcWith("nahom")
	st, _ := s.CheckUsername(context.Background(), "nahom")
	if st.Available {
		t.Fatal("an existing handle reported as free")
	}
	// Case must not be a loophole — handles resolve case-insensitively.
	st, _ = s.CheckUsername(context.Background(), "NAHOM")
	if st.Available {
		t.Fatal("case variation of a taken handle reported as free")
	}
}

func TestGoodNameIsAvailable(t *testing.T) {
	st, err := svcWith("someoneelse").CheckUsername(context.Background(), "@nahom_t2")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Available {
		t.Fatalf("valid free name refused: %q", st.Reason)
	}
	if st.Username != "nahom_t2" {
		t.Fatalf("leading @ was not stripped: %q", st.Username)
	}
}

// Reserved names are about impersonation, not aesthetics: @support asking for your
// seed phrase is the expensive version of this bug.
func TestImpersonationProneNamesAreReserved(t *testing.T) {
	for _, n := range []string{"admin", "Support", "BILLING", "moderator", "pyyol", "security"} {
		if reason := validateUsernameShape(n); reason == "" {
			t.Fatalf("%q was allowed", n)
		}
	}
}

// Someone who skips the field still gets a real handle — never a raw database id.
func TestSuggestionPrefersDisplayNameThenEmail(t *testing.T) {
	s := svcWith()
	got, err := s.SuggestUsername(context.Background(), "Nahom T.", "someone@pyyol.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "nahomt" {
		t.Fatalf("got %q, want %q from the display name", got, "nahomt")
	}

	// No usable display name → fall back to the email local part.
	got, err = s.SuggestUsername(context.Background(), "!!!", "nahom.dev@gmail.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "nahomdev" {
		t.Fatalf("got %q, want %q from the email", got, "nahomdev")
	}
}

func TestSuggestionAvoidsCollisions(t *testing.T) {
	s := svcWith("nahomt", "nahomt2")
	got, err := s.SuggestUsername(context.Background(), "Nahom T", "n@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "nahomt3" {
		t.Fatalf("got %q, want nahomt3 — the first two are taken", got)
	}
}

// A suggestion that trips the reserved list or the shape rules would be handed to the
// user as a green tick and then rejected on save.
func TestSuggestionNeverProducesAnInvalidName(t *testing.T) {
	s := svcWith()
	for _, in := range [][2]string{
		{"admin", "admin@x.com"},             // reserved base
		{"", ""},                             // nothing to work with
		{"!!!", "!!!@x.com"},                 // nothing usable
		{"a", "b@x.com"},                     // too short
		{strings.Repeat("z", 60), "c@x.com"}, // too long
	} {
		got, err := s.SuggestUsername(context.Background(), in[0], in[1])
		if err != nil {
			t.Fatalf("no suggestion for %q/%q: %v", in[0], in[1], err)
		}
		if reason := validateUsernameShape(got); reason != "" {
			t.Fatalf("suggested %q which the validator rejects: %s", got, reason)
		}
	}
}
