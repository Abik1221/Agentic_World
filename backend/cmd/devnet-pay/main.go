// Command devnet-pay pays a Solana Pay transfer request from the terminal — a
// stand-in for a phone wallet (Phantom) when testing the deposit pipeline on
// devnet. It parses the `pay_url` returned by POST /v1/deposits, builds a USDC
// transferChecked from the payer's ATA to the platform's ATA, and — crucially —
// attaches the Solana Pay `reference` pubkey as a read-only account so the
// backend's getSignaturesForAddress(reference) poller detects the deposit.
//
// Usage:
//
//	go run ./cmd/devnet-pay \
//	  -payer ~/devnet-payer.json \                 # CLI keygen file OR base58 secret
//	  -rpc https://api.devnet.solana.com \
//	  -url 'solana:<owner>?amount=1&spl-token=<mint>&reference=<ref>'
//
// The payer must already hold devnet USDC (Circle faucet) and a little SOL for
// fees. Prints the transaction signature + an explorer link on success.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/url"
	"os"
	"strings"
	"time"

	solana "github.com/gagliardetto/solana-go"
	ataprog "github.com/gagliardetto/solana-go/programs/associated-token-account"
	token "github.com/gagliardetto/solana-go/programs/token"
	"github.com/gagliardetto/solana-go/rpc"
)

func main() {
	var (
		payerFlag = flag.String("payer", "", "payer keypair: path to a solana-keygen JSON file OR a base58 secret key")
		rpcURL    = flag.String("rpc", "https://api.devnet.solana.com", "Solana RPC endpoint")
		payURL    = flag.String("url", "", "Solana Pay URL from POST /v1/deposits (pay_url)")
		decimals  = flag.Uint("decimals", 6, "USDC token decimals")
		ataOwner  = flag.String("ata-owner", "", "derive-ATA mode: print the associated token account for this wallet + -ata-mint, then exit")
		ataMint   = flag.String("ata-mint", "", "mint used with -ata-owner")
	)
	flag.Parse()

	// Derive-ATA mode: compute the deterministic USDC token account for a wallet
	// (this is your SOLANA_PLATFORM_ATA) without needing spl-token-cli.
	if *ataOwner != "" {
		owner, err := solana.PublicKeyFromBase58(*ataOwner)
		if err != nil {
			log.Fatalf("bad -ata-owner: %v", err)
		}
		mint, err := solana.PublicKeyFromBase58(*ataMint)
		if err != nil {
			log.Fatalf("bad -ata-mint: %v", err)
		}
		ata, _, err := solana.FindAssociatedTokenAddress(owner, mint)
		if err != nil {
			log.Fatalf("derive ATA: %v", err)
		}
		fmt.Println(ata)
		return
	}

	if *payerFlag == "" || *payURL == "" {
		flag.Usage()
		log.Fatal("both -payer and -url are required (or use -ata-owner/-ata-mint to derive an ATA)")
	}

	payer, err := loadPayer(*payerFlag)
	if err != nil {
		log.Fatalf("load payer: %v", err)
	}
	recipient, mint, reference, amount, err := parsePayURL(*payURL, uint8(*decimals))
	if err != nil {
		log.Fatalf("parse pay url: %v", err)
	}

	payerPub := payer.PublicKey()
	srcATA, _, err := solana.FindAssociatedTokenAddress(payerPub, mint)
	if err != nil {
		log.Fatalf("derive payer ATA: %v", err)
	}
	destATA, _, err := solana.FindAssociatedTokenAddress(recipient, mint)
	if err != nil {
		log.Fatalf("derive recipient ATA: %v", err)
	}

	fmt.Printf("payer      : %s\n", payerPub)
	fmt.Printf("payer ATA  : %s\n", srcATA)
	fmt.Printf("recipient  : %s\n", recipient)
	fmt.Printf("dest ATA   : %s\n", destATA)
	fmt.Printf("mint       : %s\n", mint)
	fmt.Printf("reference  : %s\n", reference)
	fmt.Printf("amount     : %d base units (%d decimals)\n\n", amount, *decimals)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := rpc.New(*rpcURL)

	// Use a finalized blockhash: the public devnet RPC is load-balanced, and a
	// "confirmed" blockhash isn't always visible on the node that runs preflight
	// simulation (=> spurious BlockhashNotFound). Finalized is known to all nodes.
	recent, err := client.GetLatestBlockhash(ctx, rpc.CommitmentFinalized)
	if err != nil {
		log.Fatalf("latest blockhash: %v", err)
	}

	// create-idempotent the recipient ATA (harmless if it already exists), then
	// the checked USDC transfer with the reference appended as a read-only key.
	createATA := ataprog.NewCreateIdempotentInstruction(payerPub, recipient, mint).Build()
	base := token.NewTransferCheckedInstruction(amount, uint8(*decimals), srcATA, mint, destATA, payerPub, nil).Build()
	data, err := base.Data()
	if err != nil {
		log.Fatalf("encode transfer data: %v", err)
	}
	metas := append(base.Accounts(), &solana.AccountMeta{PublicKey: reference, IsSigner: false, IsWritable: false})
	transfer := solana.NewInstruction(token.ProgramID, metas, data)

	tx, err := solana.NewTransaction(
		[]solana.Instruction{createATA, transfer},
		recent.Value.Blockhash,
		solana.TransactionPayer(payerPub),
	)
	if err != nil {
		log.Fatalf("build tx: %v", err)
	}
	if _, err := tx.Sign(func(k solana.PublicKey) *solana.PrivateKey {
		if k.Equals(payerPub) {
			return &payer
		}
		return nil
	}); err != nil {
		log.Fatalf("sign tx: %v", err)
	}

	sig, err := client.SendTransaction(ctx, tx)
	if err != nil {
		log.Fatalf("broadcast: %v", err)
	}
	fmt.Printf("SENT ✔  signature: %s\n", sig)
	fmt.Printf("explorer: https://explorer.solana.com/tx/%s?cluster=devnet\n", sig)
	fmt.Println("\nThe backend poller should flip the deposit to confirmed within a poll cycle.")
}

