package store

import (
	"context"
	"testing"

	"github.com/agent-arena/arena/internal/docs"
)

// TestDocsRepoIntegration exercises the full docs-as-data data path against a REAL
// Postgres: docs.Load() (parse embedded content) → DocsRepo.Seed → ListPages / GetPage
// / Versions — the exact queries the /v1/docs API serves the frontend. Skipped unless
// PYYOL_TEST_DATABASE_URL points at a Postgres; the harness migrates the schema itself.
func TestDocsRepoIntegration(t *testing.T) {
	// Shares the harness that MIGRATES the schema. This test used to connect straight
	// to the DSN and assume someone else had migrated, so it failed on a fresh database
	// with "relation docs_pages does not exist" — it only passed if a test that happened
	// to sort earlier had migrated first.
	ctx := context.Background()
	pool := openGroupTestDB(t)

	repo := NewDocsRepo(pool)
	pages, err := docs.Load()
	if err != nil {
		t.Fatalf("docs.Load: %v", err)
	}
	if err := repo.Seed(ctx, docs.DocsVersion, pages); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	// Idempotent: seeding again must not error or duplicate (PK guards it).
	if err := repo.Seed(ctx, docs.DocsVersion, pages); err != nil {
		t.Fatalf("Seed (2nd): %v", err)
	}

	list, err := repo.ListPages(ctx, docs.DocsVersion)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(list) != len(pages) {
		t.Fatalf("ListPages returned %d, seeded %d", len(list), len(pages))
	}
	// ListPages must NOT carry bodies (nav is metadata-only).
	for _, p := range list {
		if p.Body != "" {
			t.Errorf("ListPages leaked a body for %q", p.Slug)
		}
	}

	// A known nested page round-trips WITH its body.
	pg, ok, err := repo.GetPage(ctx, docs.DocsVersion, "games/goofspiel")
	if err != nil || !ok {
		t.Fatalf("GetPage games/goofspiel: ok=%v err=%v", ok, err)
	}
	if pg.Title != "Goofspiel" || pg.Section != "Games" || pg.Game != "goofspiel" {
		t.Errorf("metadata wrong: %+v", pg)
	}
	if len(pg.Body) < 50 {
		t.Errorf("body not persisted (len=%d)", len(pg.Body))
	}

	// Missing page → not found (no error).
	if _, ok, err := repo.GetPage(ctx, docs.DocsVersion, "nope/missing"); ok || err != nil {
		t.Errorf("missing page: ok=%v err=%v", ok, err)
	}

	versions, latest, err := repo.Versions(ctx)
	if err != nil || latest != docs.DocsVersion || len(versions) == 0 {
		t.Fatalf("Versions: versions=%v latest=%q err=%v", versions, latest, err)
	}

	// --- admin write path (CRUD without redeploy) ---
	const editVer = "9999-01-01-test" // isolated version, never clobbered by the seed
	// Remove it afterwards. The version string deliberately sorts after every real one
	// so the "latest" assertion below works — which also means leaving it behind makes
	// it the latest version FOREVER, and the earlier `latest == docs.DocsVersion` check
	// then fails on every subsequent run against the same database.
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM docs_pages WHERE version = $1`, editVer); err != nil {
			t.Errorf("cleanup of %s failed; later runs of this test will fail: %v", editVer, err)
		}
	})
	// Clone the seeded baseline into a new editable version.
	n, err := repo.CloneVersion(ctx, docs.DocsVersion, editVer)
	if err != nil || n != len(pages) {
		t.Fatalf("CloneVersion: n=%d want %d err=%v", n, len(pages), err)
	}
	// Upsert a brand-new page into it.
	if err := repo.UpsertPage(ctx, editVer, docs.Page{
		Slug: "concepts/edited", Title: "Edited", Section: "Concepts", Order: 9, Body: "# edited\n",
	}); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
	full, err := repo.ListFull(ctx, editVer)
	if err != nil || len(full) != len(pages)+1 {
		t.Fatalf("ListFull after upsert: got %d want %d err=%v", len(full), len(pages)+1, err)
	}
	// Delete it again.
	deleted, err := repo.DeletePage(ctx, editVer, "concepts/edited")
	if err != nil || !deleted {
		t.Fatalf("DeletePage: deleted=%v err=%v", deleted, err)
	}
	if again, _ := repo.DeletePage(ctx, editVer, "concepts/edited"); again {
		t.Error("second delete should report deleted=false")
	}
	// The edit version is now selectable as latest (date-string sorts after baseline).
	if _, latest2, _ := repo.Versions(ctx); latest2 != editVer {
		t.Errorf("latest after edit = %q, want %q", latest2, editVer)
	}
}

// TestDocsSeedDoesNotRevertAdminEdits pins the fix for a silent data-loss bug.
//
// Seed runs on EVERY boot, at the constant docs.DocsVersion, with ON CONFLICT DO UPDATE.
// So an admin who corrected a page through /v1/admin/docs/pages saw their edit survive
// until the next restart and then revert to the embedded copy — no error, no log, just
// the old text back. Migration 0058 explicitly promised the opposite ("an admin can later
// edit a row to override a page without a redeploy").
//
// The guard is `WHERE NOT docs_pages.admin_edited` on the seeder's update. Remove it and
// this test fails on the body assertion, which is the point of writing it this way rather
// than asserting on the column.
func TestDocsSeedDoesNotRevertAdminEdits(t *testing.T) {
	ctx := context.Background()
	pool := openGroupTestDB(t)
	repo := NewDocsRepo(pool)

	pages, err := docs.Load()
	if err != nil {
		t.Fatalf("docs.Load: %v", err)
	}
	const ver = "seed-guard-test"
	if err := repo.Seed(ctx, ver, pages); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM docs_pages WHERE version=$1`, ver)
	})

	// An admin corrects a page that the embedded content also ships.
	const slug = "research/p-index-paper"
	const corrected = "# Corrected by an operator\n\nThis text came from the admin API.\n"
	orig, ok, err := repo.GetPage(ctx, ver, slug)
	if err != nil || !ok {
		t.Fatalf("the seeded corpus must contain %s: ok=%v err=%v", slug, ok, err)
	}
	if err := repo.UpsertPage(ctx, ver, docs.Page{
		Slug: slug, Title: orig.Title, Section: orig.Section, Order: orig.Order, Body: corrected,
	}); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	// A deploy happens. The seeder runs again over the same version.
	if err := repo.Seed(ctx, ver, pages); err != nil {
		t.Fatalf("re-Seed: %v", err)
	}

	got, ok, err := repo.GetPage(ctx, ver, slug)
	if err != nil || !ok {
		t.Fatalf("GetPage after re-seed: ok=%v err=%v", ok, err)
	}
	if got.Body != corrected {
		t.Errorf("the boot seeder reverted an admin edit.\n got: %.60q\nwant: %.60q", got.Body, corrected)
	}

	// A page the admin never touched must still update on deploy, or the guard would have
	// frozen the entire corpus rather than just the edited row.
	untouched, ok, err := repo.GetPage(ctx, ver, "games/goofspiel")
	if err != nil || !ok {
		t.Fatalf("GetPage(games/goofspiel): ok=%v err=%v", ok, err)
	}
	if untouched.Body == corrected || untouched.Body == "" {
		t.Error("an un-edited page should still carry the embedded content after a re-seed")
	}
}

