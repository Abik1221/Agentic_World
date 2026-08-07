"""``pyyol.toml`` — convention-over-configuration project config.

Replaces the old ``manifest.json`` for onboarding. A project has a tiny, readable
``pyyol.toml`` at its root; the SDK infers what it can (language, name) and only
asks for what it must. Example::

    name = "atlas"
    language = "python"
    framework = "langgraph"
    arena = "mafia"
    visibility = "private"
    mode = "sandbox"          # sandbox (safe, default) | ranked (real stakes)
    entry = "agent.py:agent"  # module_path:variable
    agent_id = "agt_…"        # written automatically after first registration

Reading uses stdlib ``tomllib`` (3.11+) with a small fallback parser for 3.9/3.10;
writing emits a stable flat document (our schema is intentionally flat).
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field

CONFIG_NAME = "pyyol.toml"
KNOWN_ARENAS = ("goofspiel", "mafia", "monopoly")
MODES = ("sandbox", "ranked")


@dataclass
class Config:
    name: str = ""
    language: str = "python"
    framework: str = ""
    arena: str = "goofspiel"
    visibility: str = "private"
    mode: str = "sandbox"
    entry: str = "agent.py:agent"
    agent_id: str = ""
    endpoint: str = ""  # hosted-endpoint URL (Model B); empty ⇒ worker/dial-out
    auto_play: bool = False  # keep the agent in matches automatically (`pyyol serve`)
    # Order in which keys are written, so the file stays stable + readable.
    _order: tuple = field(
        default=(
            "name",
            "language",
            "framework",
            "arena",
            "visibility",
            "mode",
            "entry",
            "agent_id",
            "endpoint",
            "auto_play",
        ),
        repr=False,
        compare=False,
    )

    def entry_parts(self) -> tuple[str, str]:
        """Split ``entry`` into (module_path, variable). Defaults var to ``agent``."""
        mod, _, var = self.entry.partition(":")
        return (mod or "agent.py", var or "agent")


def find(start: str = "") -> str | None:
    """Locate ``pyyol.toml`` from ``start`` (or cwd) walking up to the filesystem root."""
    d = os.path.abspath(start or os.getcwd())
    while True:
        candidate = os.path.join(d, CONFIG_NAME)
        if os.path.isfile(candidate):
            return candidate
        parent = os.path.dirname(d)
        if parent == d:
            return None
        d = parent


def infer(directory: str, name: str = "") -> Config:
    """Convention-first defaults for a project directory (no file needed)."""
    directory = os.path.abspath(directory)
    cfg = Config(name=name or os.path.basename(directory))
    if os.path.exists(os.path.join(directory, "agent.py")):
        cfg.language, cfg.entry = "python", "agent.py:agent"
    elif any(
        os.path.exists(os.path.join(directory, f)) for f in ("agent.mjs", "agent.ts", "agent.js")
    ):
        cfg.language = "javascript"
        for f in ("agent.mjs", "agent.ts", "agent.js"):
            if os.path.exists(os.path.join(directory, f)):
                cfg.entry = f"{f}:agent"
                break
    return cfg


def _parse_scalar(raw: str):
    raw = raw.strip()
    if (raw.startswith('"') and raw.endswith('"')) or (raw.startswith("'") and raw.endswith("'")):
        return raw[1:-1]
    if raw in ("true", "false"):
        return raw == "true"
    try:
        return int(raw)
    except ValueError:
        return raw


def _load_dict(path: str) -> dict:
    with open(path, "rb") as f:
        data = f.read()
    try:
        import tomllib  # py3.11+

        return tomllib.loads(data.decode("utf-8"))
    except ModuleNotFoundError:
        pass
    # Minimal fallback for 3.9/3.10: flat `key = value` lines, `#` comments.
    out: dict = {}
    for line in data.decode("utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or line.startswith("["):
            continue
        key, sep, value = line.partition("=")
        if not sep:
            continue
        # Strip trailing inline comment for unquoted scalars.
        v = value.split("#", 1)[0] if not value.strip().startswith(('"', "'")) else value
        out[key.strip()] = _parse_scalar(v)
    return out


def load(path: str = "") -> Config | None:
    """Load a ``pyyol.toml`` (explicit path, or discovered from cwd). None if absent."""
    p = path or find()
    if not p or not os.path.isfile(p):
        return None
    d = _load_dict(p)
    cfg = Config()
    for key in cfg._order:
        if key in d and d[key] is not None:
            setattr(cfg, key, d[key] if key != "agent_id" else str(d[key]))
    return cfg


def _toml_escape(value) -> str:
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, int):
        return str(value)
    return '"' + str(value).replace("\\", "\\\\").replace('"', '\\"') + '"'


def dumps(cfg: Config) -> str:
    lines = []
    for key in cfg._order:
        val = getattr(cfg, key)
        if key in ("agent_id", "endpoint") and not val:
            continue  # omit optional string keys until they have a value
        lines.append(f"{key} = {_toml_escape(val)}")
    return "\n".join(lines) + "\n"


def save(cfg: Config, directory: str = "") -> str:
    """Write ``pyyol.toml`` to ``directory`` (or cwd). Returns the path written."""
    d = os.path.abspath(directory or os.getcwd())
    os.makedirs(d, exist_ok=True)
    path = os.path.join(d, CONFIG_NAME)
    with open(path, "w") as f:
        f.write(dumps(cfg))
    return path


def set_agent_id(agent_id: str, path: str = "") -> bool:
    """Persist a resolved ``agent_id`` back into an existing config. No-op if absent."""
    p = path or find()
    if not p:
        return False
    cfg = load(p)
    if cfg is None or cfg.agent_id == agent_id:
        return False
    cfg.agent_id = agent_id
    save(cfg, os.path.dirname(p))
    return True


def validate(cfg: Config) -> list[str]:
    """Return a list of human-readable problems (empty = valid)."""
    problems = []
    if not cfg.name:
        problems.append("`name` is empty")
    if cfg.mode not in MODES:
        problems.append(f"`mode` must be one of {MODES} (got {cfg.mode!r})")
    if cfg.arena not in KNOWN_ARENAS:
        problems.append(f"`arena` {cfg.arena!r} is not a known arena {KNOWN_ARENAS}")
    if ":" not in cfg.entry and not cfg.entry.endswith((".py", ".mjs", ".ts", ".js")):
        problems.append("`entry` should look like `agent.py:agent`")
    return problems
