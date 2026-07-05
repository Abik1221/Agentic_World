// Package store is the single owner of database and cache drivers. Every other
// module receives typed repositories / health checks from here and never imports
// pgx or go-redis directly. This keeps the data layer swappable and the rest of
// the codebase driver-agnostic.
package store

import (
	"context"
	"fmt"

	"github.com/agent-arena/arena/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Store aggregates the data-layer connections and exposes health checks. sqlc
// query structs hang off this type as the project grows (q := store.Queries()).
type Store struct {
	DB    *pgxpool.Pool
	Redis *redis.Client
}

// Open establishes (and verifies) the Postgres pool and Redis client. It fails
// fast: if either dependency is unreachable, the process should not start.
func Open(ctx context.Context, cfg *config.Config) (*Store, error) {
	db, err := openPostgres(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	rdb, err := openRedis(ctx, cfg)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("redis: %w", err)
	}
	return &Store{DB: db, Redis: rdb}, nil
}

// Ping verifies both data dependencies; used by the readiness probe.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.DB.Ping(ctx); err != nil {
		return fmt.Errorf("postgres ping: %w", err)
	}
	if err := s.Redis.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	return nil
}

// MigrationsApplied reports whether golang-migrate has applied at least one
// migration and the schema is not in a dirty (failed-migration) state. The
// readiness probe uses this so an instance with an un-migrated DB never serves.
func (s *Store) MigrationsApplied(ctx context.Context) (bool, error) {
	const q = `SELECT version, dirty FROM schema_migrations LIMIT 1`
	var version int64
	var dirty bool
	err := s.DB.QueryRow(ctx, q).Scan(&version, &dirty)
	if err != nil {
		// No row / missing table => migrations have not been applied yet.
		return false, nil //nolint:nilerr // absence is a clean "not ready", not an error
	}
	return !dirty && version > 0, nil
}

// Close releases all data-layer resources. Safe to call during shutdown.
func (s *Store) Close() {
	if s.Redis != nil {
		_ = s.Redis.Close()
	}
	if s.DB != nil {
		s.DB.Close()
	}
}
