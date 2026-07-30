"""`pyyol init` must scaffold a usable manifest.

Ranked REQUIRES a manifest, and until now the only accurate copy of its schema in the
whole product was `_MANIFEST_TMPL` — defined and never referenced. A developer had to
reverse-engineer it from the source or guess, then discover the requirement at a 403.
"""

import json
import subprocess
import sys


def _init(tmp_path, name="scaffold-agent"):
    subprocess.run(
        [sys.executable, "-c", f"from pyyol.cli import main; main(['init', '{name}'])"],
        cwd=tmp_path,
        check=True,
        capture_output=True,
    )
    return tmp_path / name


def test_init_writes_a_schema_valid_manifest(tmp_path):
    d = _init(tmp_path)
    mf = d / "manifest.json"
    assert mf.exists(), "ranked needs a manifest and init did not scaffold one"

    m = json.loads(mf.read_text())
    assert m["manifestVersion"] == "1.0"
    assert m["agent"]["name"] == "scaffold-agent", (
        "the manifest should name the agent you just created"
    )
    assert m["games"], "the manifest must declare at least one game"
    assert m["runtime"]["timeout"] > 0, "runtime.timeout is required by the platform"

    # NO endpoint by default — the scaffold models CONNECTED ranked, which needs no
    # hosting. A placeholder URL would be worse than none: it validates, gets probed,
    # fails, and sends the developer debugging a host they never meant to run.
    assert "endpoint" not in m, "the scaffold must not ship a placeholder endpoint"


def test_scaffolding_does_not_mutate_the_shared_template(tmp_path):
    """The template is module-level. Writing into it directly would make every later
    `init` in the same process inherit the previous agent's name."""
    from pyyol.cli import _MANIFEST_TMPL

    _init(tmp_path, "first-agent")
    assert _MANIFEST_TMPL["agent"]["name"] == "", "init mutated the shared template"

    second = _init(tmp_path, "second-agent")
    m = json.loads((second / "manifest.json").read_text())
    assert m["agent"]["name"] == "second-agent"
