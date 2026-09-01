# Market study — where Pyyol actually stands, August 2026

Desk research only. No code was changed to produce this. Sources are linked inline; where a
claim rests on a single source it says so.

---

## 1. The uncomfortable part first

**"LLMs playing games, scored with game theory" is not a defensible position in 2026.** It is
an active, published research area with at least six systems in it:

| system | what it covers |
|---|---|
| [TMGBench](https://arxiv.org/pdf/2410.10479) | All **144** game types in the Robinson–Goforth topology of 2×2 games, plus synthetic story-based variants; tests rational reasoning, robustness, Theory-of-Mind |
| [GAMA-Bench](https://arxiv.org/pdf/2410.10479) | Eight classical multi-agent games, specifically >2 simultaneous agents |
| MAgIC | Two social-deduction games + three game-theory scenarios; reasoning, planning, social ability |
| [GameBench](https://axi.lims.ac.uk/paper/2406.06613) | Strategic reasoning across game environments |
| [WGSR-Bench](https://arxiv.org/pdf/2506.10264) | Wargame-based game-theoretic strategic reasoning |
| LLMsPark | Game-theoretic evaluation across ~15 models |

TMGBench alone covers a **superset** of the game space Pyyol touches. Goofspiel, Mafia and
Monopoly are three points inside a taxonomy someone else has already enumerated exhaustively
and published with an OpenReview thread.

Any claim of the form "the first to evaluate models through game theory" is refutable in one
search, by anyone, in under a minute. It should be retired from all Pyyol material.

### And the second uncomfortable part

What Pyyol measures today is narrower than the phrase "agentic benchmark" implies. Verified
against our own wire format: text-only, single-turn, complete state re-serialised each call,
`tool_choice` **forced**. No vision, no memory test, no multi-turn dialogue, no tool
*selection*. The model chooses arguments; it never chooses whether to act.

That is a real capability — but it is not agency, and the gap between the two is exactly what
a hostile reader would attack first.

---

## 2. The agent benchmark layer is also crowded — but differently

The [2026 benchmarks that matter for production decisions](https://decodethefuture.org/en/ai-agent-benchmarks-2026/)
are GAIA, SWE-bench Verified, OSWorld, τ²-bench, WebArena, and METR HCAST / time-horizons.
Beyond those, [the landscape](https://benchmarkingagents.com/agent-benchmarks/) splits into:

- **Web** — WebArena, VisualWebArena, Mind2Web, WorkArena (ServiceNow), BrowseComp
- **OS / mobile** — OSWorld, AndroidWorld, MobileBench
- **Tools / APIs** — τ-bench, BFCL, AgentBench (8 environments), ToolLLM (16,000+ APIs),
  MINT, MCP-Bench, MCP-Atlas, MCPMark, Toolathlon

**The structural observation that matters:** every one of these is **one agent against an
environment**, scored on task completion. None of them is agent-versus-agent. None has an
opponent that adapts. The environment does not want the agent to fail — it is inert.

That is the seam.

---

## 3. The commercial layer — this is the real competitor set for an enterprise pivot

[Thirteen active platforms](https://arize.com/resources/llm-and-agent-evaluation-platforms/)
compete on six criteria: vendor durability, trajectory scoring depth, CI/CD integration,
pricing transparency, deployment model, ease of use.

| platform | position |
|---|---|
| **Braintrust** | Code-first `Eval()` primitive, seven SDKs, unifies offline + online scoring. **CI/CD regression detection with automatic PR comments.** |
| **LangSmith** | Framework-level replay/debugging; strongest for teams already on LangChain/LangGraph |
| **Galileo** | **GitHub Actions CI/CD with configurable quality gates**; eval scores block regressions |
| **Arize / Phoenix** | Vendor-agnostic tracing, open-source + managed split |
| **Confident AI (DeepEval)** | Named first among [CI/CD tools for agent testing](https://www.confident-ai.com/knowledge-base/compare/best-ci-cd-tools-testing-ai-agents-before-production-2026) |
| **Langfuse** | Open-source tracing/eval |

**Read this carefully, because it changes the plan:** GitHub-integrated CI/CD evaluation is
**already shipped** by at least three funded competitors. Braintrust posts PR comments;
Galileo gates merges. If Pyyol's pitch is "we integrate with your GitHub CI", that is table
stakes in 2026, not a differentiator.

**But** — and this is the opening — every one of these platforms is **BYO-tests**. They are
observability and regression *infrastructure*. They score the tests you write. **None of them
supplies an adversary.**

---

## 4. The fastest-moving adjacent space: adversarial evaluation

This is where the user's instinct is strongest, and also where the competition is arriving.

- [REDAgentBench](https://arxiv.org/html/2608.10669v1) finds that **adaptive attackers can
  overturn conclusions drawn from fixed evaluations** — which is, in one sentence, the
  scientific argument for Pyyol's whole thesis, published by someone else.
- [NRT-Bench](https://arxiv.org/html/2606.20408v1) runs multi-turn red-teaming against a
  five-role LLM operator team in a simulated nuclear control room.
- The state of the art has moved to **RL-trained adversaries, formalised as an MDP**, staging
  coordinated multi-turn attacks that adapt to the target's prior responses.
- Microsoft publishes an [agent-eval scenario library](https://github.com/microsoft/ai-agent-eval-scenario-library)
  including red-teaming and adversarial evaluation.
- **NIST CAISI** launched a three-pillar programme (agent security, interoperability, identity)
  on **17 Feb 2026** and open-sourced **AgentDojo-Inspect** for agent-hijacking evaluation.

### The regulatory tailwind is real and dated

- **EU AI Act GPAI obligations** — in force **2 Aug 2025**
- **EU AI Act high-risk system requirements** — **2 Aug 2026**

Organisations must *demonstrate adversarial testing* for compliance. That is a budget line
with a legal deadline attached, which is a materially better thing to sell into than
"benchmark curiosity".

**Caveat:** this space is safety red-teaming — jailbreaks, hijacking, prompt injection. It is
adjacent to, not identical with, *strategic* exploitability. That distinction is Pyyol's to
claim, and also Pyyol's to prove is worth paying for.

---

## 5. What is genuinely open

Filtering the above, four things are thin on the ground. Ranked by how defensible each is.

**1. Multi-vendor agent-vs-agent interaction.** Every benchmark in §2 is agent-vs-environment.
Putting a GPT-backed agent, a Claude-backed agent and a Gemini-backed agent into one shared
environment with conflicting incentives, and measuring the *population dynamics* — who
coalitions form against, who gets exploited, who adapts — is not something the surveyed
literature does. This is the strongest open seam.

**2. Strategic exploitability as a product metric.** Exploitability (`ε = max_σ' u(σ',σ) − v`)
is standard in computational game theory and essentially absent from commercial agent
evaluation. "How much can an adaptive opponent extract from your agent" is a question no
platform in §3 answers, and it has a rigorous definition rather than an LLM judge's opinion.

**3. Longitudinal identity.** The eval platforms score *runs*. Nobody maintains a persistent,
versioned index of how a given agent behaves as it changes over months. Pyyol already has the
P-Index machinery for exactly this.

**4. Real stakes.** No competitor has economic consequence attached to the outcome. Whether
that is a moat or a compliance liability is genuinely unclear and should be treated as an
open question, not an asset.

---

## 6. Where the proposed direction is weak — read before committing

Four problems, none fatal, all needing an answer.

**Reproducibility versus adaptivity.** An adaptive adversary is a moving target. If the
opponent changes between runs, two scores are not comparable — and reproducibility is the
core promise of a benchmark. This is a genuine tension, not a detail. The likely resolution is
versioned, frozen adversary snapshots plus a separately-reported adaptive score, but that must
be designed deliberately.

**Cost per evaluation is doubled or worse.** Agent-vs-agent means paying for inference on
*both* sides, times the number of rounds, times replicates needed for significance. Our own
experience is the evidence: **$4 bought roughly six usable Goofspiel matches.** A 50-match
seat-swapped comparison across eight models is a five-figure compute bill. The business model
has to absorb that, and "we ran out of budget mid-match" is not a story that survives a
customer conversation.

**Enterprise buyers ask about their workflow, not abstract strategy.** A bank evaluating a
support agent wants τ²-bench-style policy adherence on *their* policies. "Your agent is
exploitable in a negotiation game" is an interesting finding they may not have a budget line
for. The bridge from strategic metrics to a purchase order is unproven and is the single
biggest commercial risk.

**Construct validity.** Does exploitability in Goofspiel predict anything about an agent
negotiating a real contract? Nobody has shown that. Until it is shown, the metric is elegant
and unvalidated — and a sophisticated buyer will ask precisely this.

---

## 7. Recommended positioning

Not "the first game-theory benchmark". That is false.
Not "an agent benchmark". That market has GAIA, τ²-bench, OSWorld and SWE-bench in it.
Not "CI/CD for agent evals". Braintrust and Galileo shipped it.

The honest and defensible framing:

> **Adversarial evaluation infrastructure for multi-agent systems** — measuring how an agent
> behaves against opponents that actively search for its weaknesses, with game-theoretic
> reference points instead of a judge model, and a persistent index of how that behaviour
> changes across versions.

Three things must be true for that to hold, and they are testable claims rather than
assertions:

1. An adaptive adversary finds exploits that fixed test suites miss. *REDAgentBench already
   suggests this is true generally; Pyyol would need to show it on its own environments.*
2. Exploitability correlates with a failure mode a buyer already cares about.
   **Unproven — this is the critical experiment.**
3. The measurement is reproducible enough to be a benchmark. *Requires the frozen-adversary
   design above.*

---

## 8. What to verify before committing engineering

- Talk to five prospective enterprise buyers and ask what they *currently* spend on agent
  evaluation and what fails in production. Do not describe Pyyol first.
- Check whether EU AI Act Article 15 (robustness / accuracy for high-risk systems) can be
  read to require adversarial testing of agentic systems. If yes, that is the wedge, and the
  deadline is **2 Aug 2026**.
- Establish whether NIST CAISI's agent-security pillar intends to publish a reference
  adversarial suite. If it does, that is either a standard to align with or a competitor with
  a government mandate.
- Run the construct-validity experiment: does exploitability on a Pyyol environment predict
  failure on a τ²-bench-style task? A negative result kills the thesis cheaply, which is worth
  knowing early.

---

## 9. Sources

- [TMGBench](https://arxiv.org/pdf/2410.10479) · [OpenReview](https://openreview.net/forum?id=1KvYxcAihR)
- [GameBench](https://axi.lims.ac.uk/paper/2406.06613) · [WGSR-Bench](https://arxiv.org/pdf/2506.10264)
- [AI Agent Benchmarks 2026](https://decodethefuture.org/en/ai-agent-benchmarks-2026/) · [Agent benchmark landscape](https://benchmarkingagents.com/agent-benchmarks/)
- [LLM & agent evaluation platforms compared](https://arize.com/resources/llm-and-agent-evaluation-platforms/) · [Best CI/CD tools for testing agents](https://www.confident-ai.com/knowledge-base/compare/best-ci-cd-tools-testing-ai-agents-before-production-2026) · [Braintrust on CI/CD evals](https://www.braintrust.dev/articles/best-ai-evals-tools-cicd-2025) · [Galileo platform comparison](https://galileo.ai/blog/best-ai-agent-evaluation-platforms)
- [REDAgentBench](https://arxiv.org/html/2608.10669v1) · [NRT-Bench](https://arxiv.org/html/2606.20408v1) · [Agentic-era red teaming](https://arxiv.org/pdf/2605.04019) · [Microsoft agent-eval scenario library](https://github.com/microsoft/ai-agent-eval-scenario-library)
