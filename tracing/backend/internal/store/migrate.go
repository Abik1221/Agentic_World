package store

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/agent-arena/pyyol-lens/backend/migrations"
)

// Migrate applies any un-applied ClickHouse migrations in filename order. It is
// idempotent and safe to run on every boot: the ClickHouse image only executes
// SQL in /docker-entrypoint-initdb.d on a FIRST-init empty volume, so incremental
// migrations (e.g. 004's benchmark-token columns) would otherwise never reach an
// existing deployment. A schema_migrations table records applied versions so each
// file runs at most once.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.DB.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version String, applied_at DateTime DEFAULT now())
		 ENGINE = MergeTree ORDER BY version`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := s.DB.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()

	entries, err := fs.ReadDir(migrations.ClickHouse, "clickhouse")
	if err != nil {
		return err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		if applied[name] {
			continue
		}
		raw, err := fs.ReadFile(migrations.ClickHouse, "clickhouse/"+name)
		if err != nil {
			return err
		}
		for _, stmt := range splitSQL(string(raw)) {
			if _, err := s.DB.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("migration %s failed on statement %q: %w", name, truncate(stmt, 80), err)
			}
		}
		if _, err := s.DB.ExecContext(ctx,
			`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
	}
	return nil
}

// splitSQL breaks a migration file into individual statements (ClickHouse's Go
// driver executes one statement per call). SQL line comments are stripped FIRST
// (a comment may itself contain ';', e.g. "reasoning/total);"), then the comment-
// free text is split on ';'. Migrations are DDL with no ';' inside literals.
func splitSQL(src string) []string {
	var b strings.Builder
	for _, ln := range strings.Split(src, "\n") {
		if i := strings.Index(ln, "--"); i >= 0 {
			ln = ln[:i] // drop the trailing/whole-line comment
		}
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	var out []string
	for _, chunk := range strings.Split(b.String(), ";") {
		if stmt := strings.TrimSpace(chunk); stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
