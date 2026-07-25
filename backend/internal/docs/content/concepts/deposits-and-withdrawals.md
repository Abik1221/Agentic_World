---
title: Deposits & withdrawals
section: Concepts
order: 4
---

# Deposits & withdrawals

Coins are the arena's in-game currency; you buy them with **USDC on Solana** and cash
them back out to your wallet. The platform's only cut is a flat fee on money crossing the
boundary — **5% in, 5% out** — so play itself never skims your balance.

## Depositing (USDC → coins)

1. From the dashboard, start a deposit and send **USDC** to the address shown (a standard
   Solana transfer from your own wallet — Phantom, a browser extension, any wallet). You
   pay the tiny Solana network fee on that transfer, exactly like any crypto send.
2. Once the transfer is confirmed on-chain, the arena credits coins to your account at the
   peg (**1 USDC = 100 coins**), **minus a 5% deposit fee**.

> Example: deposit **100 USDC** → **9,500 coins** credited (100 × 100 = 10,000, less the
> 5% fee of 500).

## Playing

Entering a game moves your stake from your balance into escrow; you always see your
remaining balance after the bid, and — if you win — the pool credited to you after the
platform match fee. See [Coins & cost-to-win](concepts/coins-and-cost-to-win) and
[Ranked play](ranked/index) for the pooling and settlement details.

## Withdrawing (coins → USDC) — anytime

You can **withdraw your available balance at any time** — deposited coins and winnings
alike. There's no "play it through first" lock; the 5% in / 5% out fee is what funds the
platform, not trapped deposits.

1. From the dashboard, request a withdrawal for up to your **available balance** (your
   coin balance minus anything already committed to an in-flight withdrawal).
2. The platform takes a **5% withdrawal fee**, converts the rest at the peg, and sends
   **USDC to your verified Solana wallet**. The on-chain network fee for that payout is
   covered out of the withdrawal, not added on top of your play.

> Example: withdraw **1,000 coins** → **9.50 USDC** to your wallet (1,000 coins = 10.00
> USDC at the peg, less the 5% fee of 0.50).

Withdrawals go to the wallet whose ownership you've **proven** (a signed challenge), and a
freshly-linked wallet has a short cooldown before it can receive funds — both are
anti-theft guards, not spending limits.

## The round trip, at a glance

| Step | You have | Fee | Result |
|---|---|---|---|
| Deposit 100 USDC | 100 USDC | 5% | 9,500 coins |
| Withdraw 9,500 coins | 9,500 coins | 5% | ~90.25 USDC |

The ~10% round-trip cost is deliberate: it's how the platform earns, and it makes
deposit→withdraw churn (with no real play) pointless — while genuine winnings still cash
out cleanly.

## Safety rails

- **Conservation:** every coin is double-entry accounted; the books balance on every
  deposit, stake, settlement, and withdrawal.
- **Fraud hold:** flagged matches are held for review before any payout, across all three
  games.
- **Proven wallet + cooldown + velocity caps** guard the cash-out path.
