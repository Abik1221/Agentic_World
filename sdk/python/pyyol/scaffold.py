"""Scaffold fingerprinting: what stays the same when you swap the model.

# The problem this exists to solve

"Claude is better than GPT" is not a claim our data could support before this. Every
Pyyol match confounds two things: the MODEL a developer chose and the HARNESS they wrote
around it — the system prompt, the tools, the sampling settings, how the game state is
framed. A strong agent on a weak model can beat a weak agent on a strong model, and the
leaderboard cannot tell you which happened.

The clean way out is a PAIRED comparison: the same harness, run with model A and with
model B. Then the harness cancels and the difference is the model. That is the only
causal claim available without running the experiment ourselves.

To pair, we need to know when the harness is the same. That is what this computes.

# What a fingerprint is, and what it is not

It is a stable id for "the scaffold", derived from what the SDK observes on each model
call and deliberately EXCLUDING the model name. Excluding the model is the whole point:
if the model were in the hash, every model would get its own scaffold id and nothing
could ever be paired.

It is NOT a claim that two developers with the same fingerprint wrote the same agent, and
it is not compared across developers. Its job is narrow: detect when one agent's harness
CHANGED, so that agent's history splits into scaffold epochs and a model swap inside an
epoch is cleanly attributable.

It is also not a way to read anyone's prompt. The system prompt enters as a digest, so
the fingerprint proves "same prompt" without revealing it.

# Why the system prompt, and what happens when that fails

The system prompt is the scaffold. It is the largest determinant of behaviour after the
model itself, and unlike the user turn it does not carry the game state — so it is stable
across a match. The user messages are deliberately excluded: they change every turn, and
hashing them would produce a new fingerprint per decision.

Some agents do put game state in the system prompt. For them the fingerprint churns and
pairing is impossible. That case is REPORTED as unstable rather than papered over — an
unstable fingerprint is honest and actionable ("move variable state into the user message
to become eligible for model comparison"), where a silently useless one is neither.

# Exclusions that matter

  * the model, and any alias of it — see above
  * base_url and credentials — routing through the Pyyol gateway must NOT change the
    fingerprint, or verified and unverified play by the same agent would look like two
    different scaffolds and could never be pooled
  * stream — a transport detail that does not change decision quality
  * user/assistant message CONTENT — the game state

The serialization is specified byte-for-byte (see ``canonical``) so the Python and JS SDKs
produce the SAME fingerprint for the same request, and a shared conformance fixture holds
them to it.
"""

from __future__ import annotations

import hashlib
from typing import Any, Dict, Iterable, List, Optional, Sequence, Tuple

# Bump only for a change that intentionally invalidates existing fingerprints. It is part
# of the hashed payload, so a bump splits every agent's history into a new epoch — which
# is correct when the meaning of a fingerprint changes, and expensive otherwise.
SCAFFOLD_VERSION = "pyyol-scaffold-v1"

# Sampling parameters that change how a model behaves and are therefore part of the
# scaffold. Anything not listed is ignored, so a provider adding an unrelated field does
# not silently split every agent's history.
#
# `model` is absent on purpose. So are base_url, api_key, timeout and stream: the first
# two would make gateway routing look like a new scaffold, and the last two do not affect
# what the model decides.
SAMPLING_KEYS: Tuple[str, ...] = (
    "frequency_penalty",
    "max_completion_tokens",
    "max_output_tokens",
    "max_tokens",
    "presence_penalty",
    "reasoning_effort",
    "seed",
    "stop",
    "stop_sequences",
    "temperature",
    "thinking",
    "top_k",
    "top_p",
    "verbosity",
)


def _num(v: Any) -> str:
    """Canonical number formatting, shared with the JS SDK.

    The obvious approach diverges: Python's ``str(1.0)`` is "1.0" while JavaScript's
    ``String(1.0)`` is "1", so an agent using temperature=1.0 would fingerprint
    differently in the two SDKs and its history would split at the language boundary for
    no reason. Six significant figures with the trailing ".0" dropped is what both can
    agree on exactly.
    """
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, int):
        return str(v)
    try:
        f = float(v)
    except (TypeError, ValueError):
        return str(v)
    if f != f or f in (float("inf"), float("-inf")):  # NaN / infinities
        return "0"
    return f"{f:.6g}"


def _scalar(v: Any) -> str:
    if v is None:
        return ""
    if isinstance(v, (bool, int, float)):
        return _num(v)
    if isinstance(v, str):
        return v
    if isinstance(v, dict):
        # A structured value (Anthropic's `thinking={"type":..,"budget_tokens":..}`).
        # Sorted so key order in the developer's literal cannot change the fingerprint.
        return "{" + ";".join(f"{k}={_scalar(v[k])}" for k in sorted(v)) + "}"
    if isinstance(v, (list, tuple)):
        return "[" + ",".join(_scalar(x) for x in v) + "]"
    return str(v)


