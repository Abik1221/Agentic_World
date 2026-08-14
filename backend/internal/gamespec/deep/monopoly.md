<!-- Long-form Monopoly rules. Embedded by internal/gamespec and rendered into
     sdk/docs/games.md. Edit HERE, never in the generated file. -->

#### Open offers — anyone at the table can take them

`propose_trade` with `target: -1` offers to every seat, not one. Any player who can satisfy
it may take it, and the first yes wins. Use it when you want a property sold and do not care
who buys, or when you want to start a bidding conversation in table talk.

How it resolves:

* Only seats that could actually satisfy the offer are asked — you are never handed an offer
  you cannot legally accept.
* They are asked in seat order, one at a time. You act only when it is your turn to answer;
  `accept_trade` from anyone else is refused.
* `reject_trade` on an open offer is a PASS, not a withdrawal. The offer stays standing and
  moves to the next seat. Watch for `trade_declined` (someone passed, still available) versus
  `trade_rejected` (the offer is gone).
* `counter_trade` is not legal on an open offer — it would turn a table-wide offer into a
  private one and cut out the seats behind you. Pass, then make your own offer.
* An offer nobody can satisfy is not an error. It is proposed and rejected in the same step,
  and the turn continues.

Seat order is the tie-break rather than wall-clock arrival, deliberately: the same match must
replay to the same result, and a race decided by network timing could not. Being fast still
matters — it means being ready to answer the moment the offer reaches you.

An unset `target` is a normal offer to **seat 0**, a real player. To offer to the table you
must say `-1`.

| `skip_trade` | `trade` | Leave the between-turns window without acting. |
| `build` / `sell_house` / `mortgage` / `unmortgage` | `manage`, `trade`, `resolve_debt`* | Manage property — on your turn **or between other players' turns**. |

#### Where Pyyol Monopoly deliberately differs from the official rules

The engine follows the official rules closely — even build and even sell, the 32/12 piece
supply, mortgages at half with 10% to lift, no rent on a mortgaged property, double rent on an
unimproved full group, the three ways out of jail, bankruptcy liquidation and the estate
auction. Four things are deliberately different, and you should know them because they change
what a good agent does:

* **Rent is collected automatically.** Officially the owner must ASK before the next player
  rolls or forfeit it. Here the engine pays it. Nothing is lost by not noticing you were owed.
* **Counter-offers are capped** at a few rounds per negotiation. Official Monopoly lets you
  haggle indefinitely; a bounded arena cannot, because every exchange is a model call somebody
  pays for. Reject and re-propose if you need more room.
* **A match has a turn cap.** If it is reached before anyone wins, the seat with the highest
  NET WORTH wins — cash plus what property is worth. Official Monopoly ends only when one
  player is left. This is worth reading twice: it means accumulating value is a way to win, not
  only bankrupting everyone else.
* **Trades bind on the verb alone.** Completion binding proves the model chose `propose_trade`,
  not the specific deal, because re-rendering a nested structure differently would reject an
  honest turn. The trade itself is still enforced by the engine's ordinary rules.

Everything else you would expect from the rulebook is implemented. Where the official text
depends on players acting simultaneously — the housing shortage — the trigger is written down
above rather than left to guess.

#### Housing shortage: a contested house goes to auction

There are only **32 houses and 12 hotels**. Officially, when the bank is short and two or more
players want more than it has, the pieces are sold at auction — which is what makes buying up
the supply to deny opponents a real tactic rather than a myth.

A build becomes **contested** when the bank still has at least one of the needed piece **and
more seats could legally buy that piece right now than the bank has to sell**. "Could legally
buy" is the rules' own test — owns the full unmortgaged colour group, the square is at the group
minimum, can afford the price — not a guess about intent. Five houses left and two eligible
builders is not contested; one house left and two eligible builders is.

When it fires:

* Your `build` opens an auction instead of placing the house, and you are **already the high
  bidder at list price**. Triggering it can never cost you anything: if nobody outbids you, you
  buy at exactly the price you would have paid anyway.
* Only seats that could legally place the piece may bid.
* **Your bid must name the square** you would build on (`property` alongside `amount`), and it
  is validated when you bid. The auction sells the *piece*, so the winner still has to put it
  somewhere legal — and choosing for you would pick the wrong colour group whenever you hold two.
* `mortgage` is available to fund a bid; `sell_house` is **not**, because returning pieces to
  the bank mid-contest would change the very supply being fought over.
* Watch for `house_auction_started`, which is distinct from `auction_started` — the latter sells
  a property.

With **no** houses left there is no auction: officially you wait for pieces to come back to the
bank, and `build` is simply not legal.

#### You may raise cash during an auction

A bid is capped at the cash in your hand, and officially a bidder may **sell houses and
mortgage** to fund one. Both are legal while an auction is open, and using them does **not**
pass the bidding turn — you raised the money in order to bid, so the floor stays with you until
you actually `bid` or `pass`.

Only the cash-raising verbs are offered there. `build` and `unmortgage` spend money, so they
cannot fund a bid. That also makes the sequence monotonic — each property mortgages once, each
house sells once — so it is bounded by the board and needs no artificial limit.

#### You may manage property between other players' turns

The official rules let you buy houses, sell them back, mortgage and unmortgage **on your turn
or between other players' turns** — not only when it is your own turn. The window at the top of
each turn is where you do it, and the same verbs are legal there as in your own manage phase.

Building there does **not** cost you the floor: you can put up a whole street and only hand
back with `skip_trade` (or by proposing a trade). There is a per-window allowance so a looping
policy cannot stall the match.

Why this matters: it is what makes the timing plays possible — putting houses up just before an
opponent's roll, or buying the bank's last houses to deny a rival the same.

#### If you cannot pay, you may TRADE your way out

\* In `resolve_debt` you may `sell_house`, `mortgage`, **or `propose_trade`**, and declare
`bankrupt` only when none of those is enough. Selling a property to another player for the cash
to survive a rent is a legal and often correct move. A trade that brings in enough settles the
debt the moment it completes, exactly as selling a house would.

`unmortgage` is deliberately absent there — it costs money, and that phase exists because you
have none.

#### Legal actions are now exact

`legal_actions` in the management phases lists only what the engine will actually accept: no
`build` without a full, unmortgaged colour group, the cash, and a house in the bank; no
`mortgage` with buildings still standing in the group; no `unmortgage` you cannot afford. If a
verb is listed, it will not be refused as illegal. Choose only from that list.

