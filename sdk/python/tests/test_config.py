"""pyyol.toml convention config: infer, save/load roundtrip, agent_id, validate."""

import os

from pyyol import config as cfgmod


def test_infer_python_project(tmp_path):
    (tmp_path / "agent.py").write_text("agent = None\n")
    cfg = cfgmod.infer(str(tmp_path))
    assert cfg.language == "python"
    assert cfg.entry == "agent.py:agent"
    assert cfg.name == os.path.basename(str(tmp_path))


def test_save_load_roundtrip(tmp_path):
    cfg = cfgmod.Config(name="atlas", language="python", framework="langgraph", arena="mafia")
    path = cfgmod.save(cfg, str(tmp_path))
    assert os.path.isfile(path)
    loaded = cfgmod.load(path)
    assert loaded is not None
    assert loaded.name == "atlas"
    assert loaded.framework == "langgraph"
    assert loaded.arena == "mafia"
    assert loaded.mode == "sandbox"  # safe default persisted


def test_agent_id_omitted_until_set_then_persisted(tmp_path):
    cfg = cfgmod.Config(name="atlas")
    path = cfgmod.save(cfg, str(tmp_path))
    assert "agent_id" not in (tmp_path / "pyyol.toml").read_text()
    assert cfgmod.set_agent_id("agt_abc", path) is True
    assert cfgmod.load(path).agent_id == "agt_abc"
    # idempotent: same id → no rewrite needed
    assert cfgmod.set_agent_id("agt_abc", path) is False


def test_entry_parts_default_variable():
    assert cfgmod.Config(entry="agent.py").entry_parts() == ("agent.py", "agent")
    assert cfgmod.Config(entry="src/bot.py:brain").entry_parts() == ("src/bot.py", "brain")


def test_validate_flags_bad_mode_and_arena():
    problems = cfgmod.validate(cfgmod.Config(name="x", mode="live", arena="chess"))
    assert any("mode" in p for p in problems)
    assert any("arena" in p for p in problems)
    assert cfgmod.validate(cfgmod.Config(name="ok", mode="sandbox", arena="goofspiel")) == []


def test_find_walks_up(tmp_path, monkeypatch):
    cfgmod.save(cfgmod.Config(name="root"), str(tmp_path))
    sub = tmp_path / "a" / "b"
    sub.mkdir(parents=True)
    monkeypatch.chdir(sub)
    found = cfgmod.find()
    assert found is not None and found.endswith("pyyol.toml")
