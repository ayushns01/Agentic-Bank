// Command bankd runs the agentic bank: the chain indexer, the payment
// orchestrator and the REST API, against one EVM chain.
//
// Configuration is by environment variable:
//
//	BANK_RPC_URL        JSON-RPC endpoint (e.g. http://127.0.0.1:8545)
//	BANK_ENGINE         PolicyEngine address
//	BANK_FACTORY        AccountFactory address
//	BANK_ESCROW         Escrow address (optional)
//	BANK_TOKENS         comma-separated supported token addresses
//	BANK_RELAYER_KEY    hex private key that pays gas for relayed intents
//	BANK_START_BLOCK    first block to index (default 0)
//	BANK_CONFIRMATIONS  blocks to wait before booking (default 2)
//	BANK_LISTEN         HTTP listen address (default :8080)
//	BANK_TIMEOUT        broadcast timeout before a payment is Held (default 5m)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/ayushns01/agentic-bank/services/api"
	"github.com/ayushns01/agentic-bank/services/bank"
	"github.com/ayushns01/agentic-bank/services/chain"
	"github.com/ayushns01/agentic-bank/services/indexer"
	"github.com/ayushns01/agentic-bank/services/ledger"
	"github.com/ayushns01/agentic-bank/services/orchestrator"
)

type config struct {
	rpcURL        string
	engine        common.Address
	factory       common.Address
	escrow        common.Address
	tokens        []common.Address
	relayerKey    string
	startBlock    uint64
	confirmations uint64
	listen        string
	timeout       time.Duration
}

func loadConfig() (config, error) {
	c := config{
		rpcURL:        os.Getenv("BANK_RPC_URL"),
		relayerKey:    strings.TrimPrefix(os.Getenv("BANK_RELAYER_KEY"), "0x"),
		listen:        envOr("BANK_LISTEN", ":8080"),
		confirmations: 2,
		timeout:       5 * time.Minute,
	}
	var err error
	if c.rpcURL == "" || c.relayerKey == "" {
		return c, errors.New("BANK_RPC_URL and BANK_RELAYER_KEY are required")
	}
	if c.engine, err = mustAddr("BANK_ENGINE"); err != nil {
		return c, err
	}
	if c.factory, err = mustAddr("BANK_FACTORY"); err != nil {
		return c, err
	}
	if v := os.Getenv("BANK_ESCROW"); v != "" {
		if c.escrow, err = parseAddr("BANK_ESCROW", v); err != nil {
			return c, err
		}
	}
	for _, t := range strings.Split(os.Getenv("BANK_TOKENS"), ",") {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		a, err := parseAddr("BANK_TOKENS", t)
		if err != nil {
			return c, err
		}
		c.tokens = append(c.tokens, a)
	}
	if len(c.tokens) == 0 {
		return c, errors.New("BANK_TOKENS needs at least one token")
	}
	if v := os.Getenv("BANK_START_BLOCK"); v != "" {
		if c.startBlock, err = strconv.ParseUint(v, 10, 64); err != nil {
			return c, fmt.Errorf("BANK_START_BLOCK: %w", err)
		}
	}
	if v := os.Getenv("BANK_CONFIRMATIONS"); v != "" {
		if c.confirmations, err = strconv.ParseUint(v, 10, 64); err != nil {
			return c, fmt.Errorf("BANK_CONFIRMATIONS: %w", err)
		}
	}
	if v := os.Getenv("BANK_TIMEOUT"); v != "" {
		if c.timeout, err = time.ParseDuration(v); err != nil {
			return c, fmt.Errorf("BANK_TIMEOUT: %w", err)
		}
	}
	return c, nil
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("bankd stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := ethclient.DialContext(ctx, cfg.rpcURL)
	if err != nil {
		return fmt.Errorf("dial %s: %w", cfg.rpcURL, err)
	}
	chainID, err := client.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("chain id: %w", err)
	}
	key, err := crypto.HexToECDSA(cfg.relayerKey)
	if err != nil {
		return fmt.Errorf("BANK_RELAYER_KEY: %w", err)
	}
	opts, err := bind.NewKeyedTransactorWithChainID(key, chainID)
	if err != nil {
		return err
	}

	b := bank.New(ledger.New(), chain.NewBank(client, cfg.engine), chain.NewRelayer(client, opts),
		cfg.tokens, orchestrator.Config{})
	ix := indexer.New(client, b, indexer.Config{
		Factory: cfg.factory, Escrow: cfg.escrow, Tokens: cfg.tokens,
		StartBlock: cfg.startBlock, Confirmations: cfg.confirmations,
	})

	go ix.Run(ctx, 2*time.Second, func(err error) { log.Warn("indexer", "err", err) })
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				for _, id := range b.Orchestrator().ExpireBroadcasts(cfg.timeout) {
					log.Warn("payment held: confirmation timeout", "payment", id)
				}
			}
		}
	}()

	srv := &http.Server{Addr: cfg.listen, Handler: api.New(b), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("bankd listening", "addr", cfg.listen, "chain", chainID, "relayer", opts.From.Hex(),
		"engine", cfg.engine.Hex(), "factory", cfg.factory.Hex())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func mustAddr(k string) (common.Address, error) { return parseAddr(k, os.Getenv(k)) }

func parseAddr(k, v string) (common.Address, error) {
	if !common.IsHexAddress(v) {
		return common.Address{}, fmt.Errorf("%s: %q is not an address", k, v)
	}
	return common.HexToAddress(v), nil
}
