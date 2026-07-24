package store

import (
	"context"

	"github.com/agent-arena/arena/internal/docs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DocsRepo persists and serves the docs-as-data pages (see internal/docs).
type DocsRepo struct{ db *pgxpool.Pool }

func NewDocsRepo(db *pgxpool.Pool) *DocsRepo { return &DocsRepo{db: db} }

var (
	_ docs.Store      = (*DocsRepo)(nil)
	_ docs.AdminStore = (*DocsRepo)(nil)
)

// Seed upserts every page at a version. Idempotent — safe to run on every startup;
// re-seeding a version overwrites its pages with the current authored content, so the
// git source stays the source of truth (an admin edit to that version is overwritten
// on redeploy by design; edit a NEW version to persist overrides).
func (r *DocsRepo) Seed(ctx context.Context, version string, pages []docs.Page) error {
	batch := &pgx.Batch{}
	for _, p := range pages {
		batch.Queue(
			`INSERT INTO docs_pages (version, slug, title, section, game, category, ord, body_md, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now())
			 ON CONFLICT (version, slug) DO UPDATE SET
			   title=EXCLUDED.title, section=EXCLUDED.section, game=EXCLUDED.game,
			   category=EXCLUDED.category, ord=EXCLUDED.ord, body_md=EXCLUDED.body_md, updated_at=now()`,
			version, p.Slug, p.Title, p.Section, p.Game, p.Category, p.Order, p.Body)
	}
	br := r.db.SendBatch(ctx, batch)
	defer br.Close()
	for range pages {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// UpsertPage inserts or updates a single page in a version (admin edit).
func (r *DocsRepo) UpsertPage(ctx context.Context, version string, p docs.Page) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO docs_pages (version, slug, title, section, game, category, ord, body_md, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now())
		 ON CONFLICT (version, slug) DO UPDATE SET
		   title=EXCLUDED.title, section=EXCLUDED.section, game=EXCLUDED.game,
		   category=EXCLUDED.category, ord=EXCLUDED.ord, body_md=EXCLUDED.body_md, updated_at=now()`,
		version, p.Slug, p.Title, p.Section, p.Game, p.Category, p.Order, p.Body)
	return err
}

// DeletePage removes a page from a version; deleted=false when it didn't exist.
func (r *DocsRepo) DeletePage(ctx context.Context, version, slug string) (bool, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM docs_pages WHERE version=$1 AND slug=$2`, version, slug)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// CloneVersion copies every page from one version into another (admin: fork the
// current docs into a new editable version). Returns the number of pages copied.
// Overwrites same-slug rows already in `to` (so it's safe to re-run).
func (r *DocsRepo) CloneVersion(ctx context.Context, from, to string) (int, error) {
	tag, err := r.db.Exec(ctx,
		`INSERT INTO docs_pages (version, slug, title, section, game, category, ord, body_md, updated_at)
		 SELECT $2, slug, title, section, game, category, ord, body_md, now()
		   FROM docs_pages WHERE version = $1
		 ON CONFLICT (version, slug) DO UPDATE SET
		   title=EXCLUDED.title, section=EXCLUDED.section, game=EXCLUDED.game,
		   category=EXCLUDED.category, ord=EXCLUDED.ord, body_md=EXCLUDED.body_md, updated_at=now()`,
		from, to)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ListFull returns the full pages (WITH bodies) for a version, for admin editing.
func (r *DocsRepo) ListFull(ctx context.Context, version string) ([]docs.Page, error) {
	rows, err := r.db.Query(ctx,
		`SELECT slug, title, section, game, category, ord, body_md
		   FROM docs_pages WHERE version = $1 ORDER BY ord, slug`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []docs.Page
	for rows.Next() {
		var p docs.Page
		if err := rows.Scan(&p.Slug, &p.Title, &p.Section, &p.Game, &p.Category, &p.Order, &p.Body); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPages returns page METADATA (no body) for a version, ordered for the nav.
func (r *DocsRepo) ListPages(ctx context.Context, version string) ([]docs.Page, error) {
	rows, err := r.db.Query(ctx,
		`SELECT slug, title, section, game, category, ord
		   FROM docs_pages WHERE version = $1
		  ORDER BY ord, slug`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []docs.Page
	for rows.Next() {
		var p docs.Page
		if err := rows.Scan(&p.Slug, &p.Title, &p.Section, &p.Game, &p.Category, &p.Order); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPage returns one full page (with body) at a version; ok=false if not found.
func (r *DocsRepo) GetPage(ctx context.Context, version, slug string) (docs.Page, bool, error) {
	var p docs.Page
	err := r.db.QueryRow(ctx,
		`SELECT slug, title, section, game, category, ord, body_md
		   FROM docs_pages WHERE version = $1 AND slug = $2`, version, slug).
		Scan(&p.Slug, &p.Title, &p.Section, &p.Game, &p.Category, &p.Order, &p.Body)
	if err == pgx.ErrNoRows {
		return docs.Page{}, false, nil
	}
	if err != nil {
		return docs.Page{}, false, err
	}
	return p, true, nil
}

// Versions returns the distinct doc versions, newest first (date-string slugs sort
// lexically), plus the latest.
func (r *DocsRepo) Versions(ctx context.Context) (versions []string, latest string, err error) {
	rows, err := r.db.Query(ctx, `SELECT DISTINCT version FROM docs_pages ORDER BY version DESC`)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, "", err
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(versions) > 0 {
		latest = versions[0]
	}
	return versions, latest, nil
}