// loadPayer accepts a solana-keygen JSON keyfile path or a raw base58 secret.
func loadPayer(v string) (solana.PrivateKey, error) {
	if fi, err := os.Stat(v); err == nil && !fi.IsDir() {
		return solana.PrivateKeyFromSolanaKeygenFile(v)
	}
	return solana.PrivateKeyFromBase58(v)
}

// parsePayURL extracts recipient, mint, reference and base-unit amount from a
// Solana Pay transfer URL: solana:<recipient>?amount=<usdc>&spl-token=<mint>&reference=<ref>
func parsePayURL(raw string, decimals uint8) (recipient, mint, reference solana.PublicKey, amount uint64, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return recipient, mint, reference, 0, err
	}
	if u.Scheme != "solana" {
		return recipient, mint, reference, 0, fmt.Errorf("not a solana: url (%q)", u.Scheme)
	}
	recipient, err = solana.PublicKeyFromBase58(strings.TrimSpace(u.Opaque))
	if err != nil {
		return recipient, mint, reference, 0, fmt.Errorf("bad recipient: %w", err)
	}
	q := u.Query()
	if mint, err = solana.PublicKeyFromBase58(q.Get("spl-token")); err != nil {
		return recipient, mint, reference, 0, fmt.Errorf("bad spl-token: %w", err)
	}
	if reference, err = solana.PublicKeyFromBase58(q.Get("reference")); err != nil {
		return recipient, mint, reference, 0, fmt.Errorf("bad reference: %w", err)
	}
	amount, err = usdcToBase(q.Get("amount"), decimals)
	if err != nil {
		return recipient, mint, reference, 0, err
	}
	return recipient, mint, reference, amount, nil
}

// usdcToBase converts a decimal USDC amount string to integer base units without
// float rounding (e.g. "1.5", decimals=6 -> 1500000).
func usdcToBase(s string, decimals uint8) (uint64, error) {
	if s == "" {
		return 0, fmt.Errorf("amount missing from pay url")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return 0, fmt.Errorf("bad amount %q", s)
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	r.Mul(r, new(big.Rat).SetInt(scale))
	if !r.IsInt() {
		return 0, fmt.Errorf("amount %q has more precision than %d decimals", s, decimals)
	}
	return r.Num().Uint64(), nil
}
