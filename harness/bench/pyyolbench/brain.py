"""The decision loop: one model call per turn, bound, budgeted and recorded.

# The invariant this file exists to protect

Everything here that varies must vary by MODEL only in ways the scaffold fingerprint ignores.
The SDK hashes the system-prompt text, the tool names and a fixed list of sampling keys, with
the model excluded, and two agents whose fingerprints differ can never be paired. So:

  * the system prompt text is byte-identical across models — it comes from the policy, which
    takes no arguments;
  * the sampling parameters are module constants, not per-model settings;
  * the tool comes from the SDK's own `movetools`, so its name is identical;
  * `cache_control` markers DO vary by vendor, and that is safe — `scaffold._text_of` reads
    only the `text` field out of a content part, so a cache marker is invisible to the hash.
    This was checked rather than assumed, because getting it wrong would silently split every
    model into its own scaffold epoch and produce a benchmark that cannot compare anything.

# Why no temperature, anywhere

Claude Opus 5 and the GPT-5.6 family reject non-default sampling parameters outright — a
`temperature` is a 400, not a nudge. Sending it to some models and not others would also make
the sampling string differ and break pairing. So the harness sends none at all and lets every
model run at its own default, which is the only setting all five agree on.

# The three ways a turn can end

  * BOUND — the model returned a tool call, the move was legal, and the gateway will bind it.
  * REPAIRED — the model returned a tool call naming something illegal; the policy corrected
    it. Still a model decision, recorded separately so the report can say how often.
  * FALLBACK — no usable call (budget exhausted, provider error, timeout). The policy plays
    its inert default and the turn is unverified. The match continues, which matters: an
    abandoned match is an unfinished row the board discards, wasting every decision already
    paid for in it.

The split is written to the ledger per decision, because a model whose decisions are 40%
fallback is not being measured — it is being averaged with a heuristic — and the report has to
be able to say so instead of quietly crediting the model for the heuristic's play.
"""

from __future__ import annotations

import time
from typing import Any

from pyyol import movetools

from .budget import Budget, BudgetExhausted
from .games.base import GamePolicy
from .ledger import Ledger
from .memory import MatchMemory, Note
from .models import Model

# --- sampling: identical for every model, or pairing breaks -------------------------

#: Output ceiling per decision. Generous because reasoning tokens are billed as output and
#: counted against this: a thinking model handed 256 tokens spends them all on reasoning and
#: emits no tool call, which reads as "this model cannot play" when it is really "this model
#: was not given room to answer". 2048 clears a substantial reasoning trace plus the call.
MAX_TOKENS = 2048

#: OpenRouter's vendor-neutral reasoning switch. One key that turns on extended thinking for
#: Anthropic, OpenAI, Gemini and DeepSeek alike — which is the only way to ask five different
#: vendors for the same thing in one wire format.
#:
#: "medium" rather than "high" deliberately. Reasoning tokens bill at the OUTPUT rate, the
#: most expensive number on the invoice, and effort is the single largest lever on the cost of
#: this benchmark. Medium is enough to separate the models on games of this depth; high would
#: roughly double the bill to sharpen a distinction the win rate already shows.
REASONING = {"effort": "medium"}

#: Per-ATTEMPT timeout. The platform's shot clock is roughly 45s for Mafia and Monopoly, and
#: the budget below has to cover every attempt plus backoff, not just one.
#:
#: This number was wrong at 40s and the test suite found it. The OpenAI SDK retries twice by
#: default, so a provider returning 429 burned 3 x 40s = two minutes on a single decision —
#: the shot clock expires around 45s, the engine submits its own default move, and the agent
#: is still sitting on a socket. The seat forfeits every turn while looking healthy in its own
#: logs. 15s x 2 attempts leaves headroom inside the clock even with backoff.
TIMEOUT_S = 15.0

#: Attempts per decision, including the first. Retrying once is worth it — a 429 on a shared
#: key is common and usually clears immediately — but the ceiling is the shot clock, not the
#: provider's patience. Set on the request rather than trusted from the caller's client, so a
#: misconfigured client cannot silently reintroduce the forfeit above.
MAX_RETRIES = 1

