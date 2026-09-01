package docs

import "testing"

func TestParsePage_Frontmatter(t *testing.T) {
	raw := "---\ntitle: Your First Agent\nsection: Getting Started\norder: 3\ngame: goofspiel\n---\n# Body\n\nhello\n"
	p := parsePage("getting-started/first-agent", raw)
	if p.Slug != "getting-started/first-agent" {
		t.Errorf("slug = %q", p.Slug)
	}
	if p.Title != "Your First Agent" || p.Section != "Getting Started" || p.Game != "goofspiel" || p.Order != 3 {
		t.Errorf("frontmatter parsed wrong: %+v", p)
	}
	if p.Body != "# Body\n\nhello\n" {
		t.Errorf("body = %q", p.Body)
	}
}

func TestParsePage_NoFrontmatter(t *testing.T) {
	p := parsePage("x", "# Just markdown\n")
	if p.Title != "x" || p.Body != "# Just markdown\n" {
		t.Errorf("no-frontmatter page parsed wrong: %+v", p)
	}
}

func TestLoad_EmbeddedContent(t *testing.T) {
	pages, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) < 8 {
		t.Fatalf("expected the authored content set, got %d pages", len(pages))
	}
	by := map[string]Page{}
	for _, p := range pages {
		if p.Title == "" || p.Section == "" {
			t.Errorf("page %q missing title/section: %+v", p.Slug, p)
		}
		by[p.Slug] = p
	}
	// Spot-check known pages exist with correct metadata.
	goof, ok := by["games/goofspiel"]
	if !ok || goof.Section != "Games" || goof.Game != "goofspiel" {
		t.Errorf("games/goofspiel wrong or missing: %+v", goof)
	}
	if _, ok := by["sdk/verified-telemetry"]; !ok {
		t.Error("the verified-telemetry page must be in the corpus")
	}
	// The P-Index method paper. Pinned by slug because the public /p-index/paper route
	// fetches exactly this one: rename the file and that page silently renders its
	// not-published state, which for a document whose whole purpose is "you can audit how
	// we score you" is indistinguishable from never having published it.
	paper, ok := by["research/p-index-paper"]
	if !ok || paper.Section != "Research" {
		t.Errorf("research/p-index-paper wrong or missing: %+v", paper)
	}
	// A section nobody ranked sorts to 100 alongside every typo. Research is ranked, so
	// this asserts the registry entry exists rather than that the map has some value.
	if SectionRank("Research") >= 100 {
		t.Error("the Research section must be ranked in sectionOrder, not left to sort last by default")
	}
	// Sections are ordered: Getting Started before Games.
	seenGames := false
	for _, p := range pages {
		if p.Section == "Games" {
			seenGames = true
		}
		if p.Section == "Getting Started" && seenGames {
			t.Error("Getting Started must sort before Games")
		}
	}
}
