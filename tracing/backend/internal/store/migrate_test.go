package store

import "testing"

// A comment containing ';' (as in 004_benchmark_tokens.sql "reasoning/total);")
// must not leak into a statement — comments are stripped before the ';' split.
func TestSplitSQL_CommentWithSemicolon(t *testing.T) {
	src := `-- token economics (prompt/completion/
-- reasoning/total); previously dropped. Add columns:
ALTER TABLE t ADD COLUMN IF NOT EXISTS a Int64 DEFAULT 0;
ALTER TABLE t ADD COLUMN IF NOT EXISTS b Int64 DEFAULT 0;`
	got := splitSQL(src)
	if len(got) != 2 {
		t.Fatalf("want 2 statements, got %d: %#v", len(got), got)
	}
	for _, s := range got {
		if s[:5] != "ALTER" {
			t.Fatalf("statement leaked a comment: %q", s)
		}
	}
}

func TestSplitSQL_TrailingInlineComment(t *testing.T) {
	got := splitSQL("SELECT 1; -- trailing\nSELECT 2;")
	if len(got) != 2 || got[0] != "SELECT 1" || got[1] != "SELECT 2" {
		t.Fatalf("unexpected: %#v", got)
	}
}
