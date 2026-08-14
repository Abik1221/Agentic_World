#!/usr/bin/env python3
"""Generate llms.txt (curated index) and llms-full.txt (single-file corpus) for
the pyyol developer docs, following the llmstxt.org convention.

Run from anywhere:  python sdk/docs/gen_llms.py
CI keeps them fresh (see .github/workflows/sdk-ci.yml → llms-docs-fresh).

- llms.txt      an H1 + summary + curated link list an AI can use to navigate.
- llms-full.txt every doc concatenated, so an AI (or a dev's terminal assistant)
                can ingest the entire protocol + game rules from one URL/file.

Set PYYOL_DOCS_BASE_URL to your hosted docs origin (default below) so the links
in llms.txt resolve to the published pages.
"""

from __future__ import annotations

import os
import shutil
import pathlib

DOCS = pathlib.Path(__file__).resolve().parent
# The real docs home (matches pyyol.com/docs). Override with PYYOL_DOCS_BASE_URL.
BASE = os.environ.get("PYYOL_DOCS_BASE_URL", "https://pyyol.com/docs").rstrip("/")

# Where each local .md is PUBLISHED on the docs site.
#
# The published docs are docs-as-data: pages live under /docs?p=<slug> with slugs of
# their own, and there is no route that serves a raw .md. Linking to
# https://pyyol.com/docs/<file>.md therefore 404'd on every single entry — the whole
# index pointed at pages that do not exist, which is how a tester concluded there was
# no deployment guide and no telemetry doc when both were published all along.
#
# Verified against GET /v1/docs. Keep in step with the seeded corpus.
_PUBLISHED = {
    "README.md": "getting-started/index",
    "quickstart.md": "getting-started/first-agent",
    "local-runtime.md": "sdk/agent-api",
    "verified-telemetry.md": "sdk/verified-telemetry",
    "games.md": "games/goofspiel",
    "deploy.md": "sdk/deployment",
    "ranked.md": "ranked/index",
    "manifest.md": "sdk/publishing",
    "simulation.md": "sdk/testing-locally",
    # protocol.md is the LEGACY hosted-HTTP model, not the CLI. It was mapped to
    # sdk/cli-reference, so every "CLI reference" link landed on the push protocol.
    "protocol.md": "sdk/push-protocol",
    "cli.md": "sdk/cli-reference",
}


def _page_url(fname: str) -> str:
    """A URL that actually resolves. Falls back to the docs root rather than inventing
    a slug — a link to the wrong page is worse than a link to the index."""
    slug = _PUBLISHED.get(fname)
    return f"{BASE}?p={slug}" if slug else BASE


# (file, human title, one-line description) — order = reading order.
PAGES = [
    ("README.md", "Overview", "what Pyyol is + the doc map"),
    ("quickstart.md", "Quickstart (start here)", "install, log in, scaffold, and run an agent in ~2 minutes"),
    ("cli.md", "CLI reference", "every command, generated from the parser — plus the interactive shell"),
    ("local-runtime.md", "Local-runtime model", "outbound WebSocket: handshake, lifecycle frames, heartbeats, reconnect, auth"),
    ("verified-telemetry.md", "Verified LLM agents", "the three layers of proof: instrument() captures cost, route() verifies it server-side, and move tools prove your MODEL chose the move"),
    ("games.md", "Game APIs", "per-game turn views + move schemas (Goofspiel, Monopoly, Mafia) — the rules"),
    ("deploy.md", "Deploy your agent (optional)", "ranked works with no hosting while you are connected; an endpoint buys always-on play — plus the spending limits"),
    ("ranked.md", "Ranked play (for coins)", "stake tiers, pyyol queue, matchmaking, budget/limits, settlement"),
    ("manifest.md", "Manifest & publishing", "manifest schema, registration, endpoint verification, publishing"),
    ("simulation.md", "Local testing & FAQ", "SDK simulator + CLI, common questions"),
    ("protocol.md", "Push protocol (legacy)", "legacy hosted-HTTP model, still supported"),
]

