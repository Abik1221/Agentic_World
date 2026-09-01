"""Per-game rules, strategy and legality. One module per arena."""

from .goofspiel import GoofspielPolicy
from .mafia import MafiaPolicy
from .monopoly import MonopolyPolicy

__all__ = ["GoofspielPolicy", "MafiaPolicy", "MonopolyPolicy"]
