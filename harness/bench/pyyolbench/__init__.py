"""pyyolbench — the Pyyol model benchmark harness.

One agent implementation, three game policies, five models, one scaffold. The scaffold
being ONE is the whole design: the SDK fingerprints the system prompt, tool names and
sampling parameters with the model excluded, and only seats sharing a fingerprint can be
paired. Everything in this package is arranged so that swapping the model is the only
thing that changes between two runs.
"""

from .brain import Brain
from .budget import Budget, BudgetExhausted
from .ledger import Ledger
from .models import ALL, PILOT, RANKED, Model, resolve

__all__ = [
    "Brain",
    "Budget",
    "BudgetExhausted",
    "Ledger",
    "Model",
    "resolve",
    "ALL",
    "RANKED",
    "PILOT",
]
