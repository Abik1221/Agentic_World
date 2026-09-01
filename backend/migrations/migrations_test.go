package migrations_test

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/agent-arena/arena/migrations"
)

// The migration set is checked here rather than discovered at boot.
//
// # Why this file exists
//
// Two branches numbered a migration 0091 at the same time. Each was correct against the
// main it was cut from, both passed review, and the collision only existed once they were
// both merged — which is exactly the moment nobody is looking.
//
// golang-migrate refuses to initialise a driver holding two files at one version, so the
// result was not a bad migration. It was a server that could not start AT ALL:
//
//	migrate: open embedded migrations: failed to init driver with path .:
//	duplicate migration file: 0091_private_rooms.down.sql
//
// Every existing test passed. The build was green. The deploy rolled, the container came
// up, connected to the database, and exited — repeatedly — and the only symptom was
// production being down.
//
// A unit test is the right place for this because the failure is a property of the FILE
// SET, not of any one migration. Nothing a reviewer reads in a single diff can reveal it.

var namePattern = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.(up|down)\.sql$`)

type migration struct {
	version string
	name    string
	up, dn  bool
}

func load(t *testing.T) map[string]*migration {
	t.Helper()
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}

	byVersion := map[string]*migration{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := namePattern.FindStringSubmatch(e.Name())
		if m == nil {
			t.Errorf("%s does not match NNNN_name.(up|down).sql — the loader parses "+
				"the version out of the filename, so a file it cannot parse is either "+
				"skipped silently or breaks the driver", e.Name())
			continue
		}
		version, name, dir := m[1], m[2], m[3]

		rec, ok := byVersion[version]
		if !ok {
			rec = &migration{version: version, name: name}
			byVersion[version] = rec
		}
		if rec.name != name {
			// The failure that took production down.
			t.Errorf("version %s is used by TWO migrations: %q and %q.\n"+
				"golang-migrate refuses to initialise with a duplicate version, so the "+
				"server will not start at all — it is not a bad migration, it is no "+
				"server. Renumber the newer one to the next free version.",
				version, rec.name, name)
			continue
		}
		if dir == "up" {
			rec.up = true
		} else {
			rec.dn = true
		}
	}
	return byVersion
}

// The regression test. One name per version, always.
func TestNoDuplicateMigrationVersions(t *testing.T) {
	got := load(t)
	if len(got) == 0 {
		t.Fatal("no migrations were embedded at all")
	}
}

// Every migration needs both halves.
//
// A missing .down.sql is not cosmetic: it makes the migration irreversible, which is
// discovered during the rollback of an incident, when it is least affordable.
func TestEveryMigrationHasUpAndDown(t *testing.T) {
	for _, m := range load(t) {
		if !m.up {
			t.Errorf("%s_%s has no .up.sql", m.version, m.name)
		}
		if !m.dn {
			t.Errorf("%s_%s has no .down.sql — it cannot be rolled back, and that is "+
				"found out during an incident rather than before one", m.version, m.name)
		}
	}
}

// Versions must be contiguous from 0001.
//
// A gap is not fatal to the loader, but it is nearly always the fingerprint of a migration
// that was written, numbered, and then lost in a merge — the same class of accident as the
// duplicate above, pointing the other way.
func TestMigrationVersionsAreContiguous(t *testing.T) {
	all := load(t)
	versions := make([]string, 0, len(all))
	for v := range all {
		versions = append(versions, v)
	}
	sort.Strings(versions)

	for i, v := range versions {
		want := i + 1
		var got int
		if _, err := fmtSscan(v, &got); err != nil {
			t.Fatalf("unparseable version %q: %v", v, err)
		}
		if got != want {
			t.Errorf("migration versions jump: expected %04d, found %s (%s). A gap is "+
				"usually a migration lost in a merge.", want, v, all[v].name)
			return // one report is enough; the rest cascade
		}
	}
}

// fmtSscan is a tiny wrapper so the test reads clearly without importing fmt for one call.
func fmtSscan(s string, out *int) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errNotANumber
		}
		n = n*10 + int(r-'0')
	}
	*out = n
	return 1, nil
}

var errNotANumber = errStr("version is not numeric")

type errStr string

func (e errStr) Error() string { return string(e) }
