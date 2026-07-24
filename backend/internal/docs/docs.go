// Package docs is the platform's docs-as-data engine. Documentation is authored as
// modular Markdown files (the source of truth, under content/, versioned in git),
// each with a small frontmatter header. At startup the arena SEEDS these into the
// docs_pages table (admin data) at the current DocsVersion — so the frontend serves
// versioned, structured docs from the API instead of hardcoding Markdown, and an
// admin can later edit/override a page in the DB without a redeploy.
//
// Why this shape (industry standard): docs-as-code keeps docs reviewable next to the
// code they describe and impossible to silently drift; the DB projection makes them
// queryable, versioned, and editable by the app. Parsing is pure (no I/O, no deps)
// so it is trivially unit-tested.
package docs

import (
	"embed"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// DocsVersion stamps every seeded page. Bump on a docs release so the frontend can
// pin/select a version and older snapshots stay addressable.
const DocsVersion = "2026-07-24"

//go:embed content
var contentFS embed.FS

// Page is one documentation page: a modular unit keyed by slug, grouped into a
// section (and optionally a game), ordered for the nav.
type Page struct {
	Slug     string `json:"slug"`               // e.g. "games/goofspiel" (path-derived, stable)
	Title    string `json:"title"`              // H1 / nav label
	Section  string `json:"section"`            // top-level nav group, e.g. "Games"
	Game     string `json:"game,omitempty"`     // goofspiel|mafia|monopoly, when game-specific
	Category string `json:"category,omitempty"` // optional finer grouping within a section
	Order    int    `json:"order"`              // sort key within the section
	Body     string `json:"body"`               // Markdown body (frontmatter stripped)
}

// Load parses the embedded content into ordered pages. Deterministic (sorted by
// section order then page order then slug) so the seed + nav are stable.
func Load() ([]Page, error) {
	return parse(contentFS, "content")
}

func parse(fsys fs.FS, root string) ([]Page, error) {
	var pages []Page
	err := fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		raw, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		slug := strings.TrimSuffix(strings.TrimPrefix(path, root+"/"), ".md")
		pages = append(pages, parsePage(slug, string(raw)))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(pages, func(i, j int) bool {
		if pages[i].sectionRank() != pages[j].sectionRank() {
			return pages[i].sectionRank() < pages[j].sectionRank()
		}
		if pages[i].Order != pages[j].Order {
			return pages[i].Order < pages[j].Order
		}
		return pages[i].Slug < pages[j].Slug
	})
	return pages, nil
}

// sectionOrder pins the top-level nav order; unknown sections sort last (alpha).
var sectionOrder = map[string]int{
	"Getting Started": 0,
	"SDK":             1,
	"Games":           2,
	"Ranked":          3,
	"Protocol":        4,
}

func (p Page) sectionRank() int { return SectionRank(p.Section) }

// SectionRank is the nav order of a top-level section; unknown sections sort last.
// Exported so the API handler can order sections consistently even when reading from
// the DB (where a flat ORDER BY can't know the section ranking).
func SectionRank(section string) int {
	if r, ok := sectionOrder[section]; ok {
		return r
	}
	return 100
}

// parsePage splits optional `---` frontmatter from the Markdown body and reads the
// known keys. Frontmatter is a tiny key: value block (no nested YAML), so we parse it
// by hand — no dependency, and unknown keys are ignored forward-compatibly.
func parsePage(slug, raw string) Page {
	p := Page{Slug: slug, Title: slug}
	body := raw
	if strings.HasPrefix(raw, "---") {
		// Find the closing fence on its own line.
		rest := raw[len("---"):]
		rest = strings.TrimPrefix(rest, "\n")
		if end := strings.Index(rest, "\n---"); end >= 0 {
			front := rest[:end]
			body = strings.TrimPrefix(rest[end+len("\n---"):], "\n")
			for _, line := range strings.Split(front, "\n") {
				k, v, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				k = strings.TrimSpace(k)
				v = strings.TrimSpace(v)
				switch k {
				case "title":
					p.Title = v
				case "section":
					p.Section = v
				case "game":
					p.Game = v
				case "category":
					p.Category = v
				case "order":
					p.Order, _ = strconv.Atoi(v)
				}
			}
		}
	}
	p.Body = strings.TrimRight(body, "\n") + "\n"
	return p
}
