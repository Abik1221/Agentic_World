package devprofile

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/bloom"
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
//
// # Why a Bloom filter sits in front of the lookup
//
// This backs a live green/red flag under a text input, on a PUBLIC, UNAUTHENTICATED
// route with no rate limit. The client debounces at 350ms; nothing else does. One
// indexed lookup per keystroke is fine for one person and is a free amplifier for
// anyone who wants to point a loop at it — the request is cheap to send and costs us
// a database round trip every time.
//
// So the common answer is served from memory. A Bloom filter has no false negatives,
// which is what makes this sound: if the filter does not contain the name, it is
// DEFINITELY unclaimed and we can say so without touching the database. A hit means
// "probably taken" and pays for the one lookup that settles it.
//
// # What the filter is allowed to get wrong, and what happens then
//
// Staleness is the only hazard: a name claimed on ANOTHER instance since the last
// rebuild is absent from this filter, so the field would show green for a name that
// is gone. Three things bound that:
//
//   - writes on THIS instance are added immediately (see noteUsernameClaimed), so the
//     common single-instance case is exact;
//   - the filter is rebuilt on a timer, so another instance's writes land quickly;
//   - the database keeps a UNIQUE constraint, so the submit fails cleanly with
//     username_taken rather than two people sharing a handle.
//
// That last point is the important one: the filter can only ever cost somebody a
// rejected submit — which already happens today when two people race for the same
// name — and never a wrong claim.
func (s *Service) CheckUsername(ctx context.Context, raw string) (UsernameStatus, error) {
	username := strings.TrimPrefix(strings.TrimSpace(raw), "@")
	out := UsernameStatus{Username: username}

	if reason := validateUsernameShape(username); reason != "" {
		out.Reason = reason
		return out, nil
	}

	// THE FAST PATH. Lowercased because users.username is citext, so the filter is
	// built lowercased and has to be probed the same way.
	if f := s.usernames.Load(); f != nil && !f.Has(strings.ToLower(username)) {
		out.Available = true
		return out, nil
	}

	// Filter miss, or no filter yet (cold start): ask the database. ResolveHandle
	// matches username OR user public id, which is exactly the collision we care
	// about: if a handle would resolve to somebody, it is not free.
	if _, found, err := s.repo.ResolveHandle(ctx, username); err != nil {
		return UsernameStatus{}, err
	} else if found {
		out.Reason = "that username is taken"
		return out, nil
	}

	out.Available = true
	return out, nil
}

// RunUsernameFilter keeps the availability filter warm for the life of the process.
//
// Built once at start so the very first keystroke is already fast, then rebuilt on an
// interval. The interval is what bounds cross-instance staleness: a name claimed on
// another replica is invisible here until the next rebuild, and the cost of being wrong
// is a rejected submit that the database's UNIQUE constraint catches cleanly.
//
// A failed rebuild is logged and retried on the next tick rather than escalated: the
// previous filter stays in place, and the worst case is falling back to the database
// lookup this exists to avoid. Refusing to serve availability at all because a cache
// could not refresh would be the wrong trade.
func (s *Service) RunUsernameFilter(ctx context.Context, every time.Duration, log *slog.Logger) {
	if every <= 0 {
		every = 5 * time.Minute
	}
	build := func() {
		start := time.Now()
		if err := s.RefreshUsernameFilter(ctx); err != nil {
			log.Warn("username filter: rebuild failed; keeping the previous one",
				"error", err, "retry_in", every)
			return
		}
		if f := s.usernames.Load(); f != nil {
			log.Info("username filter rebuilt",
				"names", f.Len(), "bits", f.Bits(), "hashes", f.Hashes(),
				"kib", f.Bits()/8/1024, "took", time.Since(start))
		}
	}
	build()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			build()
		}
	}
}

// RefreshUsernameFilter rebuilds the availability filter from the database.
//
// Rebuilt rather than mutated because a Bloom filter cannot delete: a username that is
// released (an account closed, a handle changed) would stay "taken" forever otherwise.
// The new filter is swapped in atomically, so readers never block and never observe a
// half-built one.
//
// Sized from the CURRENT row count with headroom, because a filter sized for the set it
// held at boot degrades as the platform grows — and a filter whose false-positive rate
// has crept up quietly stops saving anything.
func (s *Service) RefreshUsernameFilter(ctx context.Context) error {
	names, err := s.repo.AllUsernames(ctx)
	if err != nil {
		return err
	}
	// 2× headroom + a floor, so a young deployment is not sized for a handful of names
	// and the filter does not need rebuilding the moment anyone signs up.
	f := bloom.New(uint64(len(names))*2+1024, usernameFilterFPR)
	for _, n := range names {
		f.Add(n)
	}
	s.usernames.Store(f)
	return nil
}

// noteUsernameClaimed records a just-claimed name so this instance never offers it
// again, without waiting for the next rebuild.
//
// Adds to a COPY-ON-WRITE basis is not needed here: Add only ever sets bits, and a
// concurrent reader seeing a bit set early would at worst take the slow path and ask
// the database. Setting a bit can never produce a false negative, which is the only
// error that would matter.
func (s *Service) noteUsernameClaimed(username string) {
	if f := s.usernames.Load(); f != nil {
		f.Add(strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@")))
	}
}

// usernameFilterFPR is the target false-positive rate: 1 in 200 checks pays for a
// database lookup it did not need. Tighter than this buys little — the lookup is
// indexed and cheap; the point is removing the other 199.
const usernameFilterFPR = 0.005

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
