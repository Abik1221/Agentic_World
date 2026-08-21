# Pyyol Enterprise — which wedge, and is anyone there already

Desk research, August 2026. Companion to `MARKET-2026.md`. Consumer arena (agent-vs-agent
gambling, community, P-Index) is assumed to continue unchanged; this is only about the
enterprise product beside it.

Two candidates were proposed: **customer support** and **negotiation**. They have opposite
answers, and the difference is large enough to decide the roadmap.

---

## 1. Customer support agent testing — do not enter

**The slot is taken, commoditising, and someone is already benchmarking the benchmarkers.**

| who | position |
|---|---|
| [Coval](https://www.coval.ai/blog/coval-vs-cekura) | Simulation-first, CI/CD-heavy, autonomous-vehicle testing methodology applied to voice |
| [Cekura](https://www.cekura.ai/blogs/ai-voice-agent-testing-tools) | Auto-generates test scenarios from agent config; **$30/month self-serve** |
| Hamming | Automated scenario generation, audio signal analysis, production call replay |
| Bluejay, Roark, Tuner, [Maxim AI](https://www.getmaxim.ai/articles/best-voice-agent-evaluation-tools-in-2026/), Future AGI | Adjacent testing / observability |
| Cyara | Enterprise incumbent |

Two facts should end the discussion on their own:

- **Price has already collapsed.** Cekura self-serves at $30/month. A market with $30 entry
  pricing is not one to arrive at late without a structural advantage.
- **An academic paper already scores the scorers.** "Testing the Testers" (arXiv, 2026) rates
  Coval at 48.9 and Cekura at 43.0 on evaluation accuracy. When third parties are benchmarking
  the benchmark vendors, the category is mature.

Academically the slot is also filled: **τ-bench and τ²-bench are customer-support benchmarks**
(retail and airline domains, policy adherence, tool-agent-user interaction), and τ² is named as
one of the [six benchmarks that matter in 2026](https://decodethefuture.org/en/ai-agent-benchmarks-2026/).

There is also a structural mismatch worth naming, because it is the deeper reason and it would
still apply if the market were empty. **A simulated customer is not an adversary.** It is a
persona following a scenario, and it does not want the agent to fail. Every asset Pyyol has —
exploitability, equilibrium distance, adaptive opponents, duplicate scheduling — exists to
measure behaviour against something that is *actively trying to win*. Pointed at customer
support, that machinery has nothing to bite on, and Pyyol would compete on scenario coverage
and audio analysis, where Coval and Hamming are years ahead.

---

## 2. Negotiation agent testing — this is the wedge, and it is genuinely open

### The agents are already deployed at scale, with real money moving

- **Pactum AI** — described as the most commercially proven autonomous negotiation platform,
  deployed at **Walmart, Maersk** and Fortune 500 procurement organisations
- **Nibble Technology**, **PRMAI** — negotiation and source-to-pay agents
- AI agents are projected to manage **60–70% of end-to-end transactional procurement**
- [One procurement analysis](https://nibbletechnology.com/the-state-of-autonomous-negotiation/)
  calls autonomous negotiation *"the only AI project done in 2026 that truly pays for itself in
  a concrete and measurable way"*

### And there is a precisely-shaped hole where trust should be

The single most important number found in this study:

> **92% of CPOs are assessing or planning AI capability. 49% have piloted. Only 4% run AI at
> large scale.**
> — [State of AI in Procurement 2026](https://aiassemblylines.com/resources/state-of-ai-procurement-2026-benchmarks)

The distance between 49% piloted and 4% at scale is not a capability gap — the agents work well
enough to pilot. It is a **trust gap**. Nobody will let an agent autonomously commit real money
at scale without evidence of how it behaves when the counterparty is competent and motivated.

ICML 2026 made the same point from the research side: agent capability claims cannot be taken
at face value without independent benchmark evidence.

### Nobody is doing the independent testing

Searched specifically for it. What exists:

- **Safety red-teaming** — [HackerOne](https://www.hackerone.com/product/ai-red-teaming),
  Straiker, Mend, plus [agent-orchestrated assessment research](https://www.helpnetsecurity.com/2026/05/21/ai-red-teaming-agents-research/).
  All of it targets jailbreaks, unsafe outputs, policy violations, data disclosure.
- **Academic negotiation work** — [Adversarial Negotiation Dynamics in Generative Language
  Models](https://arxiv.org/pdf/2501.00069) (legal contracts), and a [large-scale autonomous
  negotiation competition](https://arxiv.org/pdf/2503.06416).

What does **not** exist, as far as this search could establish: a commercial service answering

> **"How much money does your negotiation agent leave on the table against a competent
> adversary, and how reproducibly?"**

Safety red-teaming asks whether an agent can be made to say something harmful. This asks
whether it can be made to *lose*. Different question, different buyer, different budget line.

---

## 3. Why negotiation fits Pyyol's existing engine and support does not

This is the part that makes it a wedge rather than a pivot. Negotiation is **inherently
two-sided, adversarial and quantitatively scored** — which is the exact shape the arena
already models.

| Pyyol asset built for the arena | what it becomes in procurement |
|---|---|
| Exploitability (`ε = max_σ' u(σ',σ) − v`) | Dollars an adaptive counterparty can extract |
| Margin-aware ratings (Massey) | Money left on the table is *literally* the margin |
| Duplicate scheduling (same board, both seats) | Same negotiation scenario, both sides — removes "you got the easy supplier" |
| Anytime-valid confidence sequences | Stop testing as soon as the result is certain, without invalidating it |
| Completion binding (HMAC) | Provenance: this decision came from the model it claims |
| Adaptive adversary | The competent counterparty the buyer is actually afraid of |

None of these transfer to customer support in any meaningful way. All of them transfer to
negotiation almost unchanged — only the payoff matrix differs.

**The strategic observation:** when Walmart deploys Pactum against suppliers, and those
suppliers deploy their own AI negotiators, the result is **agent-versus-agent with real money
at stake**. That is not an analogy for Pyyol's engine. It is the same problem with a different
payoff function, and it is arriving whether or not anyone is ready to measure it.

---

## 4. Who pays, and the conflict to avoid

Three candidate buyers, in order of how clean the incentive is.

1. **The enterprise deploying the agent** (CPO / procurement). Wants assurance before letting
   software commit money. Clean incentive: they benefit from a *bad* result too, because it
   stops a costly mistake. **This is the buyer to build for.**
2. **Compliance / audit.** EU AI Act high-risk obligations land **2 Aug 2026**, and adversarial
   testing must be demonstrated. Budget with a legal deadline.
3. **The agent vendor** (Pactum et al.) buying a credibility badge. Pays well, and **corrupts
   the product** — a benchmark paid for by the thing it measures has no value to buyer #1. If
   vendor money is ever taken, it must be for a run whose result is published regardless of
   outcome, or the asset is gone.

---

## 5. Honest risks

**Construct validity is still unproven, and it is now the critical path.** Exploitability on
Goofspiel predicting negotiation performance is an assumption. The cheap experiment is to build
one procurement-shaped scenario and check whether an agent that scores badly on it actually
concedes more against a strong counterparty. A negative result kills this cheaply, which is
worth knowing before any enterprise sale.

**Domain scenarios are real work.** Cards are not contracts. Credible scenarios need payment
terms, delivery windows, volume tiers, credit, penalty clauses — and enough realism that a
procurement professional recognises them. This is domain modelling, not engine work.

**Enterprise sales cycles are long** and procurement organisations are conservative. The 4%
figure cuts both ways: the trust gap is the opportunity *and* the reason deals will be slow.

**Cost.** Every evaluation pays for inference on both sides. Our own data: **$4 bought roughly
six usable matches.** Pricing must reflect that from day one.

**Pactum could build it in-house.** They have the domain and the deployments. The defence is
independence — a vendor's self-assessment is not evidence — but that defence only holds if
Pyyol never sells the badge (see §4.3).

---

## 6. Recommendation

**Do not build customer support testing.** Late, crowded, $30/month floor, the testers are
themselves benchmarked, and none of Pyyol's machinery gives an advantage there.

**Build negotiation.** The agents are deployed at Walmart scale, the trust gap is quantified at
49%→4%, no independent evaluator exists, a regulatory deadline lands 2 Aug 2026, and every
asset built for the arena transfers with the payoff matrix swapped.

**Frame it as insurance, not as a leaderboard.** The buyer is not asking "which negotiation
agent is best". They are asking *"what happens when mine meets a competent opponent, and can
I show my board the evidence."*

**Before writing code:** build one procurement scenario, run the construct-validity check in
§5, and talk to five procurement leaders about the 49%→4% gap without mentioning Pyyol.
