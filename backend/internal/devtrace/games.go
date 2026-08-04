package devtrace

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/agent-arena/arena/internal/engine/monopoly"
)

// PER-GAME EVENT MAPPING — turning the match log into sentences.
//
// Two problems were being solved badly here, and both were invisible until you looked
// at a real developer's page.
//
// ONE: only Goofspiel existed. The mapper handled prize_revealed / card_sealed /
// round_revealed / agent_says, and the SQL allowlist admitted only those. Mafia emits
// phase|moderator|night|message|vote|eliminate|victory and Monopoly emits
// turn_started|dice_rolled|moved|…|match_finished — so a Mafia match contributed ZERO
// rows and a Monopoly match contributed two bare lines. A developer with a hundred
// Mafia matches saw a page that looked broken, and reasonably concluded the platform
// had lost their games.
//
// TWO: what did render was numbers. `{"target": 4}`, `{"property": 39}`,
// `{"seat": 2, "delta": -350}`. The developer had to hold the board layout and the
// seating chart in their head to read their own agent's history. So every entry now
// carries a `summary`: one finished sentence, composed HERE where the seat names, the
// board and the payload shapes are all in scope. The client renders prose it is given
// rather than inventing prose from integers.
//
// WHAT IS DELIBERATELY NOT MAPPED — `night`. Mafia's night payload carries the
// mafia's chosen target (and a `secret` field). It is excluded from the SQL allowlist
// entirely, not merely skipped here, because during an ACTIVE match that is the one
// fact a living player must not be able to read. Two gates, same as the rest of this
// package: a mistake in one mapper must not be able to leak it.
//
// OTHER SEATS are admitted only for a FINISHED match (see mapOptions.Finished). Their
// votes, table talk and eliminations are public game facts — a Mafia timeline without
// them is unreadable, since "why was I voted out" is the whole question — but a live
// match must not become a live scoreboard of everyone else's play.

// mapOptions carries the per-match context the mapper needs to write sentences.
type mapOptions struct {
	// SeatName resolves a seat to a display name in THIS match. Nil, or a miss,
	// degrades to "seat N" rather than dropping the event.
	SeatName func(seat int) string
	// Finished admits other seats' public events. False while a match is live.
	Finished bool
}

func (o mapOptions) seat(n int) string {
	if o.SeatName != nil {
		if name := o.SeatName(n); name != "" {
			return fmt.Sprintf("%s (seat %d)", name, n)
		}
	}
	return fmt.Sprintf("seat %d", n)
}

// Entry types the mapper produces beyond the existing agent_* set.
//
//   - match_note is a public fact about the match that the agent did not choose: a
//     phase change, a dice roll, rent falling due. Separated from agent_decision on
//     purpose — "what my agent chose" and "what happened to my agent" are different
//     questions, and a timeline that mixes them makes the first one unanswerable.
const (
	TypeDecision   = "agent_decision"
	TypeSaid       = "agent_said"
	TypeNote       = "match_note"
	TypeStarted    = "match_started"
	TypeFinished   = "match_finished"
	TypeEliminated = "agent_eliminated"
)

