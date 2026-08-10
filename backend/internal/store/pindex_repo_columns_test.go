package store

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The decision-log INSERT is a hand-built multi-row statement: a column list in a string,
// a positional row slice, and a `::jsonb` cast applied to ONE placeholder index. Those
// three have to agree, and nothing in the compiler makes them.
//
// Adding cached_write_tokens ahead of input_json moved its index from 16 to 17. Had the
// cast stayed at 16 it would have been applied to estimated_cost, and pgx sends a
// []byte to an uncast placeholder as bytea — so the real failure (a bytea in a jsonb
// column) happens at EXECUTE time, not prepare time. It would have passed every test that
// does not touch a database and broken only in production, which is precisely what the
// comment beside that cast warns about.
//
// This reads the source and checks the three agree, so the next column added ahead of
// input_json fails here instead of there.
func TestDecisionInsertColumnListAgreesWithPlaceholderCount(t *testing.T) {
	src := readSource(t, "pindex_repo.go")

	cols := parseIntConst(t, src, "cols")
	idx := parseIntConst(t, src, "inputJSONIndex")

	insert := between(t, src, "INSERT INTO agent_match_decisions (", ") VALUES ")
	names := splitColumns(insert)

	if len(names) != cols {
		t.Fatalf("column list has %d names but cols = %d\ncolumns: %v", len(names), cols, names)
	}
	if got := names[idx]; got != "input_json" {
		t.Fatalf("inputJSONIndex = %d points at %q, not input_json — the ::jsonb cast is on "+
			"the wrong placeholder and would fail only at execute time against a real DB\ncolumns: %v",
			idx, got, names)
	}

	// Every column the statement writes must also be refreshed by the upsert, or a
	// redelivered outbox event silently keeps a stale value for it.
	const conflictMarker = "ON CONFLICT (match_id, agent_id, seq) DO UPDATE SET"
	at := strings.Index(src, conflictMarker)
	if at < 0 {
		// A missing upsert clause is a real regression, not a reason to panic on a -1 index.
		t.Fatalf("upsert clause %q not found — the insert is no longer idempotent", conflictMarker)
	}
	conflict := src[at:]
	for _, c := range names {
		switch c {
		case "match_id", "agent_id", "seq": // the conflict key itself
			continue
		}
		if !strings.Contains(conflict, c+" =") {
			t.Errorf("column %q is inserted but never updated in the ON CONFLICT clause, so a "+
				"redelivered event would keep a stale value", c)
		}
	}
}

func readSource(t *testing.T, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	if f.Name.Name == "" {
		t.Fatalf("parse %s: no package name", name)
	}
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func parseIntConst(t *testing.T, src, name string) int {
	t.Helper()
	marker := "const " + name + " = "
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("const %s not found — was it renamed?", name)
	}
	rest := src[i+len(marker):]
	end := strings.IndexAny(rest, "\n\r")
	v, err := strconv.Atoi(strings.TrimSpace(rest[:end]))
	if err != nil {
		t.Fatalf("const %s is not an int literal: %v", name, err)
	}
	return v
}

func between(t *testing.T, src, open, close string) string {
	t.Helper()
	i := strings.Index(src, open)
	if i < 0 {
		t.Fatalf("marker %q not found", open)
	}
	rest := src[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		t.Fatalf("marker %q not found after %q", close, open)
	}
	return rest[:j]
}

func splitColumns(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(part)
		// Strip SQL line comments and stray whitespace/newlines.
		if k := strings.Index(p, "--"); k >= 0 {
			p = strings.TrimSpace(p[:k])
		}
		p = strings.TrimSpace(strings.ReplaceAll(p, "\n", " "))
		p = strings.TrimSpace(strings.ReplaceAll(p, "\t", " "))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
