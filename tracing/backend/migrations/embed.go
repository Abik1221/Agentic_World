// Package migrations embeds the ClickHouse schema migrations so any service can
// apply them on boot (there is no external migration runner — the docker
// entrypoint only runs SQL on a first-init empty volume, so incremental
// migrations must be applied in-process). See store.Store.Migrate.
package migrations

import "embed"

//go:embed clickhouse/*.sql
var ClickHouse embed.FS