// mapRows turns raw match_event rows into timeline entries, newest first.
//
// Each row arrives already attributed to one of the caller's agents and to that
// agent's seat (see matchActivityQuery). meta supplies per-match context; it may
// return the zero value for a match we know nothing extra about.
func mapRows(rows []MatchRow, meta func(matchPublicID string) mapOptions) []Entry {
	lat := goofspielLatency(rows)
	out := make([]Entry, 0, len(rows))
	for _, r := range rows {
		opts := mapOptions{}
		if meta != nil {
			opts = meta(r.MatchPublicID)
		}
		base := Entry{
			At:      r.CreatedAt,
			Status:  "ok",
			Game:    r.Game,
			MatchID: r.MatchPublicID,
			AgentID: r.AgentPublicID,
		}
		switch r.Game {
		case "mafia":
			out = append(out, mapMafia(base, r, opts)...)
		case "monopoly":
			out = append(out, mapMonopoly(base, r, opts)...)
		default: // goofspiel, and anything sharing its log shape
			out = append(out, mapGoofspiel(base, r, opts, lat)...)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// ── Goofspiel ────────────────────────────────────────────────────────────────

type roundKey struct {
	match string
	agent string
	round int
}

// goofspielLatency pairs each round's prize reveal with this agent sealing its card.
// The gap is real thinking time — a better measure than the sampled timings table,
// whose match_id was never populated. Only Goofspiel's log supports it: Mafia and
// Monopoly have no per-decision start marker, so their entries carry no latency
// rather than a fabricated one (the per-match average comes from the benchmark row).
func goofspielLatency(rows []MatchRow) map[roundKey]int64 {
	prizeAt := map[roundKey]time.Time{}
	sealedAt := map[roundKey]time.Time{}
	for _, r := range rows {
		switch r.Type {
		case "prize_revealed":
			var p struct{ Round int }
			if json.Unmarshal(r.Payload, &p) == nil {
				k := roundKey{r.MatchPublicID, r.AgentPublicID, p.Round}
				// Keep the FIRST reveal: a retry or duplicate row must not move the
				// start of the clock and make a decision look instant.
				if _, seen := prizeAt[k]; !seen {
					prizeAt[k] = r.CreatedAt
				}
			}
		case "card_sealed":
			var p struct {
				Round int
				Seat  int
			}
			if json.Unmarshal(r.Payload, &p) == nil && p.Seat == r.Seat {
				sealedAt[roundKey{r.MatchPublicID, r.AgentPublicID, p.Round}] = r.CreatedAt
			}
		}
	}
	out := make(map[roundKey]int64, len(sealedAt))
	for k, end := range sealedAt {
		start, ok := prizeAt[k]
		if !ok {
			continue
		}
		ms := end.Sub(start).Milliseconds()
		// A negative or absurd gap means these two rows are not the pair we think they
		// are; reporting it would put a fictional number on a latency chart.
		if ms < 0 || ms > int64(6*time.Hour/time.Millisecond) {
			continue
		}
		out[k] = ms
	}
	return out
}

func mapGoofspiel(base Entry, r MatchRow, opts mapOptions, lat map[roundKey]int64) []Entry {
	switch r.Type {
	case "round_revealed":
		// The decision WITH its value. card_sealed deliberately carries no card (a
		// spectator must not learn a move early), so the reveal is where a developer
		// finally sees what their agent actually played.
		var p struct {
			Round  int    `json:"round"`
			Prize  int    `json:"prize"`
			Cards  [2]int `json:"cards"`
			Winner int    `json:"winner"`
		}
		if json.Unmarshal(r.Payload, &p) != nil || r.Seat < 0 || r.Seat > 1 {
			return nil
		}
		won := p.Winner == r.Seat
		e := base
		e.Type = TypeDecision
		e.Operation = "bid"
		e.LatencyMS = lat[roundKey{r.MatchPublicID, r.AgentPublicID, p.Round}]
		verdict := "lost it"
		if won {
			verdict = "won it"
		}
		e.Detail = map[string]any{
			"round":   p.Round,
			"action":  fmt.Sprintf("played %d", p.Cards[r.Seat]),
			"prize":   p.Prize,
			"won":     won,
			"by_me":   true,
			"summary": fmt.Sprintf("Round %d: bid %d for the %d-point prize and %s (opponent bid %d)", p.Round, p.Cards[r.Seat], p.Prize, verdict, p.Cards[1-r.Seat]),
		}
		return []Entry{e}

	case "agent_says":
		var p struct {
			Round int    `json:"round"`
			Seat  int    `json:"seat"`
			Text  string `json:"text"`
			Kind  string `json:"kind"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		if p.Seat != r.Seat && !opts.Finished {
			return nil
		}
		e := base
		e.Type = TypeSaid
		e.Detail = map[string]any{"round": p.Round, "by_me": p.Seat == r.Seat}
		// A rationale is the agent explaining its own move; table talk is what it said
		// to the opponent. The client renders them differently, so they stay distinct.
		if p.Kind == "rationale" {
			e.Detail["rationale"] = p.Text
			e.Detail["summary"] = fmt.Sprintf("Round %d reasoning: %s", p.Round, p.Text)
		} else {
			e.Detail["text"] = p.Text
			e.Detail["summary"] = fmt.Sprintf("%s said: %s", opts.seat(p.Seat), p.Text)
		}
		return []Entry{e}

	case "match_finished":
		var p struct {
			Scores [2]int `json:"scores"`
			Winner int    `json:"winner"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		e := base
		e.Type = TypeFinished
		won := p.Winner == r.Seat
		e.Detail = map[string]any{"won": won}
		if r.Seat >= 0 && r.Seat <= 1 {
			e.Detail["score"] = p.Scores[r.Seat]
			e.Detail["summary"] = fmt.Sprintf("Match finished %d–%d — %s",
				p.Scores[r.Seat], p.Scores[1-r.Seat], outcomeWord(won, p.Winner < 0))
		}
		return []Entry{e}

	case "match_created":
		e := base
		e.Type = TypeStarted
		e.Detail = map[string]any{"summary": "Match started"}
		return []Entry{e}
	}
	return nil
}

// ── Mafia ────────────────────────────────────────────────────────────────────

func mapMafia(base Entry, r MatchRow, opts mapOptions) []Entry {
	switch r.Type {
	case "vote":
		var p struct {
			From   int `json:"from"`
			Target int `json:"target"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		mine := p.From == r.Seat
		if !mine && !opts.Finished {
			return nil
		}
		e := base
		if mine {
			e.Type = TypeDecision
			e.Operation = "vote"
		} else {
			e.Type = TypeNote
		}
		e.Detail = map[string]any{
			"by_me":   mine,
			"action":  fmt.Sprintf("voted %s", opts.seat(p.Target)),
			"target":  p.Target,
			"summary": fmt.Sprintf("%s voted to eliminate %s", opts.seat(p.From), opts.seat(p.Target)),
		}
		return []Entry{e}

	case "message":
		var p struct {
			From   int    `json:"from"`
			Tone   string `json:"tone"`
			Text   string `json:"text"`
			Target *int   `json:"target"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		mine := p.From == r.Seat
		if !mine && !opts.Finished {
			return nil
		}
		e := base
		e.Type = TypeSaid
		at := ""
		if p.Target != nil {
			at = " → " + opts.seat(*p.Target)
		}
		e.Detail = map[string]any{
			"by_me": mine, "text": p.Text, "tone": p.Tone,
			"summary": fmt.Sprintf("%s (%s)%s: %s", opts.seat(p.From), toneWord(p.Tone), at, p.Text),
		}
		return []Entry{e}

	case "eliminate":
		var p struct {
			Target int    `json:"target"`
			Cause  string `json:"cause"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		mine := p.Target == r.Seat
		if !mine && !opts.Finished {
			return nil
		}
		e := base
		e.Type = TypeNote
		if mine {
			// Its own death is the single most important line in a Mafia timeline: it
			// is where the agent stops being able to act at all.
			e.Type = TypeEliminated
		}
		by := "the town's vote"
		if p.Cause == "mafia" {
			by = "the mafia"
		}
		who := opts.seat(p.Target)
		if mine {
			who = "Your agent"
		}
		e.Detail = map[string]any{
			"by_me": mine, "cause": p.Cause, "target": p.Target,
			"summary": fmt.Sprintf("%s was eliminated by %s", who, by),
		}
		return []Entry{e}

	case "phase":
		var p struct {
			Day   int    `json:"day"`
			Phase string `json:"phase"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		e := base
		e.Type = TypeNote
		e.Detail = map[string]any{
			"round": p.Day, "phase": p.Phase, "by_me": false,
			"summary": fmt.Sprintf("Day %d — %s", p.Day, phaseWord(p.Phase)),
		}
		return []Entry{e}

	case "moderator":
		var p struct{ Text string }
		if json.Unmarshal(r.Payload, &p) != nil || p.Text == "" {
			return nil
		}
		e := base
		e.Type = TypeNote
		e.Detail = map[string]any{"by_me": false, "summary": p.Text}
		return []Entry{e}

	case "victory":
		var p struct {
			Team string `json:"team"`
			Text string `json:"text"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		e := base
		e.Type = TypeFinished
		// Which team the agent was on is NOT in this payload, and guessing it from a
		// win/loss the caller has not been told would be inventing data. The roster on
		// the detail endpoint carries the role; this line reports the fact only.
		e.Detail = map[string]any{
			"team": p.Team, "by_me": false,
			"summary": fmt.Sprintf("Match finished — %s win. %s", p.Team, p.Text),
		}
		return []Entry{e}

	case "match_created":
		e := base
		e.Type = TypeStarted
		e.Detail = map[string]any{"summary": "Match started"}
		return []Entry{e}

	case "match_finished":
		e := base
		e.Type = TypeFinished
		e.Detail = map[string]any{"summary": "Match finished"}
		return []Entry{e}
	}
	return nil
}

// ── Monopoly ─────────────────────────────────────────────────────────────────

// monopoly.Board() returns a FRESH COPY of the 40-square board on every call, so it is
// resolved once here rather than per event: a long Monopoly match maps thousands of rows
// and several of them name a square.
var boardOnce = sync.OnceValue(monopoly.Board)

// propertyName turns a board index into the name a developer recognises. The whole
// point of the detail view is not making somebody memorise that 39 is Boardwalk.
func propertyName(idx int) string {
	board := boardOnce()
	if idx < 0 || idx >= len(board) {
		return fmt.Sprintf("square %d", idx)
	}
	if n := board[idx].Name; n != "" {
		return n
	}
	return fmt.Sprintf("square %d", idx)
}

func mapMonopoly(base Entry, r MatchRow, opts mapOptions) []Entry {
	// `seat` on most payloads is the actor. Anything not the caller's seat is a public
	// fact and only admitted once the match is over.
	admit := func(seat int) bool { return seat == r.Seat || opts.Finished }

	note := func(seat int, summary string, extra map[string]any) []Entry {
		if !admit(seat) {
			return nil
		}
		e := base
		e.Type = TypeNote
		e.Detail = map[string]any{"by_me": seat == r.Seat, "summary": summary}
		for k, v := range extra {
			e.Detail[k] = v
		}
		return []Entry{e}
	}
	decision := func(seat int, op, action, summary string, extra map[string]any) []Entry {
		if !admit(seat) {
			return nil
		}
		e := base
		e.Type = TypeNote
		if seat == r.Seat {
			e.Type = TypeDecision
			e.Operation = op
		}
		e.Detail = map[string]any{"by_me": seat == r.Seat, "action": action, "summary": summary}
		for k, v := range extra {
			e.Detail[k] = v
		}
		return []Entry{e}
	}

	switch r.Type {
	case "match_created":
		var p monopoly.MatchCreatedPayload
		e := base
		e.Type = TypeStarted
		summary := "Match started"
		if json.Unmarshal(r.Payload, &p) == nil && p.Players > 0 {
			summary = fmt.Sprintf("Match started — %d players, $%d starting cash", p.Players, p.StartingCash)
			e.Detail = map[string]any{"players": p.Players, "summary": summary}
		} else {
			e.Detail = map[string]any{"summary": summary}
		}
		return []Entry{e}

	case "turn_started":
		var p monopoly.TurnStartedPayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		return note(p.Seat, fmt.Sprintf("Turn %d — %s to play", p.TurnCount, opts.seat(p.Seat)),
			map[string]any{"round": p.TurnCount})

	case "dice_rolled":
		var p monopoly.DiceRolledPayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		extra := ""
		if p.Doubles {
			extra = " (doubles)"
		}
		return note(p.Seat, fmt.Sprintf("%s rolled %d + %d = %d%s", opts.seat(p.Seat), p.Die1, p.Die2, p.Total, extra), nil)

	case "moved":
		var p monopoly.MovedPayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		go_ := ""
		if p.PassedGo {
			go_ = ", passing GO"
		}
		return note(p.Seat, fmt.Sprintf("%s moved to %s%s", opts.seat(p.Seat), propertyName(p.To), go_), nil)

	case "cash_changed":
		var p monopoly.CashChangedPayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		dir := "received"
		amt := p.Delta
		if p.Delta < 0 {
			dir, amt = "paid", -p.Delta
		}
		return note(p.Seat, fmt.Sprintf("%s %s $%d (%s) — cash now $%d",
			opts.seat(p.Seat), dir, amt, reasonWord(p.Reason), p.Balance),
			map[string]any{"delta": p.Delta, "balance": p.Balance})

	case "rent_paid":
		var p monopoly.RentPaidPayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		// Rent has two sides and BOTH may be the caller: paying it and collecting it
		// are opposite facts and must not read the same.
		if p.From == r.Seat {
			return note(p.From, fmt.Sprintf("Your agent paid $%d rent to %s for %s",
				p.Amount, opts.seat(p.To), propertyName(p.Property)),
				map[string]any{"delta": -p.Amount})
		}
		if p.To == r.Seat {
			return note(p.To, fmt.Sprintf("Your agent collected $%d rent from %s for %s",
				p.Amount, opts.seat(p.From), propertyName(p.Property)),
				map[string]any{"delta": p.Amount})
		}
		return note(-1, fmt.Sprintf("%s paid $%d rent to %s for %s",
			opts.seat(p.From), p.Amount, opts.seat(p.To), propertyName(p.Property)), nil)

	case "property_purchased":
		var p monopoly.PropertyPurchasedPayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		return decision(p.Seat, "buy", fmt.Sprintf("bought %s", propertyName(p.Property)),
			fmt.Sprintf("%s bought %s for $%d", opts.seat(p.Seat), propertyName(p.Property), p.Price),
			map[string]any{"delta": -p.Price})

	case "card_drawn":
		var p monopoly.CardDrawnPayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		return note(p.Seat, fmt.Sprintf("%s drew %s: %s", opts.seat(p.Seat), deckWord(p.Deck), p.Text), nil)

	case "went_to_jail":
		var p monopoly.WentToJailPayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		return note(p.Seat, fmt.Sprintf("%s went to jail (%s)", opts.seat(p.Seat), reasonWord(p.Reason)), nil)

	case "left_jail":
		var p monopoly.LeftJailPayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		return note(p.Seat, fmt.Sprintf("%s left jail (%s)", opts.seat(p.Seat), reasonWord(p.Method)), nil)

	case "house_built", "house_sold":
		var p monopoly.BuildPayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		verb, op := "built on", "build"
		if r.Type == "house_sold" {
			verb, op = "sold a house on", "sell"
		}
		return decision(p.Seat, op, fmt.Sprintf("%s %s", verb, propertyName(p.Property)),
			fmt.Sprintf("%s %s %s — now %s", opts.seat(p.Seat), verb, propertyName(p.Property), housesWord(p.Houses)), nil)

	case "mortgaged", "unmortgaged":
		var p monopoly.MortgagePayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		verb, op := "mortgaged", "mortgage"
		if r.Type == "unmortgaged" {
			verb, op = "lifted the mortgage on", "unmortgage"
		}
		return decision(p.Seat, op, fmt.Sprintf("%s %s", verb, propertyName(p.Property)),
			fmt.Sprintf("%s %s %s for $%d", opts.seat(p.Seat), verb, propertyName(p.Property), p.Amount), nil)

	case "auction_started":
		var p monopoly.AuctionStartedPayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		return note(-1, fmt.Sprintf("Auction opened for %s", propertyName(p.Property)), nil)

	case "bid_placed":
		var p monopoly.BidPayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		return decision(p.Seat, "bid", fmt.Sprintf("bid %d", p.Amount),
			fmt.Sprintf("%s bid $%d for %s", opts.seat(p.Seat), p.Amount, propertyName(p.Property)), nil)

	case "auction_passed":
		var p monopoly.AuctionPassedPayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		return decision(p.Seat, "pass", "passed",
			fmt.Sprintf("%s passed on %s", opts.seat(p.Seat), propertyName(p.Property)), nil)

	case "auction_won", "auction_unsold":
		var p monopoly.AuctionResultPayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		if r.Type == "auction_unsold" {
			return note(-1, fmt.Sprintf("%s went unsold", propertyName(p.Property)), nil)
		}
		return note(p.Seat, fmt.Sprintf("%s won %s at auction for $%d",
			opts.seat(p.Seat), propertyName(p.Property), p.Amount), map[string]any{"delta": -p.Amount})

	case "bankrupt":
		var p monopoly.BankruptPayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		if !admit(p.Seat) {
			return nil
		}
		e := base
		e.Type = TypeNote
		who := opts.seat(p.Seat)
		if p.Seat == r.Seat {
			// Bankruptcy ends the agent's match. Reported as a failure-class row so it
			// surfaces in the same place as timeouts and illegal moves, which is where
			// a developer looks to ask "what went wrong".
			e.Type = TypeEliminated
			who = "Your agent"
		}
		to := "the bank"
		if p.Creditor >= 0 {
			to = opts.seat(p.Creditor)
		}
		e.Detail = map[string]any{
			"by_me": p.Seat == r.Seat, "cause": p.Reason,
			"summary": fmt.Sprintf("%s went bankrupt owing $%d to %s (%s)", who, p.Amount, to, reasonWord(p.Reason)),
		}
		return []Entry{e}

	case "trade_proposed", "trade_executed", "trade_rejected":
		var p monopoly.TradePayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		if p.Proposer != r.Seat && p.Target != r.Seat && !opts.Finished {
			return nil
		}
		verb := map[string]string{
			"trade_proposed": "proposed a trade to",
			"trade_executed": "completed a trade with",
			"trade_rejected": "was turned down by",
		}[r.Type]
		e := base
		e.Type = TypeNote
		if p.Proposer == r.Seat && r.Type == "trade_proposed" {
			e.Type = TypeDecision
			e.Operation = "trade"
		}
		e.Detail = map[string]any{
			"by_me":  p.Proposer == r.Seat || p.Target == r.Seat,
			"action": "trade",
			"summary": fmt.Sprintf("%s %s %s — offering %s for %s",
				opts.seat(p.Proposer), verb, opts.seat(p.Target),
				tradeSide(p.GiveProps, p.GiveCash), tradeSide(p.WantProps, p.WantCash)),
		}
		return []Entry{e}

	case "turn_ended":
		var p monopoly.TurnEndedPayload
		if json.Unmarshal(r.Payload, &p) != nil || p.Seat != r.Seat {
			// Only the caller's own turn boundaries: every seat's would triple the
			// timeline's length with rows carrying no information.
			return nil
		}
		return note(p.Seat, "Your agent ended its turn", nil)

	case "match_finished":
		var p monopoly.MatchFinishedPayload
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		e := base
		e.Type = TypeFinished
		won := p.Winner == r.Seat
		e.Detail = map[string]any{"won": won}
		if r.Seat >= 0 && r.Seat < len(p.NetWorths) {
			e.Detail["score"] = p.NetWorths[r.Seat]
			e.Detail["summary"] = fmt.Sprintf("Match finished — %s, final net worth $%d",
				outcomeWord(won, p.Winner < 0), p.NetWorths[r.Seat])
		} else {
			e.Detail["summary"] = fmt.Sprintf("Match finished — %s", outcomeWord(won, p.Winner < 0))
		}
		return []Entry{e}

	case "agent_says":
		var p struct {
			Turn int    `json:"turn"`
			Seat int    `json:"seat"`
			Text string `json:"text"`
			Kind string `json:"kind"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		if p.Seat != r.Seat && !opts.Finished {
			return nil
		}
		e := base
		e.Type = TypeSaid
		e.Detail = map[string]any{"round": p.Turn, "by_me": p.Seat == r.Seat}
		if p.Kind == "rationale" {
			e.Detail["rationale"] = p.Text
			e.Detail["summary"] = fmt.Sprintf("Turn %d reasoning: %s", p.Turn, p.Text)
		} else {
			e.Detail["text"] = p.Text
			e.Detail["summary"] = fmt.Sprintf("%s said: %s", opts.seat(p.Seat), p.Text)
		}
		return []Entry{e}
	}
	return nil
}

// ── Wording helpers ──────────────────────────────────────────────────────────
//
// Small on purpose. Each one exists because the raw enum value is a token, not
// English, and the whole reason this file exists is that the developer was being
// handed tokens.

func outcomeWord(won, tie bool) string {
	switch {
	case tie:
		return "a draw"
	case won:
		return "your agent won"
	default:
		return "your agent lost"
	}
}

func toneWord(t string) string {
	switch t {
	case "accuse":
		return "accusing"
	case "defend":
		return "defending"
	case "claim":
		return "claiming a role"
	case "alliance":
		return "proposing an alliance"
	case "info":
		return "sharing information"
	case "":
		return "talking"
	default:
		return t
	}
}

func phaseWord(p string) string {
	switch p {
	case "night":
		return "night falls"
	case "morning":
		return "morning"
	case "discussion":
		return "discussion opens"
	case "voting":
		return "voting opens"
	case "result":
		return "the result is read"
	case "":
		return "phase change"
	default:
		return p
	}
}

func reasonWord(r string) string {
	switch r {
	case "":
		return "no reason given"
	case "go_to_jail_space":
		return "landed on Go To Jail"
	case "three_doubles":
		return "three doubles"
	case "jail_fine":
		return "jail fine"
	default:
		// Enum values are snake_case; a space reads as a sentence and an unknown value
		// still says something true rather than being hidden.
		out := make([]byte, 0, len(r))
		for i := 0; i < len(r); i++ {
			if r[i] == '_' {
				out = append(out, ' ')
				continue
			}
			out = append(out, r[i])
		}
		return string(out)
	}
}

func deckWord(d string) string {
	if d == "community_chest" {
		return "a Community Chest card"
	}
	return "a Chance card"
}

func housesWord(n int) string {
	switch {
	case n >= 5:
		return "a hotel"
	case n == 1:
		return "1 house"
	case n <= 0:
		return "no houses"
	default:
		return fmt.Sprintf("%d houses", n)
	}
}

func tradeSide(props []int, cash int) string {
	names := make([]string, 0, len(props))
	for _, p := range props {
		names = append(names, propertyName(p))
	}
	switch {
	case len(names) == 0 && cash == 0:
		return "nothing"
	case len(names) == 0:
		return fmt.Sprintf("$%d", cash)
	case cash == 0:
		return joinAnd(names)
	default:
		return fmt.Sprintf("%s plus $%d", joinAnd(names), cash)
	}
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	case 2:
		return xs[0] + " and " + xs[1]
	}
	out := ""
	for i, x := range xs[:len(xs)-1] {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out + " and " + xs[len(xs)-1]
}