// TestDocsSeedKeepsAdminCreatedPages pins Super Admin create surviving a deploy.
// pruneMissing used to DELETE every slug the embedded corpus does not ship, which
// made an operator-created page vanish on the next backend restart.
func TestDocsSeedKeepsAdminCreatedPages(t *testing.T) {
	ctx := context.Background()
	pool := openGroupTestDB(t)
	repo := NewDocsRepo(pool)

	pages, err := docs.Load()
	if err != nil {
		t.Fatalf("docs.Load: %v", err)
	}
	const ver = "seed-admin-create-test"
	if err := repo.Seed(ctx, ver, pages); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM docs_pages WHERE version=$1`, ver)
	})

	created := docs.Page{
		Slug: "concepts/operator-note", Title: "Operator note",
		Section: "Concepts", Order: 99, Body: "# Created in the console\n",
	}
	if err := repo.UpsertPage(ctx, ver, created); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	if err := repo.Seed(ctx, ver, pages); err != nil {
		t.Fatalf("re-Seed: %v", err)
	}

	got, ok, err := repo.GetPage(ctx, ver, created.Slug)
	if err != nil || !ok {
		t.Fatalf("admin-created page was pruned on re-seed: ok=%v err=%v", ok, err)
	}
	if got.Body != created.Body {
		t.Errorf("admin-created body changed on re-seed:\n got: %.60q\nwant: %.60q", got.Body, created.Body)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO docs_pages (version, slug, title, section, ord, body_md, updated_at, admin_edited)
		VALUES ($1, 'games/withdrawn', 'Withdrawn', 'Games', 0, 'gone', now(), false)`, ver); err != nil {
		t.Fatalf("insert withdrawn slug: %v", err)
	}
	if err := repo.Seed(ctx, ver, pages); err != nil {
		t.Fatalf("re-Seed after withdrawn insert: %v", err)
	}
	if _, ok, err := repo.GetPage(ctx, ver, "games/withdrawn"); err != nil || ok {
		t.Fatalf("un-edited withdrawn slug should be pruned: ok=%v err=%v", ok, err)
	}
}
