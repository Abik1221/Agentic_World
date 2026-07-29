package solanadeposit

import (
	"fmt"
	"strconv"
	"strings"
)

// AcceptedMint is one SPL token the platform takes deposits in.
//
// DECIMALS TRAVEL WITH THE MINT, and that is the whole point of this type. The credit
// path converts base units to coins with `base * CoinsPerUSDC / 10^decimals`. While
// there was exactly one accepted token, a single configured decimals value was fine.
// The moment a second token is accepted, a global value is a money bug: a 9-decimal
// token credited against a hardcoded 6 pays out 1000x, and it pays it to whoever
// noticed first.
type AcceptedMint struct {
	// Symbol is what the UI shows ("USDC", "USDT").
	Symbol string
	// Mint is the SPL mint address. This is the identity — symbols are not unique and
	// anyone can deploy a token calling itself USDC.
	Mint string
	// Decimals is THIS token's base-unit exponent, used for its own conversion.
	Decimals int
}

// MintRegistry is the set of tokens deposits are accepted in, keyed by mint address.
type MintRegistry struct {
	byMint map[string]AcceptedMint
	order  []AcceptedMint // config order, so the UI lists them predictably
}

// ParseMints builds a registry from "SYMBOL:MINT:DECIMALS" triples, comma-separated.
//
// ONLY USD-PEGGED STABLECOINS BELONG HERE. The platform credits coins at a fixed peg
// (CoinsPerUSDC), so it treats one whole token as one dollar. Accept a volatile token
// on those terms and you have sold coins at whatever that token is worth today —
// someone deposits $0.10 of it and receives $1.00 of play. Supporting arbitrary tokens
// safely needs live price feeds and slippage handling, which is a different product.
// The registry is an allowlist for exactly that reason: unknown mints are not credited.
func ParseMints(spec string) (*MintRegistry, error) {
	r := &MintRegistry{byMint: map[string]AcceptedMint{}}
	for _, raw := range strings.Split(spec, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		if len(parts) != 3 {
			return nil, fmt.Errorf("mint %q: want SYMBOL:MINT:DECIMALS", entry)
		}
		symbol := strings.ToUpper(strings.TrimSpace(parts[0]))
		mint := strings.TrimSpace(parts[1])
		decimals, err := strconv.Atoi(strings.TrimSpace(parts[2]))
		if err != nil {
			return nil, fmt.Errorf("mint %q: decimals must be a number", entry)
		}
		if symbol == "" || mint == "" {
			return nil, fmt.Errorf("mint %q: symbol and mint address are required", entry)
		}
		// 0..18 covers every SPL token in existence; outside it the pow10 conversion
		// either does nothing useful or overflows.
		if decimals < 0 || decimals > 18 {
			return nil, fmt.Errorf("mint %q: decimals out of range (0-18)", entry)
		}
		if _, dup := r.byMint[mint]; dup {
			return nil, fmt.Errorf("mint %s listed twice", mint)
		}
		m := AcceptedMint{Symbol: symbol, Mint: mint, Decimals: decimals}
		r.byMint[mint] = m
		r.order = append(r.order, m)
	}
	if len(r.order) == 0 {
		return nil, fmt.Errorf("no accepted mints configured")
	}
	return r, nil
}

// Lookup resolves a mint address. ok is false for anything not explicitly accepted —
// the caller must NOT credit an unknown mint, since its decimals and its dollar value
// are both unknown.
func (r *MintRegistry) Lookup(mint string) (AcceptedMint, bool) {
	if r == nil {
		return AcceptedMint{}, false
	}
	m, ok := r.byMint[mint]
	return m, ok
}

// Default returns the first configured mint — what the UI preselects.
func (r *MintRegistry) Default() AcceptedMint {
	if r == nil || len(r.order) == 0 {
		return AcceptedMint{}
	}
	return r.order[0]
}

// All returns the accepted tokens in configuration order.
func (r *MintRegistry) All() []AcceptedMint {
	if r == nil {
		return nil
	}
	out := make([]AcceptedMint, len(r.order))
	copy(out, r.order)
	return out
}

// Resolve picks the mint for a requested symbol or address, falling back to the
// default when the request is empty. It never falls back for an UNKNOWN request:
// silently substituting a different token than the one asked for is how someone ends
// up sending funds to a flow built for something else.
func (r *MintRegistry) Resolve(requested string) (AcceptedMint, error) {
	if r == nil || len(r.order) == 0 {
		return AcceptedMint{}, fmt.Errorf("no accepted mints configured")
	}
	req := strings.TrimSpace(requested)
	if req == "" {
		return r.Default(), nil
	}
	if m, ok := r.byMint[req]; ok {
		return m, nil
	}
	upper := strings.ToUpper(req)
	for _, m := range r.order {
		if m.Symbol == upper {
			return m, nil
		}
	}
	return AcceptedMint{}, fmt.Errorf("%s is not accepted here", req)
}
