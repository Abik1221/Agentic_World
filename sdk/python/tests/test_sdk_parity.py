"""The two SDKs must expose the SAME surface, and this asserts it mechanically.

Behavioural conformance is already pinned by sdk/conformance/*.json — those catch a Python and
a JS SDK that DISAGREE about a move or a token count. They cannot catch a symbol that exists in
one language and not the other, which is a different and quieter failure: the developer ports
`pyyol.X` from one SDK to the other, it is simply absent, and the platform looks half-finished.

That is what happened. `prompt_for` shipped in Python and not JS. `canon`/`tool_name` existed in
Python's movetools under names JS spelled `canonMove`/`moveToolName`, and were not exported from
the package at all. `canonical` was public in JS and private here. Every one of those was found
by hand, one at a time; this test finds the next one.

The rule: for every name one SDK exports, the other exports it too, modulo snake_case ↔ camelCase.
Anything genuinely one-sided must be listed below WITH A REASON — an unexplained exemption is how
a parity guard rots into decoration.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

import pyyol

JS_INDEX = Path(__file__).resolve().parents[2] / "js" / "src" / "index.ts"

# Deliberate, reasoned one-sided exports. Keep this SHORT and keep the reasons real.
PY_ONLY = {
    # Python's duck-typed handlers may return a Move object, a dict, or None, so the server needs
    # a normalizer. TypeScript's handler signature makes the shape a compile-time matter, so the
    # JS server has nothing to normalize.
    "move_to_dict",
    # A zero-dependency urllib client that signs lifecycle requests like the platform does. The
    # JS SDK covers the same ground with simulateGoofspiel; a second HTTP client there would be
    # surface without a user.
    "LocalClient",
    # Typed views/moves are runtime classes in Python and compile-time interfaces in TS, so they
    # cannot be compared as exported VALUES. Their field-level agreement is what conformance
    # fixtures cover. (parse_view is NOT here: JS exports parseView from models.js, which the
    # first version of this list wrongly assumed was Python-only.)
    "GoofspielView",
    "GoofspielMove",
    "MonopolyView",
    "MonopolyMove",
    # Same reason as the views above: a dataclass in Python, an interface in TS. Both SDKs can
    # express a Monopoly trade — OPEN_TO_TABLE, the value that carries the open-offer
    # convention, IS exported by both and is deliberately NOT in this list.
    "MonopolyTrade",
    "MafiaView",
    "MafiaMove",
}
JS_ONLY = {
    # Monkey-patching a class prototype is how the JS instrumenter attaches to a client it does
    # not construct. Python's instrumenter wraps the call site instead, so there is no equivalent.
    "patchPrototype",
    # An internal helper re-exported for the JS test suite; not part of the documented surface.
    "runTurnUsage",
    # Generated from package.json at build time. Python's equivalent is __version__, which is
    # excluded below because it is dunder-named in one language and not the other.
    "SDK_VERSION",
    # TS needs a named export for the union/alias types; Python has no runtime counterpart.
    "SUPPORTED_GAMES",
}


def _camel_to_snake(name: str) -> str:
    return re.sub(r"(?<!^)(?=[A-Z])", "_", name).lower()


def _js_value_exports() -> set[str]:
    """Names the JS SDK exports as VALUES (not `export type`), including `export *` re-exports."""
    src = JS_INDEX.read_text(encoding="utf-8")
    names: set[str] = set()

    for match in re.finditer(r"export\s+(type\s+)?\{([^}]*)\}", src, re.S):
        if match.group(1):  # `export type { ... }` has no runtime counterpart
            continue
        for chunk in match.group(2).split(","):
            chunk = re.sub(r"//.*", "", chunk).strip()
            if " as " in chunk:
                chunk = chunk.split(" as ")[-1].strip()
            chunk = chunk.split("\n")[-1].strip()
            if re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", chunk):
                names.add(chunk)

    # `export * from "./models.js"` — pull the value exports out of the re-exported module too,
    # or every model helper looks Python-only. This was the bug in the first hand-rolled audit.
    for module in re.findall(r'export\s+\*\s+from\s+"\./([A-Za-z0-9_]+)\.js"', src):
        path = JS_INDEX.parent / f"{module}.ts"
        if not path.exists():
            continue
        body = path.read_text(encoding="utf-8")
        decl = r"^export\s+(?:async\s+)?(?:function|const|class|let|var)\s+([A-Za-z_][A-Za-z0-9_]*)"
        for m in re.finditer(decl, body, re.M):
            names.add(m.group(1))
    return names


def _py_value_exports() -> set[str]:
    return {n for n in pyyol.__all__ if not n.startswith("__")}


def test_every_python_export_has_a_js_counterpart():
    py = _py_value_exports()
    js_snake = {_camel_to_snake(n) for n in _js_value_exports()}
    missing = sorted(n for n in py if _camel_to_snake(n) not in js_snake and n not in PY_ONLY)
    assert not missing, (
        "these exist in the Python SDK and not in JS — add them there, or declare them in "
        f"PY_ONLY with a reason: {missing}"
    )


def test_every_js_export_has_a_python_counterpart():
    py_snake = {_camel_to_snake(n) for n in _py_value_exports()}
    missing = sorted(
        n for n in _js_value_exports() if _camel_to_snake(n) not in py_snake and n not in JS_ONLY
    )
    assert not missing, (
        "these exist in the JS SDK and not in Python — add them here, or declare them in "
        f"JS_ONLY with a reason: {missing}"
    )


def test_declared_exemptions_are_real():
    """An exemption for a symbol that now exists on both sides is stale and must be deleted,
    otherwise the lists grow into a place where real gaps hide."""
    py = _py_value_exports()
    js = _js_value_exports()
    js_snake = {_camel_to_snake(n) for n in js}
    py_snake = {_camel_to_snake(n) for n in py}

    stale_py = sorted(n for n in PY_ONLY if n in py and _camel_to_snake(n) in js_snake)
    stale_js = sorted(n for n in JS_ONLY if _camel_to_snake(n) in py_snake)
    assert not stale_py, f"PY_ONLY entries that now exist in BOTH — delete them: {stale_py}"
    assert not stale_js, f"JS_ONLY entries that now exist in BOTH — delete them: {stale_js}"

    # And an exemption for something this SDK does not even export is a typo.
    unknown = sorted(n for n in PY_ONLY if n not in py)
    assert not unknown, f"PY_ONLY names the Python SDK does not export: {unknown}"


def test_every_exported_name_actually_resolves():
    """__all__ is a promise. A name listed there but missing from _LAZY raises AttributeError on
    first access — so the parity fix above could itself ship a broken export."""
    broken = []
    for name in _py_value_exports():
        try:
            getattr(pyyol, name)
        except AttributeError as exc:
            broken.append(f"{name}: {exc}")
    assert not broken, f"exported but unresolvable: {broken}"


def test_prompt_for_is_byte_identical_across_sdks():
    """Same view in, same prompt out. Python's json.dumps defaults to ", "/": " while JS's
    JSON.stringify emits neither, so these silently produced different prompts — which would make
    a paired cross-SDK comparison of one scaffold compare two different prompts."""
    fixture = Path(__file__).resolve().parents[2] / "conformance" / "prompt_for.json"
    data = json.loads(fixture.read_text(encoding="utf-8"))
    for case in data["cases"]:
        assert pyyol.prompt_for(case["view"]) == case["prompt"], case["name"]
