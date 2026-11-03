package indexer

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/ayushns01/agentic-bank/services/chain"
)

// fakeChain serves logs from memory and honours address and topic filters.
type fakeChain struct {
	head    uint64
	logs    []types.Log
	queries []ethereum.FilterQuery
}

func (f *fakeChain) BlockNumber(context.Context) (uint64, error) { return f.head, nil }

func (f *fakeChain) FilterLogs(_ context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	f.queries = append(f.queries, q)
	var out []types.Log
	for _, l := range f.logs {
		if l.BlockNumber < q.FromBlock.Uint64() || l.BlockNumber > q.ToBlock.Uint64() {
			continue
		}
		if !contains(q.Addresses, l.Address) || !containsHash(q.Topics[0], l.Topics[0]) {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}

func contains(xs []common.Address, x common.Address) bool {
	for _, y := range xs {
		if x == y {
			return true
		}
	}
	return false
}

func containsHash(xs []common.Hash, x common.Hash) bool {
	for _, y := range xs {
		if x == y {
			return true
		}
	}
	return false
}

type recSink struct {
	ranges [][2]uint64
	events []chain.Event
	fail   error
}

func (s *recSink) Handle(_ context.Context, from, to uint64, evs []chain.Event) error {
	if s.fail != nil {
		return s.fail
	}
	s.ranges = append(s.ranges, [2]uint64{from, to})
	s.events = append(s.events, evs...)
	return nil
}

var (
	factory = common.HexToAddress("0xF0")
	token   = common.HexToAddress("0x70")
	account = common.HexToAddress("0xA0")
	owner   = common.HexToAddress("0x0E")
	outside = common.HexToAddress("0x99")
)

func topic(a common.Address) common.Hash { return common.BytesToHash(a.Bytes()) }

func opened(block uint64, idx uint) types.Log {
	data, _ := chain.AccountFactoryABI.Events["AccountOpened"].Inputs.NonIndexed().Pack([32]byte{})
	return types.Log{Address: factory, BlockNumber: block, Index: idx, Data: data,
		Topics: []common.Hash{chain.TopicAccountOpened, topic(account), topic(owner)}}
}

func transfer(block uint64, idx uint, from, to common.Address, amount int64) types.Log {
	data, _ := chain.TestTokenABI.Events["Transfer"].Inputs.NonIndexed().Pack(big.NewInt(amount))
	return types.Log{Address: token, BlockNumber: block, Index: idx, Data: data,
		Topics: []common.Hash{chain.TopicTransfer, topic(from), topic(to)}}
}

func TestWaitsForConfirmations(t *testing.T) {
	src := &fakeChain{head: 10, logs: []types.Log{opened(9, 0)}}
	sink := &recSink{}
	ix := New(src, sink, Config{Factory: factory, Tokens: []common.Address{token}, Confirmations: 2})

	if ok, err := ix.Step(context.Background()); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if sink.ranges[0] != [2]uint64{0, 8} || len(sink.events) != 0 {
		t.Fatalf("processed %v, events %v; block 9 is not yet 2 deep", sink.ranges, sink.events)
	}
	if ok, _ := ix.Step(context.Background()); ok {
		t.Fatal("nothing new should be processed")
	}
	src.head = 11
	ix.Step(context.Background())
	if len(sink.events) != 1 {
		t.Fatalf("block 9 should be indexed at head 11: %v", sink.events)
	}
}

func TestAccountOpenedMidRangeIsTracked(t *testing.T) {
	// The account opens and receives a deposit in the same range; an outside
	// transfer of the same token is ignored.
	src := &fakeChain{head: 20, logs: []types.Log{
		opened(5, 0),
		transfer(5, 1, outside, account, 100),
		transfer(6, 0, outside, outside, 7),
	}}
	sink := &recSink{}
	ix := New(src, sink, Config{Factory: factory, Tokens: []common.Address{token}})
	ix.Step(context.Background())

	if len(sink.events) != 2 {
		t.Fatalf("events %#v", sink.events)
	}
	if _, ok := sink.events[0].(chain.AccountOpened); !ok {
		t.Fatal("events must be in chain order")
	}
	if tr := sink.events[1].(chain.Transfer); tr.To != account || tr.Amount.Int64() != 100 {
		t.Fatalf("%+v", tr)
	}
	if got := ix.Accounts(); len(got) != 1 || got[0] != account {
		t.Fatalf("accounts %v", got)
	}
}

func TestChunksLargeRanges(t *testing.T) {
	src := &fakeChain{head: 9}
	sink := &recSink{}
	ix := New(src, sink, Config{Factory: factory, MaxRange: 4})
	for {
		ok, err := ix.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
	}
	want := [][2]uint64{{0, 3}, {4, 7}, {8, 9}}
	if len(sink.ranges) != len(want) {
		t.Fatalf("ranges %v", sink.ranges)
	}
	for i := range want {
		if sink.ranges[i] != want[i] {
			t.Fatalf("ranges %v want %v", sink.ranges, want)
		}
	}
	if ix.Next() != 10 {
		t.Fatalf("next %d", ix.Next())
	}
}

func TestCursorHoldsOnSinkError(t *testing.T) {
	src := &fakeChain{head: 5, logs: []types.Log{opened(3, 0)}}
	sink := &recSink{fail: errors.New("db down")}
	ix := New(src, sink, Config{Factory: factory})

	if _, err := ix.Step(context.Background()); err == nil {
		t.Fatal("want sink error")
	}
	if ix.Next() != 0 || len(ix.Accounts()) != 0 {
		t.Fatal("a failed range must not advance the cursor or register accounts")
	}
	sink.fail = nil
	ix.Step(context.Background())
	if ix.Next() != 6 || len(sink.events) != 1 {
		t.Fatalf("retry: next %d events %d", ix.Next(), len(sink.events))
	}
}

func TestStartBlockAndKnownAccounts(t *testing.T) {
	src := &fakeChain{head: 100, logs: []types.Log{
		transfer(10, 0, outside, account, 1), // before start: skipped
		transfer(60, 0, account, outside, 2),
	}}
	sink := &recSink{}
	ix := New(src, sink, Config{Factory: factory, Tokens: []common.Address{token},
		Accounts: []common.Address{account}, StartBlock: 50})
	ix.Step(context.Background())
	if len(sink.events) != 1 || sink.events[0].(chain.Transfer).Amount.Int64() != 2 {
		t.Fatalf("events %v", sink.events)
	}
}
