package seedadmin

import "testing"

// The parsing rules here decide whether a person can log in, and every failure mode looks
// identical from the outside: "invalid credentials". So they are pinned rather than trusted.

func TestFirstOperatorKeepsTheDefaultID(t *testing.T) {
	// ADMIN_USER_IDS on an existing deployment already names usr_pyyoladmin. If the first
	// entry stopped taking that id, moving from SEED_ADMIN_EMAIL to the list would silently
	// strip the operator's admin rights — the account still logs in and sees nothing.
	ops, err := ParseOperators(`[{"email":"a@pyyol.com","password":"longenough"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if ops[0].UserID != DefaultUserID {
		t.Errorf("first operator got %q, want %q", ops[0].UserID, DefaultUserID)
	}
}

func TestLaterOperatorsGetStableDerivedIDs(t *testing.T) {
	// Deterministic for the same reason DefaultUserID is: the id has to be listable in
	// ADMIN_USER_IDS before the account exists, and must not move between deploys.
	ops, err := ParseOperators(
		`[{"email":"nahom@pyyol.com","password":"longenough"},{"email":"Yeabsira@Pyyol.com","password":"longenough"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if ops[1].UserID != "usr_admin_yeabsira" {
		t.Errorf("second operator id = %q, want usr_admin_yeabsira", ops[1].UserID)
	}
	// Case must not change the id, or an operator who retypes their address in a different
	// case gets a SECOND account and loses their admin rights with no error anywhere.
	again, _ := ParseOperators(`[{"email":"x@x.com","password":"longenough"},{"email":"YEABSIRA@PYYOL.COM","password":"longenough"}]`)
	if again[1].UserID != ops[1].UserID {
		t.Errorf("case changed the id: %q vs %q", again[1].UserID, ops[1].UserID)
	}
}

func TestAnExplicitUserIDWins(t *testing.T) {
	ops, err := ParseOperators(`[{"email":"a@x.com","password":"longenough","user_id":"usr_chosen"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if ops[0].UserID != "usr_chosen" {
		t.Errorf("explicit user_id ignored: %q", ops[0].UserID)
	}
}

func TestEmptyMeansNoOperatorsAndIsNotAnError(t *testing.T) {
	// The normal state of a deployment that seeds nobody. Treating it as an error would make
	// every such deploy log a failure for a setting it deliberately does not use.
	for _, raw := range []string{"", "   ", "\n"} {
		ops, err := ParseOperators(raw)
		if err != nil || len(ops) != 0 {
			t.Errorf("ParseOperators(%q) = %v, %v — want no operators and no error", raw, ops, err)
		}
	}
}

func TestMalformedJSONIsLoud(t *testing.T) {
	// The opposite of the case above, and the reason it must be: seeding nobody because a
	// secret had a stray comma would present as "my login does not work" with nothing in the
	// logs explaining it.
	for _, raw := range []string{`[{"email":"a@x.com",}]`, `not json`, `{"email":"a@x.com"}`} {
		if _, err := ParseOperators(raw); err == nil {
			t.Errorf("ParseOperators(%q) accepted malformed input", raw)
		}
	}
}

func TestAnIncompleteOperatorIsRejected(t *testing.T) {
	// Seeding an account with no password would create a login nobody can use, and a missing
	// email would hash a password against nothing.
	for _, raw := range []string{
		`[{"email":"a@x.com"}]`,
		`[{"password":"longenough"}]`,
		`[{"email":"  ","password":"longenough"}]`,
	} {
		if _, err := ParseOperators(raw); err == nil {
			t.Errorf("ParseOperators(%q) accepted an incomplete operator", raw)
		}
	}
}

func TestARepeatedEmailIsRejected(t *testing.T) {
	// Run matches on email first, so a duplicate would have the last entry overwrite the
	// first's password — one of the two operators cannot log in, with both entries sitting
	// in the secret looking correct.
	_, err := ParseOperators(
		`[{"email":"a@x.com","password":"first-one"},{"email":"A@X.com","password":"second-one"}]`)
	if err == nil {
		t.Error("a repeated email was accepted; one operator would silently lose their password")
	}
}

func TestAPasswordWithSeparatorCharactersSurvives(t *testing.T) {
	// The whole reason this is JSON. A delimited format would have split this password and
	// produced a login failure with nothing pointing at the parser.
	const nasty = `p:a,s;s"w\o|rd 123`
	ops, err := ParseOperators(`[{"email":"a@x.com","password":"p:a,s;s\"w\\o|rd 123"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if ops[0].Password != nasty {
		t.Errorf("password mangled: %q, want %q", ops[0].Password, nasty)
	}
}