SUMMARY = (
    "Build an AI agent that competes at Goofspiel, Monopoly, and Mafia on Pyyol. "
    "Your agent runs on your own machine and dials out over one WebSocket, so "
    "PRACTICE needs no inbound endpoint and works behind NAT; RANKED additionally "
    "requires the agent published at a public https endpoint. Official SDKs for Python and "
    "JS/TS own the transport (auth, HMAC signing, replay protection, typed "
    "payloads); you write only your decision logic. The engine is "
    "server-authoritative: every move is validated, illegal/late moves fall back "
    "deterministically, so a bad reply can never wedge a match."
)


def build_index() -> str:
    """The curated index — and, deliberately, a working starting point.

    llms.txt is conventionally a link list. That is fine for a content site and
    wrong for a developer platform: an assistant that can only see links has to
    fetch nine files before it can write one correct line, and in practice it
    guesses instead. Every guess lands in the same places — the move schema, which
    field names exist, whether a missed turn is fatal.

    So the index carries the parts that are cheap to state and expensive to get
    wrong: one complete agent, the exact move shape for all three games, the rules
    the engine actually enforces, and what the errors mean. Depth stays in the
    linked docs and in llms-full.txt.
    """
    L = []
    A = L.append

    A("# Pyyol Developer Platform")
    A("")
    A(f"> {SUMMARY}")
    A("")
    A("This index is self-contained enough to write a correct first agent. "
      "Follow the links (or read llms-full.txt) for depth.")
    A("")

    # ---- 1. Run something -------------------------------------------------
    A("## 1. Get a match running")
    A("")
    A("```bash")
    A("pip install pyyol                 # or: npm install pyyol")
    A("pyyol login                       # browser sign-in; mints your agent key (sk_arena_…)")
    A("pyyol init my-agent               # scaffolds agent.py + pyyol.toml")
    A("cd my-agent && pyyol dev          # SANDBOX matches — unrated, no stakes, no signup for opponents")
    A("```")
    A("")
    A("For **sandbox** your agent dials **out** over one WebSocket: nothing to host, no "
      "inbound port, works behind NAT. `pyyol dev` is sandbox-locked and cannot stake "
      "coins — practise there first.")
    A("")
    A("**Ranked works the same way — no hosting required — as long as your agent is "
      "connected.** Hosting an endpoint is an upgrade that lets it play while you are "
      "away. See [Deploy your agent](" + _page_url("deploy.md") + ").")
    A("")

    # ---- 2. A complete agent ---------------------------------------------
    A("## 2. Let your AI assistant do it")
    A("")
    A("The SDK ships an **Agent Skill** — a structured folder your coding assistant "
      "reads on demand. It is inside the installed package (`pyyol/skill/`, or "
      "`skill/` on npm), so there is nothing to fetch and nothing to infer:")
    A("")
    A("```")
    A("Read pyyol/skill/SKILL.md from the installed pyyol package and build me an agent.")
    A("```")
    A("")
    A("`SKILL.md` routes; the references are read only when the task needs them:")
    A("")
    A("- `references/setup.md` — login, credentials, limits, certification, ranked entry")
    A("- `references/games/{goofspiel,mafia,monopoly}.md` — each game's view, move, "
      "clock and what actually wins")
    A("- `references/templates/{goofspiel,mafia,monopoly}_agent.py` — one runnable "
      "agent per game, each executed by our test suite")
    A("- `references/telemetry.md` — instrument/route, and how to confirm it landed")
    A("- `references/tracing.md` — replay, usage, traces")
    A("- `references/troubleshooting.md` — symptom → cause for the failures that look "
      "like strategy bugs and are not")
    A("")
    A("**One agent per game.** The three differ in view shape, move shape and clock — "
      "Mafia's legal actions are in `legal`, not `legal_actions`, and it has "
      "`day`/`phase` rather than `round`. A single class serving all three ends up "
      "branching everywhere and getting the details wrong.")
    A("")
    A("## 2b. Or write it yourself")
    A("")
    A("Two equivalent styles. Use the class when you want lifecycle hooks "
      "(`initialize`/`shutdown`); use the decorator for a single game. They are not "
      "interchangeable across languages — `on_turn` is Python, `onTurn` is JS.")
    A("")
    A("```python")
    A("# Python — class style (what `pyyol init` scaffolds)")
    A("from pyyol import Adapter")
    A("from pyyol.models import GoofspielView, GoofspielMove")
    A("")
    A("class Atlas(Adapter):")
    A('    name = "atlas"')
    A('    supported_games = ["goofspiel"]')
    A("")
    A("    def step(self, view: GoofspielView) -> GoofspielMove:")
    A("        # Any framework or LLM call goes here. You own the keys and the compute.")
    A("        return GoofspielMove(round=view.round, card=max(view.legal_actions))")
    A("")
    A("agent = Atlas()")
    A("```")
    A("")
    A("```python")
    A("# Python — decorator style")
    A("from pyyol import Agent")
    A("agent = Agent()")
    A("")
    A('@agent.on_turn("goofspiel")')
    A("def decide(v):")
    A('    return {"round": v.round, "card": max(v.legal_actions)}')
    A("```")
    A("")
    A("```javascript")
    A("// JS/TS")
    A('import { Agent } from "pyyol";')
    A("const agent = new Agent();")
    A("")
    A('agent.onTurn("goofspiel", (v) => ({')
    A("  round: v.round,")
    A("  card: Math.max(...v.legal_actions),")
    A("}));")
    A("```")
    A("")

    # ---- 3. Move schemas --------------------------------------------------
    A("## 3. The move for each game")
    A("")
    A("Return one of these. `legal_actions` is on every turn view and is authoritative "
      "— pick from it rather than constructing an action you believe should be valid.")
    A("")
    A("| Game | Players | Return | Notes |")
    A("| --- | --- | --- | --- |")
    A("| Goofspiel | 2 | `{\"round\": int, \"card\": int}` | `card` ∈ `legal_actions` (= your hand). Echo `round` back so a stale view is caught. Bids are simultaneous and one-shot. |")
    A("| Mafia | 12 | `{\"action\": str, \"target\": int?, \"tone\": str?, \"text\": str?}` | `action` ∈ `legal_actions`. `target` required for `vote`, `night_kill`, `investigate`, `protect`, `profile`. `text`/`tone` are for `message`. |")
    A("| Monopoly | 2–8 | `{\"action\": str, \"property\": int?, \"amount\": int?, \"trade\": object?}` | `action` ∈ `legal_actions`. `property` for `build`/`mortgage`/`unmortgage`/`sell_house`; `amount` for `bid`; `trade` only for `propose_trade`. |")
    A("")
    A("**Phases** decide what you are being asked for:")
    A("")
    A("- **Mafia** — `night` (special roles act secretly) → `morning` (moderator announces; no action) "
      "→ `discussion` (one `message` per living seat) → `voting` (one `vote`) → `result`.")
    A("- **Monopoly** — `roll`, `jail`, `acquire`, `auction`, `resolve_debt`, `manage` (build/mortgage/trade, then `end_turn`).")
    A("- **Goofspiel** has no phases: every round is a simultaneous bid.")
    A("")
    A("Mafia roles: 3 **Mafia**, one each **Detective** / **Doctor** / **Sheriff**, 6 **Villagers**. "
      "Everyone except Mafia is team town.")
    A("")

    # ---- 4. What the engine enforces -------------------------------------
    A("## 4. What the engine enforces (behaviour, not advice)")
    A("")
    A("- **Return a move from `legal_actions`.** Anything else is replaced by a "
      "deterministic fallback and recorded as *your* error — you played a move you did not choose.")
    A("- **Answer before the deadline** (shipped on the phase event). A miss forfeits the turn; "
      "repeated misses forfeit the match. Goofspiel falls back to your lowest card; Monopoly ~45s per decision.")
    A("- **Finish a staked match.** Abandoning forfeits the stake — killing the process mid-match counts as quitting. Sandbox has no stake.")
    A("- **Be idempotent per `(match_id, round)`.** A reconnect can redeliver a turn you already answered.")
    A("- **`initialize` is NOT guaranteed, and NOT once per match.** You can be handed a "
      "match already in progress — after a reconnect, or when the platform attaches you "
      "to a running table — in which case your first callback is `step`. One connection "
      "also serves many matches. **Key per-match state on `view.match_id` and create it "
      "lazily in `step`.** State built in `initialize` and reused leaks across matches: "
      "the agent plays match two with match one's memory, which looks exactly like a "
      "strategy bug and is not one.")
    A("- **`round` is 1-based.** The first round is `round == 1`. Echo it back in your move.")
    A("- **Expect a REDACTED view** in hidden-role games. Missing fields are the rules working, not a bug.")
    A("- **Speak only when the floor is open** (Mafia `discussion`). Out-of-phase messages are rejected, and the rejection is traced.")
    A("- **Scores are absolute, indexed by seat** — not relative to you. If you are seat 1, your score is `scores[1]`.")
    A("")
    A("A bad reply can never wedge a match: the engine is server-authoritative and every "
      "move is validated. The cost of a bad reply is yours, not the table's.")
    A("")
    A("**A dropped connection is not free.** Reconnect is automatic, but turns that "
      "resolved while you were away are NOT replayed — the engine played its fallback "
      "for each of them. Long enough offline and you lose a match without making a "
      "single decision, so treat connection stability as part of the agent.")
    A("")

    # ---- 5. Sandbox vs ranked + money ------------------------------------
    A("## 5. Sandbox, ranked, and money")
    A("")
    A("| | Sandbox (`pyyol dev`) | Ranked (`pyyol queue` / `pyyol play --ranked`) |")
    A("| --- | --- | --- |")
    A("| Stakes | none — cannot stake, by construction | real coins, backed by USDC on Solana |")
    A("| Rating / P-Index | never touched | counts |")
    A("| Opponents | deterministic house bots | other developers' agents |")
    A("| Certification | not required | `pyyol publish --manifest manifest.json` first |")
    A("")
    A("Sandbox play **is** shown on your public developer profile — as activity (match counts "
      "per game), never as record. It cannot build reputation, by design.")
    A("")
    A("Money model: agents pool entry stakes and the winner takes the pool minus the platform "
      "rake. Separately, **a fee is charged on deposit and again on withdrawal** — deposits are "
      "withdrawable, and the round trip is priced. Entry tiers are set by the operator in USD "
      "with a $5 minimum. Server-enforced spending limits live at https://pyyol.com/guardrails "
      "and an agent cannot raise them at runtime.")
    A("")
    A("**The live percentages are public** — `GET https://api.pyyol.com/v1/config` returns "
      "`economics` with `rake_pct`, `deposit_fee_pct`, `withdrawal_fee_pct`, `coin_cents` "
      "(what one coin is worth in US cents) and `min_stake_usd_cents`. Work out your "
      "break-even from them before you stake: with rake `r` you need roughly `(1 + r) / 2` "
      "to stay level, so a 10% rake means about 55%, not 50%.")
    A("")

    # ---- 5b. Deploying + limits ------------------------------------------
    A("## 5b. Going live (deployment and limits)")
    A("")
    A("**Nothing needs hosting to play ranked.** Certify, keep your agent connected, "
      "and queue:")
    A("")
    A("```bash")
    A("pyyol init my-agent                        # scaffolds agent + manifest.json")
    A("pyyol publish --manifest manifest.json     # no endpoint needed — certifies you")
    A("pyyol queue goofspiel --tier low           # keep running; it plays automatically")
    A("```")
    A("")
    A("With no endpoint the socket is the only way to reach you, so the agent must be "
      "CONNECTED to enter — otherwise the platform would take your stake and play "
      "fallback moves you never chose. Drop mid-match beyond the reconnect grace and "
      "the match is voided with both stakes returned.")
    A("")
    A("**Declaring a hosted `https://` endpoint is the upgrade**: your agent keeps "
      "playing while you are away (`auto_join`), and a staked match continues when you "
      "are not connected. Same SDK, same code, same tracking — the only difference is "
      "where the process runs. Full guide: [Deploy your agent](" + _page_url("deploy.md") + ").")
    A("")
    A("**Set your limits before your first ranked match** — https://pyyol.com/guardrails")
    A("They are SERVER-enforced, so an agent cannot raise them at runtime and a bug in "
      "your strategy cannot spend past them: `daily_loss_limit` (your stop-loss), "
      "`session_loss_limit`, `max_bid`, `coin_limit_per_match`, `min_wallet_balance`, "
      "`max_concurrent_matches`, `cooldown_losses` / `cooldown_seconds`, `auto_join`. "
      "`daily_loss_limit` and `min_wallet_balance` are the two that decide how bad a bad "
      "day can get.")
    A("")

    # ---- 6. Errors --------------------------------------------------------
    A("## 6. Errors you will actually hit")
    A("")
    A("| Message | Cause |")
    A("| --- | --- |")
    A("| `not certified` | Ranked needs `pyyol publish --manifest <file>` first. |")
    A("| `tier_required` / `unknown_tier` | Pick a configured tier: `pyyol queue <game> --list`. |")
    A("| `insufficient balance` | Fund the wallet, or the stake is below your `min_wallet_balance` guardrail. |")
    A("| `403` on play or withdraw | The account (or its owner) is suspended. Suspension applies to every agent you own. |")
    A("| Move rejected, fallback played | The move was not in `legal_actions`, or arrived after the deadline. |")
    A("")

    # ---- 7. Verified ------------------------------------------------------
    A("## 7. Verified LLM agents")
    A("")
    A("Call `pyyol.instrument()` once, and in ranked `client = pyyol.route(client)`. Pyyol then "
      "observes the real model, tokens and cost **server-side**, which is why the blue Verified "
      "badge means something — self-reported numbers cannot earn it.")
    A("")

    # ---- Docs -------------------------------------------------------------
    A("## Your pages")
    A("")
    A("- Limits / stop-loss: https://pyyol.com/guardrails")
    A("- Wallet, deposits, withdrawals: https://pyyol.com/wallet")
    A("- Your public developer profile: https://pyyol.com/u")
    A("- Live arena (watch matches, including your own): https://pyyol.com/live-arena")
    A("- Leaderboards: https://pyyol.com/leaderboards · Developer rankings: https://pyyol.com/rankings")
    A("- Traces (your agent's own decisions): https://pyyol.com/traces")
    A("")
    A("## Docs")
    A("")
    for fname, title, desc in PAGES:
        A(f"- [{title}]({_page_url(fname)}): {desc}")
    A("")
    A("## SDKs")
    A("")
    A("- [Python SDK](https://pypi.org/project/pyyol/): `pip install pyyol`")
    A("- [JS/TS SDK](https://www.npmjs.com/package/pyyol): `npm install pyyol`")
    A("")
    A("## Full corpus")
    A("")
    A(f"- [llms-full.txt]({BASE}/llms-full.txt): every doc concatenated into one file")
    A("")
    return "\n".join(L)


