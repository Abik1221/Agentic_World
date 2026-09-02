package seedadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Seeding MORE THAN ONE operator.
//
// Run seeds a single account, which was right when a deployment had one operator and wrong
// as soon as it had two: the second person had to be created by hand against the production
// database, which is the exact problem this package exists to remove.
//
// # Why a JSON list rather than SEED_ADMIN_2_EMAIL and friends
//
// Indexed variables do not scale and they encode the count in the deploy script, so adding a
// third operator means editing the pipeline. More importantly, a delimited string cannot
// carry a password: passwords contain colons, commas, semicolons and everything else anyone
// picks as a separator, and a scheme that mangles a credential fails as a login error with
// nothing pointing at the parser.
//
// JSON has one escaping rule, it survives any password, and it is one secret to rotate.
//
// # Passwords do not belong in this repository
//
// SEED_ADMIN_OPERATORS is a CI SECRET. A credential committed here is disclosed to everyone
// who can read the repository the moment it is pushed, and deleting it later does not help:
// the git history keeps it and anyone who already cloned has it. That holds whatever the
// repository's visibility is today, because visibility can change and the history does not.
// Nothing in this package reads a literal, and nothing should ever be added that does.

// Operator is one account to seed.
type Operator struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	// UserID is optional. Omitted, the FIRST operator takes DefaultUserID (so an existing
	// single-admin deployment keeps the id its ADMIN_USER_IDS already names) and later ones
	// get a deterministic id derived from the email — see idFor.
	UserID string `json:"user_id,omitempty"`
}

// ParseOperators reads the SEED_ADMIN_OPERATORS JSON list.
//
// An empty or blank value is not an error: it means "no operators configured", which is the
// normal state of a deployment that seeds nobody. A MALFORMED value is an error, loudly —
// silently seeding nobody because a secret had a stray comma would present as "my login does
// not work" with nothing in the logs to explain it.
func ParseOperators(raw string) ([]Operator, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var ops []Operator
	if err := json.Unmarshal([]byte(raw), &ops); err != nil {
		return nil, fmt.Errorf("seedadmin: SEED_ADMIN_OPERATORS is not valid JSON: %w", err)
	}
	seen := map[string]bool{}
	for i := range ops {
		ops[i].Email = strings.ToLower(strings.TrimSpace(ops[i].Email))
		ops[i].UserID = strings.TrimSpace(ops[i].UserID)
		if ops[i].Email == "" || ops[i].Password == "" {
			return nil, fmt.Errorf("seedadmin: operator %d needs both an email and a password", i)
		}
		// A repeated email would have the second entry silently overwrite the first's
		// password, so whichever operator is listed last wins and the other cannot log in —
		// with both entries present in the secret and looking correct.
		if seen[ops[i].Email] {
			return nil, fmt.Errorf("seedadmin: %s appears twice", ops[i].Email)
		}
		seen[ops[i].Email] = true

		if ops[i].UserID == "" {
			if i == 0 {
				ops[i].UserID = DefaultUserID
			} else {
				ops[i].UserID = idFor(ops[i].Email)
			}
		}
	}
	return ops, nil
}

// idFor derives a stable public id from an email.
//
// Deterministic for the same reason DefaultUserID is: ADMIN_USER_IDS has to be settable
// before the account exists, and the id must not move between deploys or the allowlist stops
// matching and a working login can suddenly see nothing.
//
// The local part only, lowercased, with anything outside [a-z0-9] dropped. The domain is left
// out because every operator here shares one, so including it would make every id the same
// length of noise with two useful characters in it.
func idFor(email string) string {
	local := email
	if at := strings.IndexByte(local, '@'); at >= 0 {
		local = local[:at]
	}
	var b strings.Builder
	b.WriteString("usr_admin_")
	for _, r := range strings.ToLower(local) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// RunAll seeds every operator and reports what happened to each.
//
// It does NOT stop at the first failure. One bad entry — a password under the minimum, an
// address with a typo — must not deny the other operators their accounts, and the caller
// needs to know exactly which one failed rather than that "seeding failed".
func RunAll(ctx context.Context, pool *pgxpool.Pool, ops []Operator, pepper string) ([]Result, error) {
	var out []Result
	var errs []error
	for _, op := range ops {
		res, err := Run(ctx, pool, op.Email, op.Password, op.UserID, pepper)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", op.Email, err))
			continue
		}
		out = append(out, res)
	}
	return out, errors.Join(errs...)
}
