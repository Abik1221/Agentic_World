package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Documentation is a promise. These tests check the promises that can be checked
// mechanically, because the expensive failures are not typos — they are sentences that
// were true once and quietly stopped being true.
//
// Three real defects were found by hand-running these checks, and each had the same
// shape: the docs described the platform as it used to be.
//
//   - Mafia and Monopoly were described as "sandbox/lobby today (no ranked queue yet)",
//     which reads as "no money here". Both stake real coins on a paid table, pay out of
//     the pot minus the platform fee, and move a per-arena skill rating. A developer
//     deciding what to build read that line and had no reason to try.
//   - The deployment Dockerfile told people to set PYYOL_API_KEY. Nothing reads it — the
//     SDK reads PYYOL_TOKEN — so a container built from the example started, found no
//     credential and exited, with the Dockerfile looking correct.
//   - `pyyol games` existed and was documented nowhere.
//
// What these tests deliberately do NOT do is check prose. They check the claims with a
// machine-checkable referent: env var names, CLI command names, and money-flow wording
// that contradicts the code.

// docsRoot walks up to the repo's backend dir so the test can read sibling packages'
// source. Skips (rather than fails) if the layout is not what it expects — a test that
// cannot find its inputs should not masquerade as a passing check.
func repoFile(t *testing.T, rel string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func contentFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir("content", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		out[path] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("reading docs content: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("no markdown found under content/ — this test would pass vacuously")
	}
	return out
}

// Every arena env var the docs name must actually be read by config.go.
//
// This is the check that would have caught PYYOL_API_KEY. An env var in a doc is an
// instruction, and an instruction that does nothing is worse than a missing one: the
// reader follows it and concludes the platform is broken.
func TestDocumentedArenaEnvVarsAreRead(t *testing.T) {
	cfg, ok := repoFile(t, "../config/config.go")
	if !ok {
		t.Skip("config.go not readable from here")
	}
	// Names config.go really pulls from the environment.
	readRe := regexp.MustCompile(`l\.(?:str|intVal|boolVal|floatVal|dur|durVal)\(\s*"([A-Z0-9_]+)"`)
	read := map[string]bool{}
	for _, m := range readRe.FindAllStringSubmatch(cfg, -1) {
		read[m[1]] = true
	}
	// Also honour anything assigned via a direct LookupEnv/Getenv in that file.
	for _, m := range regexp.MustCompile(`(?:LookupEnv|Getenv)\(\s*"([A-Z0-9_]+)"`).FindAllStringSubmatch(cfg, -1) {
		read[m[1]] = true
	}

	// PYYOL_* names are read by the Python CLIENT, not by this service, so they are
	// checked against the SDK source instead of config.go. Skipping them entirely is what
	// let PYYOL_API_KEY sit in the deployment guide: a variable nothing reads, in the one
	// example people copy verbatim into a Dockerfile.
	//
	// sdkReadable records whether we could actually SEE the SDK, and it is load-bearing.
	// The SDK lives in a sibling directory (../../../sdk), so a checkout or a container
	// that mounts only backend/ cannot reach it — and when that happened this test did not
	// skip the PYYOL_ names, it left `read` empty for all of them and then reported every
	// documented PYYOL_ variable as one "neither the arena nor the SDK reads". Ten failures
	// blaming the docs for a path the test could not open.
	//
	// That is the same vacuous-assertion problem contentFiles guards against above, run in
	// reverse: there, missing input would silently PASS; here, missing input spuriously
	// FAILED, which is worse — it sends someone to edit correct documentation. The arena's
	// own variables are still fully checked either way; only the client-owned subset is
	// held back, and loudly.
	sdkReadable := false
	if cli, ok := repoFile(t, "../../../sdk/python/pyyol/cli.py"); ok {
		sdkReadable = true
		sdk := cli
		for _, f := range []string{"../../../sdk/python/pyyol/telemetry.py", "../../../sdk/python/pyyol/config.py", "../../../sdk/python/pyyol/credentials.py", "../../../sdk/python/pyyol/runtime.py", "../../../sdk/python/pyyol/server.py"} {
			if extra, ok := repoFile(t, f); ok {
				sdk += extra
			}
		}
		// Both accessor spellings. `os.environ.get("X", default)` is the common one, but the
		// CLI also does a bare `os.environ["X"]` after testing for presence, so a name read
		// only that way would look unread.
		for _, re := range []*regexp.Regexp{
			regexp.MustCompile(`(?:environ\.get|getenv)\(\s*"(PYYOL_[A-Z0-9_]+)"`),
			regexp.MustCompile(`environ\[\s*"(PYYOL_[A-Z0-9_]+)"\s*\]`),
		} {
			for _, m := range re.FindAllStringSubmatch(sdk, -1) {
				read[m[1]] = true
			}
		}
		read["PYYOL_LENS_API_KEY"] = true // read by the telemetry emitter
	} else {
		t.Logf("SDK source not reachable from here (../../../sdk/python/pyyol/cli.py) — " +
			"PYYOL_* names in the docs are NOT being verified in this run. Mount the repo " +
			"root, not just backend/, to restore that half of the check.")
	}

	arenaPrefix := regexp.MustCompile(`^(SOLANA_|WITHDRAW_|DEPOSIT_|RAKE_|COIN_|RANKED_|HOT_WALLET_|S3_|MEDIA_|METRICS_|PYYOL_)`)
	nameRe := regexp.MustCompile(`\b([A-Z][A-Z0-9]{2,}(?:_[A-Z0-9]+)+)\b`)

	for path, body := range contentFiles(t) {
		for _, m := range nameRe.FindAllStringSubmatch(body, -1) {
			name := m[1]
			if !arenaPrefix.MatchString(name) || read[name] {
				continue
			}
			// Client-owned name we had no source to check against — see sdkReadable.
			// Asserting here would be asserting against an empty set.
			if !sdkReadable && strings.HasPrefix(name, "PYYOL_") {
				continue
			}
			t.Errorf("%s names %s, which neither the arena nor the SDK reads — following "+
				"that instruction would have no effect", path, name)
		}
	}
}

// Every `pyyol <command>` the docs mention must exist in the CLI.
func TestDocumentedCLICommandsExist(t *testing.T) {
	cli, ok := repoFile(t, "../../../sdk/python/pyyol/cli.py")
	if !ok {
		t.Skip("the Python CLI is not readable from here")
	}
	// add_parser("name") — the name may sit on the following line, so match across it.
	// A single-line pattern under-reports and would flag real commands as phantom.
	real := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?s)add_parser\(\s*"([a-z][a-z0-9-]*)"`).FindAllStringSubmatch(cli, -1) {
		real[m[1]] = true
	}
	// `help` is dispatched in main() before argparse so `pyyol help` / `pyyol /help`
	// are not invalid-choice errors. The docs advertise that on purpose; treating
	// it as missing is what kept CI (and therefore deploy) red after the slash fix.
	if strings.Contains(cli, `"help", "h", "?"`) {
		real["help"] = true
	}
	if len(real) < 10 {
		t.Fatalf("only found %d CLI commands — the parser pattern is wrong, and this test "+
			"would report every real command as missing", len(real))
	}

	// `pyyol` also appears as a Python module ("from pyyol import Adapter") and in prose
	// ("the installed pyyol package"), neither of which is a command.
	notCommands := map[string]bool{"import": true, "package": true}
	cmdRe := regexp.MustCompile("`pyyol ([a-z][a-z0-9-]*)")

	for path, body := range contentFiles(t) {
		for _, m := range cmdRe.FindAllStringSubmatch(body, -1) {
			name := m[1]
			if notCommands[name] || real[name] {
				continue
			}
			t.Errorf("%s documents `pyyol %s`, which the CLI does not define", path, name)
		}
	}
}

// The games docs must not describe Mafia or Monopoly as money-free.
//
// Pinned as a phrase check because the claim is prose, and because this specific wrong
// sentence survived multiple doc revisions: both games settle real coins through
// wallet.SettleTable on a paid table and update a per-arena rating. What is actually
// Goofspiel-only is the automatic MATCHMAKING QUEUE, which is a statement about how you
// are matched, not about whether money moves.
func TestGameDocsDoNotClaimSandboxOnly(t *testing.T) {
	banned := []string{
		"sandbox/lobby today",
		"sandbox/lobby until",
		"no ranked queue yet",
		"sandbox only",
	}
	for path, body := range contentFiles(t) {
		low := strings.ToLower(body)
		for _, phrase := range banned {
			if strings.Contains(low, phrase) {
				t.Errorf("%s says %q. Mafia and Monopoly stake real coins on a paid table "+
					"(wallet.SettleTable) and move a skill rating; only the matchmaking "+
					"QUEUE is Goofspiel-only. Say which one you mean.", path, phrase)
			}
		}
	}
}

// The fee wording must match the configured economy: free in, one fee out.
//
// The docs carried "5% in, 5% out" in six places while the platform charged it, and a
// number in prose is exactly the kind of claim that outlives the code that produced it.
func TestFeeWordingMatchesTheEconomy(t *testing.T) {
	stale := []string{
		"5% in, 5% out",
		"5% deposit fee",
		"5% withdrawal fee",
		"5% in / 5% out",
	}
	for path, body := range contentFiles(t) {
		low := strings.ToLower(body)
		for _, phrase := range stale {
			if strings.Contains(low, strings.ToLower(phrase)) {
				t.Errorf("%s still says %q — deposits are free and the cash-out fee is 10%%", path, phrase)
			}
		}
	}
}