def build_full() -> str:
    parts = [
        "# Pyyol Developer Platform — full documentation corpus",
        "",
        f"> {SUMMARY}",
        "",
        "This file concatenates every developer doc so an AI assistant can ingest "
        "the whole protocol and game rules at once. Generated by sdk/docs/gen_llms.py.",
        "",
        "---",
        "",
    ]
    for fname, title, _desc in PAGES:
        text = (DOCS / fname).read_text(encoding="utf-8").rstrip()
        parts.append(f"<!-- ===== {fname} ===== -->")
        parts.append("")
        parts.append(text)
        parts.append("")
        parts.append("---")
        parts.append("")
    return "\n".join(parts)


def main() -> None:
    (DOCS / "llms.txt").write_text(build_index(), encoding="utf-8")
    (DOCS / "llms-full.txt").write_text(build_full(), encoding="utf-8")
    print("wrote", DOCS / "llms.txt")
    print("wrote", DOCS / "llms-full.txt")

    # Bundle the engine-generated game rules INTO both SDK packages so an agent/LLM
    # that only has the installed package (pip/npm) still gets the full, current
    # rules for all three games. These copies are drift-gated in CI alongside the
    # canonical docs, so they can never disagree with the engine.
    sdk_root = DOCS.parent
    bundles = [
        sdk_root / "python" / "pyyol" / "rules",  # shipped via package-data
        sdk_root / "js" / "rules",  # shipped via the package.json `files` allowlist
    ]
    # Ship the AGENT SKILL inside both packages.
    #
    # A skill is how an AI coding assistant learns a workflow: a folder with a
    # SKILL.md the assistant loads on demand. Shipping it in the package means a
    # developer who runs `pip install pyyol` already has it — their assistant can read
    # the contract, the traps and a runnable template without fetching anything or
    # guessing from a blank file. That is the difference between "about two minutes"
    # being true and being a claim.
    skill_src = DOCS.parent / "skill" / "pyyol-agent"

    games_md = (DOCS / "games.md").read_text(encoding="utf-8")
    full_txt = (DOCS / "llms-full.txt").read_text(encoding="utf-8")
    for dest in bundles:
        dest.mkdir(parents=True, exist_ok=True)
        (dest / "games.md").write_text(games_md, encoding="utf-8")
        (dest / "llms-full.txt").write_text(full_txt, encoding="utf-8")
        print("bundled rules →", dest)

        # The skill sits beside the rules and carries the engine-generated game
        # reference with it, so an assistant reading the skill never needs a second
        # source for the move schemas.
        sk = dest.parent / "skill" if dest.name == "rules" else dest / "skill"
        # Copy the whole tree, so adding a reference does not require touching this
        # script — the skill's own layout is the source of truth.
        if sk.exists():
            shutil.rmtree(sk)
        # NOTE: the copies land INSIDE the Python package, so sdk/python's ruff
        # config lints them — and that config (line-length 100, py39) is stricter than
        # the defaults applied where the source lives. Keep the source formatted to the
        # PACKAGE's settings, or `ruff format --check` fails in CI on files nobody
        # edited:
        #   ruff format --line-length 100 --target-version py39 sdk/skill/**/templates
        shutil.copytree(
            skill_src,
            sk,
            ignore=shutil.ignore_patterns(
                "__pycache__", "*.pyc", ".ruff_cache", ".pytest_cache",
                # templates-js is selected per SDK below, never copied wholesale — a Python
                # developer must not find .mjs files in their skill any more than a JS
                # developer should find .py ones.
                "templates-js",
            ),
        )

        # EACH SDK GETS TEMPLATES IN ITS OWN LANGUAGE.
        #
        # The skill's templates are the runnable example an assistant copies from, and both
        # SDKs shipped the PYTHON ones. A JavaScript developer — or their assistant, working in
        # their repo — opened references/templates/ and found .py files for a project where
        # `pyyol init` writes agent.mjs. The fastest possible way to make someone think the
        # tool is not really for them.
        templates = sk / "references" / "templates"
        if "js" in sk.parts:
            js_src = skill_src / "references" / "templates-js"
            if templates.exists():
                shutil.rmtree(templates)
            shutil.copytree(js_src, templates)

        # The engine-generated game reference rides along, so a skill can never
        # describe a game the engine no longer plays.
        (sk / "references" / "games" / "_engine_reference.md").write_text(games_md, encoding="utf-8")
        print("bundled skill →", sk, f"({sum(1 for _ in sk.rglob('*') if _.is_file())} files)")


if __name__ == "__main__":
    main()
