// Package blockchain is a small, dependency-free Solana JSON-RPC client used by
// the deposit listener (Beta wallet pipeline P2). It speaks only the two methods
// the read-path needs — getSignaturesForAddress and getTransaction — and reduces
// a transaction to the SPL-token balance deltas that matter for crediting a
// deposit. It never signs or broadcasts (that is the withdrawal path, P3).
//
// Deposit detection uses the Solana Pay "reference" pattern: each deposit session
// mints a unique reference pubkey that the payer includes as a read-only account
// in the transfer. getSignaturesForAddress(reference) then returns exactly the
// transactions that touched it, and we confirm the USDC actually landed in the
// platform token account by diffing pre/post token balances.
package blockchain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Config configures the RPC endpoint. Commitment defaults to "finalized" — the
// strongest guarantee, so a credited deposit can never be rolled back.
type Config struct {
	RPCURL      string
	Commitment  string // "finalized" (default) | "confirmed"
	HTTPTimeout time.Duration
}

// Client is a minimal Solana JSON-RPC HTTP client. Safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client
}

// New builds a client, applying defaults.
func New(cfg Config) *Client {
	if cfg.Commitment == "" {
		cfg.Commitment = "finalized"
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 15 * time.Second
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.HTTPTimeout}}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func (c *Client) call(ctx context.Context, method string, params []any, out any) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.RPCURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("solana rpc %s: http %d", method, resp.StatusCode)
	}
	var rr rpcResponse
	if err := json.Unmarshal(raw, &rr); err != nil {
		return fmt.Errorf("solana rpc %s: decode: %w", method, err)
	}
	if rr.Error != nil {
		return fmt.Errorf("solana rpc %s: %d %s", method, rr.Error.Code, rr.Error.Message)
	}
	if out != nil && len(rr.Result) > 0 {
		if err := json.Unmarshal(rr.Result, out); err != nil {
			return fmt.Errorf("solana rpc %s: decode result: %w", method, err)
		}
	}
	return nil
}

// SignatureInfo is one entry from getSignaturesForAddress.
type SignatureInfo struct {
	Signature          string `json:"signature"`
	Slot               uint64 `json:"slot"`
	Err                any    `json:"err"` // non-nil ⇒ the transaction failed on-chain
	ConfirmationStatus string `json:"confirmationStatus"`
	BlockTime          *int64 `json:"blockTime"`
}

