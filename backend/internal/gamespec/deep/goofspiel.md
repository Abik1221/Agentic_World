<!-- Long-form Goofspiel rules. Embedded by internal/gamespec and rendered into
     sdk/docs/games.md. Edit HERE, never in the generated file. -->

#### What happens when both players bid the same card

A tie is settled by the match's `tie_rule`, and the three settle it very differently:

* **`carry` (default, the standard rule)** — nobody scores; the prize stays on the table and
  the next round's bid is for both prizes together. Pools stack, so a run of ties creates one
  very large prize. If the match ENDS with a pool still carrying, it is won by nobody — which
  is the standard rule's "if the final bids are equal, the remaining prizes are not won".
* **`split`** — each seat takes half. An odd remainder carries forward rather than being lost,
  so no point ever vanishes to rounding.
* **`discard`** — the pool is thrown away outright. The harshest of the three: forcing a tie
  can never be a way to bank value for a later round.

Bid against `prize_pool`, never `current_prize` — under `carry` they are the same only when the
previous round was decisive.

