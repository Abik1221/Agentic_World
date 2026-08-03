// Command seed-admin creates (or repairs) the operator's own login on the arena, by hand.
//
// The server does this at boot when SEED_ADMIN_EMAIL + SEED_ADMIN_PASSWORD are set (which is
// how CI/CD provisions it), so this command is for running it against a database directly —
// a local stack, a restored backup, or an account lockout you want to fix without a deploy.
//
// Both paths share internal/seedadmin, so they cannot drift. See that package for the
// reasoning: the deterministic public id, the idempotency, and why the pepper is mandatory.
//
// CREDENTIALS COME FROM THE ENVIRONMENT, never from source. An admin password committed to a
// repository is in the history of every clone, permanently, on a platform that moves real
// money — so nothing here is defaulted and the command fails loudly instead.
//
// Usage:
//
//	DATABASE_URL=… API_KEY_PEPPER=… SEED_ADMIN_EMAIL=… SEED_ADMIN_PASSWORD=… seed-admin
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/seedadmin"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seed-admin:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		dsn      = flag.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN (or $DATABASE_URL)")
		email    = flag.String("email", os.Getenv("SEED_ADMIN_EMAIL"), "operator login (or $SEED_ADMIN_EMAIL)")
		password = flag.String("password", os.Getenv("SEED_ADMIN_PASSWORD"), "operator password (or $SEED_ADMIN_PASSWORD)")
		userID   = flag.String("user-id", os.Getenv("SEED_ADMIN_USER_ID"), "the account's public id; must appear in ADMIN_USER_IDS (default "+seedadmin.DefaultUserID+")")
		pepper   = flag.String("pepper", os.Getenv("API_KEY_PEPPER"), "the server's API_KEY_PEPPER (or $API_KEY_PEPPER)")
	)
	flag.Parse()

	if strings.TrimSpace(*dsn) == "" {
		return errors.New("DATABASE_URL (or -dsn) is required")
	}
	if strings.TrimSpace(*email) == "" || strings.TrimSpace(*password) == "" {
		return errors.New("SEED_ADMIN_EMAIL and SEED_ADMIN_PASSWORD are both required " +
			"(pass them as secrets; they are deliberately not defaulted)")
	}
	if strings.TrimSpace(*pepper) == "" {
		return errors.New("API_KEY_PEPPER (or -pepper) is required, and must be the value the " +
			"server runs with — a password hashed with a different pepper can never be verified")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}

	res, err := seedadmin.Run(ctx, pool, *email, *password, *userID, *pepper)
	if err != nil {
		return err
	}

	verb := "updated"
	if res.Created {
		verb = "created"
	}
	fmt.Printf("seed-admin: %s operator account %s (%s)\n", verb, res.UserPublicID, strings.ToLower(strings.TrimSpace(*email)))
	fmt.Printf("seed-admin: ADMIN_USER_IDS must contain %q for this account to hold admin rights\n", res.UserPublicID)
	return nil
}
