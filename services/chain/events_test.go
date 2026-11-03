package chain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func mkLog(t *testing.T, a abi.ABI, name string, emitter common.Address, topics []common.Hash, data ...any) types.Log {
	t.Helper()
	packed, err := a.Events[name].Inputs.NonIndexed().Pack(data...)
	if err != nil {
		t.Fatalf("pack %s: %v", name, err)
	}
	return types.Log{
		Address:     emitter,
		Topics:      append([]common.Hash{a.Events[name].ID}, topics...),
		Data:        packed,
		BlockNumber: 7,
		TxHash:      common.HexToHash("0xabc"),
		Index:       3,
	}
}

var (
	acct  = common.HexToAddress("0x1000000000000000000000000000000000000001")
	owner = common.HexToAddress("0x2000000000000000000000000000000000000002")
	token = common.HexToAddress("0x3000000000000000000000000000000000000003")
	to    = common.HexToAddress("0x4000000000000000000000000000000000000004")
	key   = common.HexToAddress("0x5000000000000000000000000000000000000005")
)

func addrTopic(a common.Address) common.Hash { return common.BytesToHash(a.Bytes()) }

func TestDecodePaymentExecuted(t *testing.T) {
	node := common.HexToHash("0x0a")
	ref := [32]byte{1, 2, 3}
	l := mkLog(t, AgentAccountABI, "PaymentExecuted", acct,
		[]common.Hash{node, addrTopic(key), addrTopic(token)},
		to, big.NewInt(25_000_000), ref, big.NewInt(4), true)
	ev, ok, err := Decode(l)
	if err != nil || !ok {
		t.Fatalf("decode: ok=%v err=%v", ok, err)
	}
	p := ev.(PaymentExecuted)
	if p.Address != acct || p.Node != [32]byte(node) || p.Key != key || p.Token != token || p.To != to {
		t.Fatalf("addresses: %+v", p)
	}
	if p.Amount.Int64() != 25_000_000 || p.Nonce.Int64() != 4 || !p.CoSigned || p.Ref != ref {
		t.Fatalf("values: %+v", p)
	}
	if p.ID() != common.HexToHash("0xabc").Hex()+":3" {
		t.Fatalf("id %s", p.ID())
	}
}

func TestDecodeTransferAndAccountOpened(t *testing.T) {
	l := mkLog(t, TestTokenABI, "Transfer", token, []common.Hash{addrTopic(acct), addrTopic(to)}, big.NewInt(9))
	ev, ok, err := Decode(l)
	if err != nil || !ok {
		t.Fatal(err)
	}
	tr := ev.(Transfer)
	if tr.From != acct || tr.To != to || tr.Amount.Int64() != 9 || tr.Address != token {
		t.Fatalf("%+v", tr)
	}

	l = mkLog(t, AccountFactoryABI, "AccountOpened", owner, []common.Hash{addrTopic(acct), addrTopic(owner)}, [32]byte{'m'})
	ev, ok, err = Decode(l)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if o := ev.(AccountOpened); o.Account != acct || o.Owner != owner || o.Salt[0] != 'm' {
		t.Fatalf("%+v", o)
	}
}

func TestDecodeEscrowAndCall(t *testing.T) {
	id := common.BigToHash(big.NewInt(42))
	l := mkLog(t, EscrowABI, "Opened", to, []common.Hash{id, addrTopic(acct), addrTopic(owner)},
		token, big.NewInt(100), uint64(1_900_000_000), common.Address{}, [32]byte{9})
	ev, ok, err := Decode(l)
	if err != nil || !ok {
		t.Fatal(err)
	}
	o := ev.(EscrowOpened)
	if o.ID.Int64() != 42 || o.Payer != acct || o.Payee != owner || o.Amount.Int64() != 100 || o.Expiry != 1_900_000_000 {
		t.Fatalf("%+v", o)
	}

	l = mkLog(t, EscrowABI, "Resolved", to, []common.Hash{id}, big.NewInt(60), big.NewInt(40))
	ev, _, err = Decode(l)
	if err != nil {
		t.Fatal(err)
	}
	if r := ev.(EscrowResolved); r.ID.Int64() != 42 || r.ToPayee.Int64() != 60 || r.ToPayer.Int64() != 40 {
		t.Fatalf("%+v", r)
	}

	l = mkLog(t, AgentAccountABI, "CallExecuted", acct,
		[]common.Hash{common.HexToHash("0x0b"), addrTopic(key), addrTopic(to)}, [4]byte{0x09, 0x5e, 0xa7, 0xb3}, big.NewInt(1), false)
	ev, _, err = Decode(l)
	if err != nil {
		t.Fatal(err)
	}
	if c := ev.(CallExecuted); c.Target != to || c.Selector != [4]byte{0x09, 0x5e, 0xa7, 0xb3} || c.CoSigned {
		t.Fatalf("%+v", c)
	}
}

func TestDecodeIgnoresUnknownAndRemoved(t *testing.T) {
	if _, ok, err := Decode(types.Log{Topics: []common.Hash{common.HexToHash("0x01")}}); ok || err != nil {
		t.Fatal("unknown topic should be skipped")
	}
	l := mkLog(t, TestTokenABI, "Transfer", token, []common.Hash{addrTopic(acct), addrTopic(to)}, big.NewInt(9))
	l.Removed = true
	if _, ok, _ := Decode(l); ok {
		t.Fatal("removed (reorged) log should be skipped")
	}
	// ERC-721 Transfer: same topic, 4 topics, no data.
	nft := types.Log{Topics: []common.Hash{TopicTransfer, addrTopic(acct), addrTopic(to), common.BigToHash(big.NewInt(1))}}
	if _, ok, _ := Decode(nft); ok {
		t.Fatal("ERC-721 transfer should be skipped")
	}
}
