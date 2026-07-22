"""Money-safety mode model: sandbox is the safe default; ranked needs explicit opt-in."""

import io

from pyyol import mode


def test_dev_is_always_sandbox_even_if_ranked_requested(monkeypatch):
    # dev_locked wins over every other signal — development can never risk money.
    monkeypatch.setenv("PYYOL_MODE", "ranked")
    assert mode.resolve(ranked_flag=True, cfg_mode="ranked", dev_locked=True) == mode.SANDBOX


def test_precedence_flag_env_cfg_default(monkeypatch):
    monkeypatch.delenv("PYYOL_MODE", raising=False)
    # default
    assert mode.resolve() == mode.SANDBOX
    # cfg
    assert mode.resolve(cfg_mode="ranked") == mode.RANKED
    # env beats cfg
    monkeypatch.setenv("PYYOL_MODE", "sandbox")
    assert mode.resolve(cfg_mode="ranked") == mode.SANDBOX
    # explicit flag beats everything (but dev_locked, tested above)
    assert mode.resolve(ranked_flag=True, cfg_mode="sandbox") == mode.RANKED


def test_banner_labels():
    assert "SANDBOX" in mode.banner(mode.SANDBOX, color=False)
    assert "RANKED" in mode.banner(mode.RANKED, color=False)


def test_confirm_ranked_requires_explicit_yes():
    assert mode.confirm_ranked(assume_yes=True) is True

    # Non-interactive (CI) without --yes must NOT auto-enter real stakes.
    class NoTTY(io.StringIO):
        def isatty(self):
            return False

    assert mode.confirm_ranked(assume_yes=False, stream=NoTTY()) is False
