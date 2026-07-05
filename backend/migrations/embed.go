// Package migrations embeds the SQL migration files so the server can apply them
// in-process on startup (auto-migrate) without needing the files on disk or a
// separate `migrate` step. The same files are still used by `make migrate` and
// the docker-compose migrate service; this embed just makes them available to the
// binary. See internal/store.Migrate.
package migrations

import "embed"

// FS holds every NNNN_name.(up|down).sql migration, in version order.
//
//go:embed *.sql
var FS embed.FS
