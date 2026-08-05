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
