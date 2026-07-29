package devprofile

import (
	"context"
	"fmt"
	"strings"
)

// Username rules, in one place so the check the UI runs while you type and the check
// the server runs when you save can never disagree. Two implementations of "is this
// name allowed" is how you get a green tick followed by a rejection.

// reservedUsernames are names nobody may claim.
//
// Three reasons a name lands here, and they are different problems:
//
//   - ROUTE COLLISION. /docs, /login, /wallet and friends are real pages. A profile at
//     /u/<name> does not collide today, but reserving them costs nothing and protects
//     against the day someone shortens profile URLs to /<name> — a decision that would
//     otherwise be blocked by whoever grabbed "settings" first.
//   - IMPERSONATION. admin, support, billing, moderator, staff, official. Someone
//     called @support asking for your seed phrase is the oldest trick there is, and on
//     a platform holding real money it is the expensive one.
//   - PLATFORM IDENTITY. pyyol and its obvious variants.
//
// Matching is case-insensitive; the caller lowercases first.
var reservedUsernames = map[string]bool{
	// platform identity
	"pyyol": true, "pyyolapp": true, "pyyolteam": true, "pyyolofficial": true,
	"team": true, "official": true, "staff": true, "arena": true,
	// impersonation-prone
	"admin": true, "administrator": true, "root": true, "superuser": true, "sysadmin": true,
	"support": true, "help": true, "helpdesk": true, "billing": true, "payments": true,
	"moderator": true, "mod": true, "security": true, "abuse": true, "legal": true,
	"noreply": true, "no_reply": true, "postmaster": true, "webmaster": true,
	// routes and reserved paths
	"api": true, "docs": true, "doc": true, "login": true, "logout": true, "signin": true,
	"signup": true, "register": true, "settings": true, "profile": true, "dashboard": true,
	"wallet": true, "withdrawals": true, "deposits": true, "leaderboard": true,
	"leaderboards": true, "rankings": true, "models": true, "clips": true, "watch": true,
	"lobby": true, "sandbox": true, "traces": true, "benchmark": true, "deploy": true,
	"onboarding": true, "verify": true, "welcome": true, "privacy": true, "terms": true,
	"about": true, "contact": true, "status": true, "health": true, "healthz": true,
	"static": true, "assets": true, "public": true, "internal": true, "system": true,
	// games — a handle that looks like a game section invites confusion
	"mafia": true, "goofspiel": true, "monopoly": true, "games": true, "game": true,
	// generic placeholders people try
	"null": true, "undefined": true, "none": true, "test": true, "user": true, "me": true,
	"you": true, "anonymous": true, "unknown": true, "guest": true, "bot": true,
}

// UsernameStatus is the machine-readable outcome of a check. The UI turns Available
// into the green/red flag; Reason is shown verbatim, so it is written for a person.
type UsernameStatus struct {
	Username  string `json:"username"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// CheckUsername reports whether a username can be claimed, and why not when it cannot.
//
// It runs the SAME validation as SetUsername plus a uniqueness lookup, so what the
// field says while you type is what the server will do when you submit.
func (s *Service) CheckUsername(ctx context.Context, raw string) (UsernameStatus, error) {
	username := strings.TrimPrefix(strings.TrimSpace(raw), "@")
	out := UsernameStatus{Username: username}

	if reason := validateUsernameShape(username); reason != "" {
		out.Reason = reason
		return out, nil
	}

	// Taken? ResolveHandle matches username OR user public id, which is exactly the
	// collision we care about: if a handle would resolve to somebody, it is not free.
	if _, found, err := s.repo.ResolveHandle(ctx, username); err != nil {
		return UsernameStatus{}, err
	} else if found {
		out.Reason = "that username is taken"
		return out, nil
	}

	out.Available = true
	return out, nil
}

// validateUsernameShape returns a human-readable reason, or "" when the name is fine.
// Shared by the live check and the write path — see the note at the top of the file.
func validateUsernameShape(username string) string {
	switch {
	case username == "":
		return "pick a username"
	case len(username) < 3:
		return "at least 3 characters"
	case len(username) > 30:
		return "at most 30 characters"
	case !usernameRe.MatchString(username):
		return "letters, digits and underscore only"
	}
	lower := strings.ToLower(username)
	// A public id (usr_…/agt_…) is a valid username shape and appears in URLs, so a
	// username must not impersonate one — otherwise a handle resolves to two rows.
	if strings.HasPrefix(lower, "usr_") || strings.HasPrefix(lower, "agt_") {
		return "can't start with usr_ or agt_"
	}
	if reservedUsernames[lower] {
		return "that username is reserved"
	}
	return ""
}

// SuggestUsername derives a usable handle from whatever identity we already have, so
// a developer who skips the field still gets a real @handle instead of a raw database
// id showing up as their name across the platform.
//
// Preference order is deliberate: display name first because it is what the person
// chose to be called, email local-part second because it is stable and usually
// recognisable. The public id is never used as a base — surfacing "usr_01H8XK" as
// somebody's handle is the exact problem this exists to prevent.
//
// Uniqueness is resolved by appending a number, checked against the repo. `taken`
// lets callers pre-seed names claimed earlier in the same transaction.
func (s *Service) SuggestUsername(ctx context.Context, displayName, email string) (string, error) {
	base := sanitizeUsernameBase(displayName)
	if validateUsernameShape(base) != "" {
		if at := strings.IndexByte(email, '@'); at > 0 {
			base = sanitizeUsernameBase(email[:at])
		}
	}
	if validateUsernameShape(base) != "" {
		base = "player"
	}

	// Try the bare name, then name2, name3… Bounded: an unbounded loop against a
	// popular base would hammer the database on every skipped signup.
	for i := 0; i < 50; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s%d", base, i+1)
			if len(candidate) > 30 {
				candidate = fmt.Sprintf("%s%d", base[:30-len(fmt.Sprint(i+1))], i+1)
			}
		}
		if validateUsernameShape(candidate) != "" {
			continue
		}
		_, found, err := s.repo.ResolveHandle(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !found {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not derive a free username from %q", base)
}

// sanitizeUsernameBase reduces arbitrary text to the allowed alphabet.
//
// Spaces and punctuation become nothing rather than underscores: "Nahom T." reads
// better as "nahomt" than "nahom_t_", and trailing separators look like a mistake.
// Digits are kept — plenty of people have them in the name they already use.
func sanitizeUsernameBase(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_':
			b.WriteRune(r)
		}
		if b.Len() >= 30 {
			break
		}
	}
	return b.String()
}
