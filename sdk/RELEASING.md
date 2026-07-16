# Releasing the pyyol SDKs

Both SDKs publish from GitHub Actions on a version tag, using **OIDC trusted
publishing** — no long-lived API tokens are stored in the repo. The Python
package goes to PyPI, the JS package to npm (with provenance).

- Python workflow: [`.github/workflows/release-python.yml`](../.github/workflows/release-python.yml) — trigger tag `py-vX.Y.Z`
- JS workflow: [`.github/workflows/release-js.yml`](../.github/workflows/release-js.yml) — trigger tag `js-vX.Y.Z`

## One-time setup (per registry)

**PyPI + TestPyPI** — for project `pyyol`, add a *Trusted Publisher*:
- Owner `Abik1221`, repository `Agentic_World`
- Workflow `release-python.yml`
- Environment `pypi` on PyPI, `testpypi` on TestPyPI

Create matching **GitHub Environments** named `pypi` and `testpypi`
(Settings → Environments). Optionally add a required reviewer on `pypi` so a
human approves every production publish.

**npm** — on npmjs.com for package `pyyol`, add a *Trusted Publisher* pointing at
`Abik1221/Agentic_World`, workflow `release-js.yml`. (Fallback if trusted
publishing isn't available: create an automation `NPM_TOKEN` repo secret and
uncomment the `NODE_AUTH_TOKEN` line in the workflow's publish step.)

## Cutting a release

1. **Bump the version** (single source of truth per package):
   - Python: `sdk/python/pyyol/__init__.py` → `__version__`
   - JS: `sdk/js/package.json` → `version` (the `SDK_VERSION` in `dist` is
     regenerated from it at build)
   Follow [SemVer](https://semver.org). The **package version is independent of
   the wire protocol** (`PROTOCOL_VERSION` / signature scheme).
2. **Update the changelog**: `sdk/python/CHANGELOG.md` / `sdk/js/CHANGELOG.md`.
3. **Rehearse (recommended)** via *Actions → run workflow → dry_run = true*:
   - Python: uploads to **TestPyPI**.
   - JS: runs `npm publish --dry-run`.
4. **Publish** by pushing the tag:
   ```bash
   git tag py-v1.0.0 && git push origin py-v1.0.0   # → PyPI
   git tag js-v1.0.0 && git push origin js-v1.0.0   # → npm
   ```
   Each workflow runs the full test suite, asserts the tag matches the package
   version (fails closed on a mismatch), verifies the tarball (no test files,
   LICENSE present), then publishes.
5. **Verify**: `pip install pyyol==X.Y.Z` / `npm install pyyol@X.Y.Z` from a clean
   environment; confirm provenance shows on the npm package page.

## Automated releases (recommended) — Release Please

You should **not** publish on every push to `main` (package versions are
immutable, and most commits don't warrant a release). Instead
[`.github/workflows/release-please.yml`](../.github/workflows/release-please.yml)
turns your Conventional Commits into releases with a single human gate:

1. Merge normal commits to `main` (`feat(sdk): …`, `fix(sdk): …`, `perf: …`).
2. Release Please keeps an open **release PR per SDK** that bumps the version and
   regenerates the `CHANGELOG.md` from those commits. Nothing publishes yet.
3. **Merge the release PR** → Release Please creates a GitHub Release and the
   version tag (`py-vX.Y.Z` / `js-vX.Y.Z`), which triggers the tag workflows
   above → OIDC publish. The version bump is automatic; you never edit it by hand.

Config: [`release-please-config.json`](../release-please-config.json) (two
packages, `component: py` / `js` so the tags match the publish triggers) +
[`.release-please-manifest.json`](../.release-please-manifest.json) (current
versions — release-please updates it on each release).

**One-time token setup (required for the chain to fire):** a tag pushed with the
default `GITHUB_TOKEN` does not trigger other workflows. Create a GitHub App
installation token (preferred) or a fine-grained PAT with `contents: write` +
`pull-requests: write`, and store it as the repo secret `RELEASE_PLEASE_TOKEN`.
Without it, release PRs still work but you push the final `py-v*` / `js-v*` tag by
hand (step 4 of the manual flow).
