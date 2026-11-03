// Package indexer follows the chain and feeds decoded bank events to a sink.
//
// It reads only blocks at least Confirmations deep, so ordinary reorgs never
// reach the ledger. Delivery is at-least-once: the cursor advances only after
// the sink accepts a range, so sinks must be idempotent (the bank keys ledger
// entries by tx hash and log index).
package indexer

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/ayushns01/agentic-bank/services/chain"
)

// LogSource is the subset of an RPC client the indexer needs.
type LogSource interface {
	BlockNumber(ctx context.Context) (uint64, error)
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error)
}

// Sink consumes the events of one block range, in chain order.
type Sink interface {
	Handle(ctx context.Context, from, to uint64, events []chain.Event) error
}

type Config struct {
	Factory common.Address
	Escrow  common.Address // zero if not deployed
	Tokens  []common.Address
	// Accounts already known (e.g. restored from storage).
	Accounts      []common.Address
	StartBlock    uint64
	Confirmations uint64
	MaxRange      uint64 // blocks per FilterLogs call; default 2000
}

type Indexer struct {
	src  LogSource
	sink Sink
	cfg  Config

	mu       sync.Mutex
	next     uint64 // next block to process
	accounts map[common.Address]bool
}

func New(src LogSource, sink Sink, cfg Config) *Indexer {
	if cfg.MaxRange == 0 {
		cfg.MaxRange = 2000
	}
	ix := &Indexer{src: src, sink: sink, cfg: cfg, next: cfg.StartBlock, accounts: map[common.Address]bool{}}
	for _, a := range cfg.Accounts {
		ix.accounts[a] = true
	}
	return ix
}

// Next returns the next block the indexer will process.
func (ix *Indexer) Next() uint64 {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.next
}

// Accounts returns the AgentAccounts seen so far.
func (ix *Indexer) Accounts() []common.Address {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	out := make([]common.Address, 0, len(ix.accounts))
	for a := range ix.accounts {
		out = append(out, a)
	}
	return out
}

// Step processes at most one range of confirmed blocks. It returns false
// when there was nothing new to process.
func (ix *Indexer) Step(ctx context.Context) (bool, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	head, err := ix.src.BlockNumber(ctx)
	if err != nil {
		return false, fmt.Errorf("indexer: head: %w", err)
	}
	if head < ix.cfg.Confirmations {
		return false, nil
	}
	safe := head - ix.cfg.Confirmations
	from := ix.next
	if from > safe {
		return false, nil
	}
	to := min(safe, from+ix.cfg.MaxRange-1)

	// Pass 1: accounts opened in this range, so pass 2 can include them even
	// if they transact in the same range.
	opened, err := ix.filter(ctx, from, to, []common.Address{ix.cfg.Factory}, []common.Hash{chain.TopicAccountOpened})
	if err != nil {
		return false, err
	}
	newAccounts := map[common.Address]bool{}
	for _, l := range opened {
		if ev, ok, err := chain.Decode(l); err == nil && ok {
			newAccounts[ev.(chain.AccountOpened).Account] = true
		}
	}
	known := func(a common.Address) bool { return ix.accounts[a] || newAccounts[a] }

	// Pass 2: everything else, from tokens, the escrow and known accounts.
	addrs := append([]common.Address{}, ix.cfg.Tokens...)
	if ix.cfg.Escrow != (common.Address{}) {
		addrs = append(addrs, ix.cfg.Escrow)
	}
	for a := range ix.accounts {
		addrs = append(addrs, a)
	}
	for a := range newAccounts {
		addrs = append(addrs, a)
	}
	rest, err := ix.filter(ctx, from, to, addrs, chain.AllTopics[1:])
	if err != nil {
		return false, err
	}

	logs := append(opened, rest...)
	sort.Slice(logs, func(i, j int) bool {
		if logs[i].BlockNumber != logs[j].BlockNumber {
			return logs[i].BlockNumber < logs[j].BlockNumber
		}
		return logs[i].Index < logs[j].Index
	})

	var events []chain.Event
	for _, l := range logs {
		ev, ok, err := chain.Decode(l)
		if err != nil {
			return false, fmt.Errorf("indexer: block %d log %d: %w", l.BlockNumber, l.Index, err)
		}
		if !ok || !ix.relevant(ev, known) {
			continue
		}
		events = append(events, ev)
	}

	if err := ix.sink.Handle(ctx, from, to, events); err != nil {
		return false, fmt.Errorf("indexer: sink [%d,%d]: %w", from, to, err)
	}
	for a := range newAccounts {
		ix.accounts[a] = true
	}
	ix.next = to + 1
	return true, nil
}

// relevant drops events that don't involve the bank, e.g. token transfers
// between two outside addresses.
func (ix *Indexer) relevant(ev chain.Event, known func(common.Address) bool) bool {
	switch e := ev.(type) {
	case chain.AccountOpened:
		return e.Address == ix.cfg.Factory
	case chain.PaymentExecuted, chain.CallExecuted:
		return known(e.EventMeta().Address)
	case chain.Transfer:
		return known(e.From) || known(e.To)
	default: // escrow events
		return e.EventMeta().Address == ix.cfg.Escrow
	}
}

func (ix *Indexer) filter(ctx context.Context, from, to uint64, addrs []common.Address, topics []common.Hash) ([]types.Log, error) {
	if len(addrs) == 0 {
		return nil, nil
	}
	q := ethereum.FilterQuery{
		FromBlock: new(big.Int).SetUint64(from),
		ToBlock:   new(big.Int).SetUint64(to),
		Addresses: addrs,
		Topics:    [][]common.Hash{topics},
	}
	logs, err := ix.src.FilterLogs(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("indexer: logs [%d,%d]: %w", from, to, err)
	}
	return logs, nil
}

// Run steps until ctx is cancelled, catching up as fast as possible and then
// polling every interval. Errors are reported to onErr and retried.
func (ix *Indexer) Run(ctx context.Context, interval time.Duration, onErr func(error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		for {
			progressed, err := ix.Step(ctx)
			if err != nil && onErr != nil {
				onErr(err)
			}
			if !progressed || err != nil || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