def _text_of(content: Any) -> str:
    """Flatten a message content field to text.

    Content is a string in the simple case and a list of typed parts in the multimodal
    one. Only text parts contribute: an image's bytes are not the scaffold, and hashing
    them would make the fingerprint depend on the board screenshot of the moment.
    """
    if content is None:
        return ""
    if isinstance(content, str):
        return content
    if isinstance(content, dict):
        return str(content.get("text", "") or "")
    if isinstance(content, (list, tuple)):
        return "".join(_text_of(part) for part in content)
    text = getattr(content, "text", None)
    return str(text) if isinstance(text, str) else ""


def _digest(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def extract(kwargs: Dict[str, Any], *, endpoint: str = "") -> Dict[str, str]:
    """The fingerprint components observable in one provider request.

    Never raises: a fingerprinting problem must not break a developer's model call, and a
    turn that decides correctly while going unfingerprinted is strictly better than one
    that fails. On any surprise the affected component is simply absent.
    """
    roles: List[str] = []
    system_parts: List[str] = []

    # Anthropic carries the system prompt as a top-level argument rather than a message.
    # Treated as a leading system role so the two providers produce comparable shapes.
    sys_kwarg = kwargs.get("system")
    if sys_kwarg is not None:
        text = _text_of(sys_kwarg)
        if text:
            roles.append("system")
            system_parts.append(text)

    messages = kwargs.get("messages") or kwargs.get("input") or []
    if isinstance(messages, (list, tuple)):
        for m in messages:
            role = ""
            content: Any = None
            if isinstance(m, dict):
                role = str(m.get("role", "") or "")
                content = m.get("content")
            else:
                role = str(getattr(m, "role", "") or "")
                content = getattr(m, "content", None)
            if not role:
                continue
            roles.append(role)
            # "developer" is OpenAI's newer name for the system role; both are scaffold.
            if role in ("system", "developer"):
                system_parts.append(_text_of(content))

    sampling: List[str] = []
    for key in SAMPLING_KEYS:
        if key in kwargs and kwargs[key] is not None:
            sampling.append(f"{key}={_scalar(kwargs[key])}")

    tools: List[str] = []
    for t in kwargs.get("tools") or []:
        name = ""
        if isinstance(t, dict):
            # OpenAI nests the name under `function`; Anthropic puts it at the top level.
            fn = t.get("function")
            if isinstance(fn, dict):
                name = str(fn.get("name", "") or "")
            if not name:
                name = str(t.get("name", "") or "")
        else:
            name = str(getattr(t, "name", "") or "")
        if name:
            tools.append(name)

    out = {
        "client": endpoint,
        "roles": ",".join(roles),
        "sampling": ";".join(sampling),
        # Sorted: declaring the same tools in a different order is the same scaffold.
        "tools": ",".join(sorted(set(tools))),
    }
    if system_parts:
        out["system"] = _digest("\n".join(system_parts))
    return out


def canonical(components: Dict[str, str]) -> str:
    """The exact string that gets hashed.

    Specified rather than incidental: the JS SDK builds the same string, and a shared
    conformance fixture checks both against the same expected fingerprints. Keys are
    emitted in a FIXED order (not sorted, not dict order) so neither language's map
    iteration can affect the result, and an absent component is omitted rather than
    emitted empty — so adding a component later does not change the fingerprint of
    requests that never had one.
    """
    order = ("client", "roles", "sampling", "tools", "system")
    lines = [SCAFFOLD_VERSION]
    for key in order:
        val = components.get(key)
        if val:
            lines.append(f"{key}={val}")
    return "\n".join(lines)


def fingerprint(components: Dict[str, str]) -> str:
    """Short, prefixed id for a scaffold. 16 hex chars of SHA-256 (64 bits).

    Short enough to read in a UI and to group by in SQL. Collisions are irrelevant here in
    a way they would not be for a security token: fingerprints are only ever compared
    WITHIN one agent's own history, so the space that must stay distinct is a handful of
    harness versions, not the whole platform.
    """
    return "sc_" + _digest(canonical(components))[:16]


# Why a request could not be fingerprinted, as a stable CODE rather than prose.
#
# A code on the wire and prose at the point of reading, deliberately. The reason repeats on
# every decision of every non-qualifying agent, so shipping and storing the sentence would
# duplicate it thousands of times per match. A code is also aggregatable — "how many agents
# are ineligible, and why" is a question worth being able to ask — and it can change wording
# later without a migration.
ISSUE_NO_SYSTEM_PROMPT = "no_system_prompt"
ISSUE_NO_MESSAGES = "no_messages"

# Prose for each code. Read by the CLI and the dev-facing trace; never stored.
ISSUE_EXPLANATIONS: Dict[str, str] = {
    ISSUE_NO_SYSTEM_PROMPT: (
        "This request's instructions live in the user turn, mixed with the game state, where "
        "they cannot be told apart from it. Move your standing instructions into a system "
        "message to make this agent eligible for paired model comparison."
    ),
    ISSUE_NO_MESSAGES: (
        "No messages were observed on this request, so there was nothing to fingerprint."
    ),
}


def issue(kwargs: Dict[str, Any], *, endpoint: str = "") -> str:
    """Code for why this request yields no usable fingerprint, or "" when it does.

    Separate from ``from_request`` so the reason can reach a developer without the hot path
    building a string on every call.
    """
    try:
        comps = extract(kwargs, endpoint=endpoint)
    except Exception:  # noqa: BLE001
        return ISSUE_NO_MESSAGES
    if not comps.get("roles"):
        return ISSUE_NO_MESSAGES
    if not comps.get("system"):
        return ISSUE_NO_SYSTEM_PROMPT
    return ""


def explain(code: str) -> str:
    """Human-readable reason for an issue code, or "" for no issue / an unknown code."""
    return ISSUE_EXPLANATIONS.get(code, "")


def diagnose(kwargs: Dict[str, Any], *, endpoint: str = "") -> str:
    """Prose reason this request cannot be fingerprinted, or "" when it can.

    Convenience for local developer output; the wire carries ``issue`` codes.
    """
    return explain(issue(kwargs, endpoint=endpoint))


def from_request(kwargs: Dict[str, Any], *, endpoint: str = "") -> str:
    """Fingerprint one provider request, or "" when it cannot be fingerprinted.

    An empty result means "unknown", never a hash of nothing — a fingerprint shared by every
    request that failed to yield components would silently pool unrelated scaffolds into one
    bogus epoch and publish it as a controlled comparison.

    # A system prompt is REQUIRED, and this is the sharp edge of the whole design

    Without one, the hashable surface is ``client`` + ``roles`` + sampling — none of which
    move when the developer rewrites the instructions they actually steer the model with,
    because those instructions are sitting in a user message alongside the game state.

    That is worse than having no fingerprint at all. It is a FALSE CERTIFICATE: an agent
    could replace its entire strategy prompt mid-season, keep reporting the same scaffold
    id, and have the resulting improvement attributed to whatever model it happened to swap
    to. The fingerprint would be actively manufacturing the confound it exists to remove.

    There is no way to fix this by hashing more: the instructions and the game state are the
    same string, and hashing that string produces a new fingerprint every turn. So the
    honest answer is that a scaffold whose instructions live in the user turn cannot be
    identified, and such an agent is not eligible for paired comparison until it moves them
    into a system message. ``diagnose`` returns that explanation.
    """
    try:
        comps = extract(kwargs, endpoint=endpoint)
    except Exception:  # noqa: BLE001 - fingerprinting must never break a model call
        return ""
    if not comps.get("system"):
        return ""
    return fingerprint(comps)


class ScaffoldTracker:
    """Accumulates the fingerprints seen during one turn and reports stability.

    A turn that used one scaffold reports it. A turn whose fingerprint CHANGED between
    calls is reported as unstable, which is what happens when variable game state sits in
    the system prompt. Unstable is a real state with a real consequence — the agent cannot
    take part in a paired model comparison — so it travels with the data instead of being
    smoothed into the first value seen.
    """

    def __init__(self) -> None:
        self.first: str = ""
        self.unstable: bool = False
        self._seen: List[str] = []

    def observe(self, fp: str) -> None:
        if not fp:
            return
        if not self.first:
            self.first = fp
        elif fp != self.first:
            self.unstable = True
        if fp not in self._seen:
            self._seen.append(fp)

    @property
    def seen(self) -> Sequence[str]:
        return tuple(self._seen)


def eligible_for_pairing(fingerprints: Iterable[Optional[str]]) -> bool:
    """True when a set of observations can support a within-scaffold model comparison.

    Requires exactly one known scaffold. An unknown ("") fingerprint disqualifies rather
    than being ignored: treating "we could not tell" as "the same as the others" is how a
    confounded comparison gets published as a clean one.
    """
    vals = list(fingerprints)
    if not vals:
        return False
    if any(not v for v in vals):
        return False
    return len(set(vals)) == 1


# Prefixed public aliases, for parity with the JS SDK's scaffoldFingerprint /
# scaffoldFromRequest / scaffoldEligibleForPairing. The bare names stay available for code
# that imports this module directly; the prefixed ones are what `from pyyol import ...`
# offers, since "fingerprint" alone says nothing about what is being fingerprinted.
scaffold_fingerprint = fingerprint
scaffold_from_request = from_request
scaffold_eligible_for_pairing = eligible_for_pairing