// SignaturesForAddress returns recent signatures that reference `address`
// (newest first), at the configured commitment.
func (c *Client) SignaturesForAddress(ctx context.Context, address string, limit int) ([]SignatureInfo, error) {
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	var out []SignatureInfo
	params := []any{address, map[string]any{"limit": limit, "commitment": c.cfg.Commitment}}
	if err := c.call(ctx, "getSignaturesForAddress", params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// TokenAccountBalance returns the SPL-token balance (base units) of a token
// account, at the configured commitment. Used by the solvency monitor to compare
// the platform hot-wallet's on-chain USDC against outstanding payout liability.
func (c *Client) TokenAccountBalance(ctx context.Context, tokenAccount string) (int64, error) {
	var out struct {
		Value struct {
			Amount string `json:"amount"`
		} `json:"value"`
	}
	params := []any{tokenAccount, map[string]any{"commitment": c.cfg.Commitment}}
	if err := c.call(ctx, "getTokenAccountBalance", params, &out); err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(out.Value.Amount, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("solana rpc getTokenAccountBalance: invalid amount %q: %w", out.Value.Amount, err)
	}
	return n, nil
}

// TokenCredit is a positive SPL-token balance change on one token account within
// a transaction (post − pre, in base units).
type TokenCredit struct {
	Account string // the token account (ATA) pubkey
	Owner   string // the account's owner wallet
	Mint    string
	Delta   int64 // base units credited (post − pre)
}

// Transaction is the reduced view the listener needs: whether it failed, the
// per-token-account credits it produced, and the full set of account keys in the
// message. AccountKeys lets the caller independently re-verify that a deposit
// session's Solana Pay reference actually appears in THIS transaction (the RPC's
// getSignaturesForAddress(reference) binding is otherwise taken on trust). (H3)
type Transaction struct {
	Signature   string
	Slot        uint64
	Failed      bool
	Credits     []TokenCredit
	AccountKeys []string
}

// HasAccount reports whether pubkey is one of the transaction's account keys.
func (t *Transaction) HasAccount(pubkey string) bool {
	for _, k := range t.AccountKeys {
		if k == pubkey {
			return true
		}
	}
	return false
}

type tokenBalance struct {
	AccountIndex  uint32 `json:"accountIndex"`
	Mint          string `json:"mint"`
	Owner         string `json:"owner"`
	UITokenAmount struct {
		Amount string `json:"amount"`
	} `json:"uiTokenAmount"`
}

// base parses the token amount (base units) and PROPAGATES the parse error rather
// than silently yielding MaxInt64 on overflow/garbage — a malformed or malicious
// RPC amount must never be treated as a real (huge) credit. (M7)
func (b tokenBalance) base() (int64, error) {
	n, err := strconv.ParseInt(b.UITokenAmount.Amount, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid token amount %q: %w", b.UITokenAmount.Amount, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("negative token amount %q", b.UITokenAmount.Amount)
	}
	return n, nil
}

// GetTransaction fetches a transaction at the configured commitment and reduces
// it to token credits. Returns (nil, nil) when the transaction is not yet
// available at that commitment (e.g. seen as a signature but not finalized) — the
// caller should treat that as "keep waiting", not an error.
func (c *Client) GetTransaction(ctx context.Context, signature string) (*Transaction, error) {
	params := []any{signature, map[string]any{
		"encoding":                       "jsonParsed",
		"commitment":                     c.cfg.Commitment,
		"maxSupportedTransactionVersion": 0,
	}}
	var raw json.RawMessage
	if err := c.call(ctx, "getTransaction", params, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil // not yet at commitment
	}
	var tx struct {
		Slot        uint64 `json:"slot"`
		Transaction struct {
			Message struct {
				AccountKeys []struct {
					Pubkey string `json:"pubkey"`
				} `json:"accountKeys"`
			} `json:"message"`
		} `json:"transaction"`
		Meta *struct {
			Err               any            `json:"err"`
			PreTokenBalances  []tokenBalance `json:"preTokenBalances"`
			PostTokenBalances []tokenBalance `json:"postTokenBalances"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(raw, &tx); err != nil {
		return nil, fmt.Errorf("solana rpc getTransaction: decode tx: %w", err)
	}
	out := &Transaction{Signature: signature, Slot: tx.Slot}
	keys := tx.Transaction.Message.AccountKeys
	out.AccountKeys = make([]string, len(keys))
	for i, k := range keys {
		out.AccountKeys[i] = k.Pubkey
	}
	if tx.Meta == nil {
		return out, nil
	}
	out.Failed = tx.Meta.Err != nil

	pre := make(map[uint32]int64, len(tx.Meta.PreTokenBalances))
	for _, b := range tx.Meta.PreTokenBalances {
		amt, err := b.base()
		if err != nil {
			return nil, fmt.Errorf("solana rpc getTransaction: preTokenBalance: %w", err)
		}
		pre[b.AccountIndex] = amt
	}
	for _, b := range tx.Meta.PostTokenBalances {
		post, err := b.base()
		if err != nil {
			return nil, fmt.Errorf("solana rpc getTransaction: postTokenBalance: %w", err)
		}
		delta := post - pre[b.AccountIndex]
		if delta <= 0 {
			continue
		}
		acct := ""
		if int(b.AccountIndex) < len(keys) {
			acct = keys[b.AccountIndex].Pubkey
		}
		out.Credits = append(out.Credits, TokenCredit{Account: acct, Owner: b.Owner, Mint: b.Mint, Delta: delta})
	}
	return out, nil
}
