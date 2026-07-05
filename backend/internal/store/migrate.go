package store

import (
	"errors"
	"fmt"

	"github.com/agent-arena/arena/migrations"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // registers the "postgres" driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Migrate applies all pending SQL migrations to databaseURL, in version order,
// from the embedded migration files (migrations.FS). It is the auto-migrate path
// the server runs on startup.
//
// Safe to run on every instance concurrently: golang-migrate takes a Postgres
// advisory lock, so simultaneous callers serialize and each version is applied
// exactly once. Returns nil when the schema is already at head. This is the same
// migration set used by `make migrate` and the docker-compose migrate service, so
// the schema_migrations bookkeeping stays consistent across all three paths.
func Migrate(databaseURL string) error {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("migrate: open embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, databaseURL)
	if err != nil {
		return fmt.Errorf("migrate: init: %w", err)
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate: apply: %w", err)
	}
	return nil
}
