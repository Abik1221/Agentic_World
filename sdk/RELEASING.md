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