#: Worst-case cost used for the budget RESERVATION, in tokens. Reserving the ceiling rather
#: than an estimate is what makes the shared budget safe: the run stops slightly early rather
#: than overrunning, and the reservation is released against the real number afterwards.
_RESERVE_PROMPT_TOKENS = 6000


class Brain:
    """One model playing one game. Constructed per agent process."""

    def __init__(
        self,
        *,
        model: Model,
        policy: GamePolicy,
        client: Any,
        budget: Budget,
        ledger: Ledger,
        seat_label: str = "",
        memory_window: int = 8,
    ) -> None:
        self.model = model
        self.policy = policy
        self.client = client
        self.budget = budget
        self.ledger = ledger
        self.seat_label = seat_label
        self.memory = MatchMemory(window=memory_window)
        # Built once. The tool definition is part of the scaffold (by name), and building it
        # per call would be pure waste on a hot path that is otherwise all network.
        self._tool = movetools.tool_for(policy.game, provider="openai")
        self._tool_choice = movetools.tool_choice_for(policy.game, provider="openai")

    # --- prompt assembly -------------------------------------------------------------

    def _system_message(self) -> dict[str, Any]:
        """The standing instructions, marked cacheable where the vendor needs telling.

        Anthropic and Google cache only what you explicitly mark, and OpenRouter forwards
        `cache_control` to both. OpenAI and DeepSeek cache long prefixes automatically and
        reject nothing, but sending them a marker they ignore is noise, so they get the
        plain string form.

        Both forms hash identically for the scaffold — `_text_of` extracts only `text` —
        so this vendor split cannot break paired comparison.
        """
        text = self.policy.system_prompt()
        if self.model.family in ("anthropic", "google"):
            return {
                "role": "system",
                "content": [
                    {"type": "text", "text": text, "cache_control": {"type": "ephemeral"}}
                ],
            }
        return {"role": "system", "content": text}

    def _messages(self, view: Any, match_id: str) -> list[dict[str, Any]]:
        """System prefix, then this turn's state. Nothing else.

        Note what is NOT here: prior turns' messages. The turn view is self-contained by
        design — Goofspiel replays every resolved round, Mafia replays the public
        transcript — so a growing message history would re-send information the platform
        already re-sends, at a cost that rises every turn. The agent's own private notes
        are the one thing the view cannot carry, and those come through `memory` inside
        the user turn where they do not disturb the cached prefix.
        """
        state = self.policy.render_state(view, self.memory.render(match_id))
        return [self._system_message(), {"role": "user", "content": state}]

    # --- the decision ----------------------------------------------------------------

    def decide(self, view: Any) -> dict[str, Any]:
        """Return one legal move for `view`. Never raises."""
        match_id = str(getattr(view, "match_id", "") or "")
        turn = self._turn_of(view)

        projected = self.model.cost_usd(_RESERVE_PROMPT_TOKENS, MAX_TOKENS)
        try:
            self.budget.reserve(projected, model=self.model.key)
        except BudgetExhausted as e:
            move = self.policy.fallback(view)
            self.ledger.decision(
                match_id=match_id, turn=turn, game=self.policy.game,
                seat=self._seat_of(view), source="fallback", move=move,
            )
            self.ledger.write("budget_exhausted", match_id=match_id, turn=turn, detail=str(e))
            return move

        actual = 0.0
        try:
            move, source, actual = self._call_and_decide(view, match_id, turn)
        except Exception as e:  # noqa: BLE001 — a brain must never forfeit a turn
            move, source = self.policy.fallback(view), "fallback"
            self.ledger.write(
                "brain_error", match_id=match_id, turn=turn,
                error=f"{type(e).__name__}: {e}",
            )
        finally:
            self.budget.settle(projected, actual, model=self.model.key)

        self.ledger.decision(
            match_id=match_id, turn=turn, game=self.policy.game,
            seat=self._seat_of(view), source=source, move=move,
        )
        return move

    def _call_and_decide(
        self, view: Any, match_id: str, turn: int
    ) -> tuple[dict[str, Any], str, float]:
        seat = self._seat_of(view)
        self.ledger.call_start(
            match_id=match_id, turn=turn, model=self.model.slug, game=self.policy.game, seat=seat
        )
        t0 = time.monotonic()
        status = 0
        error = ""
        resp: Any = None
        try:
            # `with_options` rather than trusting how the client was built: the retry count
            # is a correctness property of the decision (see MAX_RETRIES), not a caller
            # preference, and the brain is the only place that knows the shot clock.
            client = self.client
            with_options = getattr(client, "with_options", None)
            if callable(with_options):
                client = with_options(max_retries=MAX_RETRIES, timeout=TIMEOUT_S)
            resp = client.chat.completions.create(
                model=self.model.slug,
                messages=self._messages(view, match_id),
                tools=[self._tool],
                tool_choice=self._tool_choice,
                max_tokens=MAX_TOKENS,
                # `reasoning` is OpenRouter's own key and is not one of the SDK's hashed
                # sampling keys, so it does not enter the fingerprint. Held identical
                # across models regardless, because the comparison is only honest if every
                # model is asked to think equally hard.
                extra_body={"reasoning": REASONING},
                timeout=TIMEOUT_S,
            )
            status = 200
        except Exception as e:  # noqa: BLE001
            error = f"{type(e).__name__}: {e}"
            status = getattr(e, "status_code", 0) or 0
        latency_ms = int((time.monotonic() - t0) * 1000)

        if resp is None:
            self.ledger.call_end(
                match_id=match_id, turn=turn, model=self.model.slug, game=self.policy.game,
                seat=seat, status=status, prompt_tokens=0, completion_tokens=0,
                reasoning_tokens=0, cached_read=0, cost_usd=0.0, latency_ms=latency_ms,
                bound_move=None, submitted_move=None, error=error,
            )
            return self.policy.fallback(view), "fallback", 0.0

        usage = self._usage(resp)
        cost = self.model.cost_usd(
            usage["prompt_tokens"], usage["completion_tokens"], cached_read=usage["cached_read"]
        )

        args = movetools.move_from_response(self.policy.game, resp) or {}
        # Mafia's message body rides outside the tool call by design — the canonical bound
        # form is verb-and-seat, and text is deliberately not bindable. Hand the prose to
        # the policy under a reserved key so only Mafia has to know about it.
        if self.policy.game == "mafia":
            args = {**args, "_text": self._text_of(resp)}

        bound = movetools.bound_move(self.policy.game, resp)
        if not args:
            self.ledger.call_end(
                match_id=match_id, turn=turn, model=self.model.slug, game=self.policy.game,
                seat=seat, status=status, prompt_tokens=usage["prompt_tokens"],
                completion_tokens=usage["completion_tokens"],
                reasoning_tokens=usage["reasoning_tokens"], cached_read=usage["cached_read"],
                cost_usd=cost, latency_ms=latency_ms, bound_move=None, submitted_move=None,
                error="no_tool_call",
            )
            return self.policy.fallback(view), "fallback", cost

        move, source = self.policy.coerce(args, view)
        submitted = movetools.canon(self.policy.game, self._as_tool_args(move))

        self.ledger.call_end(
            match_id=match_id, turn=turn, model=self.model.slug, game=self.policy.game,
            seat=seat, status=status, prompt_tokens=usage["prompt_tokens"],
            completion_tokens=usage["completion_tokens"],
            reasoning_tokens=usage["reasoning_tokens"], cached_read=usage["cached_read"],
            cost_usd=cost, latency_ms=latency_ms, bound_move=bound, submitted_move=submitted,
            error="",
        )
        self.memory.add(
            match_id,
            Note(turn=turn, intent=self._first_line(self._text_of(resp)), move=submitted or "?"),
        )
        return move, source, cost

    # --- response readers ------------------------------------------------------------

    def _as_tool_args(self, move: dict[str, Any]) -> dict[str, Any]:
        """Re-express a submitted move in the tool's argument vocabulary.

        The move dict speaks the PLATFORM's field names (`action`) while the canonicaliser
        speaks the TOOL's (`kind`). Translating here lets the ledger record the submitted
        move in exactly the form the gateway will compare against the bound one, which is
        what makes a substitution detectable locally instead of only at match time.
        """
        if self.policy.game == "goofspiel":
            return {"card": move.get("card")}
        return {
            "kind": move.get("action"),
            "target": move.get("target"),
            "property": move.get("property"),
            "amount": move.get("amount"),
        }

    def _usage(self, resp: Any) -> dict[str, int]:
        """Token counts, tolerant of every shape OpenRouter forwards.

        Each vendor reports cached reads under its own key and OpenRouter passes the
        vendor's shape through, so this matches by MEANING rather than by a fixed name —
        the same rule the gateway's own usage normaliser follows. An unreadable shape
        yields zeros, which understates cost rather than inventing it.
        """
        u = getattr(resp, "usage", None)
        if u is None:
            return {"prompt_tokens": 0, "completion_tokens": 0, "reasoning_tokens": 0, "cached_read": 0}
        d = u if isinstance(u, dict) else getattr(u, "model_dump", lambda: u.__dict__)()
        if not isinstance(d, dict):
            d = {}

        def num(*keys: str) -> int:
            for k in keys:
                v = d.get(k)
                if isinstance(v, (int, float)):
                    return int(v)
            return 0

        prompt = num("prompt_tokens", "input_tokens")
        completion = num("completion_tokens", "output_tokens")

        details = d.get("prompt_tokens_details")
        cached = 0
        if isinstance(details, dict):
            v = details.get("cached_tokens")
            if isinstance(v, (int, float)):
                cached = int(v)
        if not cached:
            cached = num("cache_read_input_tokens", "prompt_cache_hit_tokens", "cached_tokens")

        reasoning = 0
        cdet = d.get("completion_tokens_details")
        if isinstance(cdet, dict):
            v = cdet.get("reasoning_tokens")
            if isinstance(v, (int, float)):
                reasoning = int(v)

        return {
            "prompt_tokens": prompt,
            "completion_tokens": completion,
            "reasoning_tokens": reasoning,
            "cached_read": min(cached, prompt),
        }

    def _text_of(self, resp: Any) -> str:
        try:
            choices = getattr(resp, "choices", None) or []
            if not choices:
                return ""
            msg = getattr(choices[0], "message", None)
            content = getattr(msg, "content", None) if msg is not None else None
            return content.strip() if isinstance(content, str) else ""
        except Exception:  # noqa: BLE001
            return ""

    def _first_line(self, text: str, limit: int = 160) -> str:
        if not text:
            return ""
        line = text.strip().splitlines()[0].strip()
        return line[:limit]

    def _seat_of(self, view: Any) -> int:
        raw = getattr(view, "raw", {}) or {}
        for key in ("seat", "your_seat"):
            v = getattr(view, key, None)
            if isinstance(v, int):
                return v
            v = raw.get(key)
            if isinstance(v, int):
                return v
        return 0

    def _turn_of(self, view: Any) -> int:
        """The decision index the turn proof is bound to.

        Each game names it differently — Goofspiel has `round`, Monopoly has `turn`, Mafia
        counts phases — so this reads whichever is present. Only used for the local ledger:
        the header the gateway actually trusts is set by the SDK from the raw payload, so a
        wrong guess here mislabels a log line and cannot mis-bind a decision.
        """
        raw = getattr(view, "raw", {}) or {}
        for key in ("round", "turn", "day"):
            v = getattr(view, key, None)
            if isinstance(v, int):
                return v
            v = raw.get(key)
            if isinstance(v, int):
                return v
        return 0

    def on_match_end(self, match_id: str) -> None:
        self.memory.forget(match_id)
