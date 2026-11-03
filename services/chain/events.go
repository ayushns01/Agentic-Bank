package chain

import (
	"fmt"
	"math/big"
	"reflect"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Meta locates an event on chain. (TxHash, LogIndex) is unique and is used
// as the idempotency key for ledger postings.
type Meta struct {
	Block    uint64
	TxHash   common.Hash
	LogIndex uint
	Address  common.Address // emitting contract
}

// ID returns a stable identifier for the event.
func (m Meta) ID() string { return fmt.Sprintf("%s:%d", m.TxHash.Hex(), m.LogIndex) }

// Event is any decoded bank event.
type Event interface{ EventMeta() Meta }

type AccountOpened struct {
	Meta
	Account common.Address
	Owner   common.Address
	Salt    [32]byte
}

type PaymentExecuted struct {
	Meta     // Address is the AgentAccount
	Node     [32]byte
	Key      common.Address
	Token    common.Address
	To       common.Address
	Amount   *big.Int
	Ref      [32]byte
	Nonce    *big.Int
	CoSigned bool
}

type CallExecuted struct {
	Meta     // Address is the AgentAccount
	Node     [32]byte
	Key      common.Address
	Target   common.Address
	Selector [4]byte
	Nonce    *big.Int
	CoSigned bool
}

type Transfer struct {
	Meta   // Address is the token
	From   common.Address
	To     common.Address
	Amount *big.Int
}

type EscrowOpened struct {
	Meta
	ID      *big.Int `abi:"id"`
	Payer   common.Address
	Payee   common.Address
	Token   common.Address
	Amount  *big.Int
	Expiry  uint64
	Arbiter common.Address
	Ref     [32]byte
}

type EscrowCaptured struct {
	Meta
	ID        *big.Int `abi:"id"`
	Amount    *big.Int
	Remaining *big.Int
}

type EscrowRefunded struct {
	Meta
	ID     *big.Int `abi:"id"`
	Amount *big.Int
}

type EscrowResolved struct {
	Meta
	ID      *big.Int `abi:"id"`
	ToPayee *big.Int
	ToPayer *big.Int
}

func (e AccountOpened) EventMeta() Meta   { return e.Meta }
func (e PaymentExecuted) EventMeta() Meta { return e.Meta }
func (e CallExecuted) EventMeta() Meta    { return e.Meta }
func (e Transfer) EventMeta() Meta        { return e.Meta }
func (e EscrowOpened) EventMeta() Meta    { return e.Meta }
func (e EscrowCaptured) EventMeta() Meta  { return e.Meta }
func (e EscrowRefunded) EventMeta() Meta  { return e.Meta }
func (e EscrowResolved) EventMeta() Meta  { return e.Meta }

// Topic IDs of every event the bank indexes.
var (
	TopicAccountOpened   = AccountFactoryABI.Events["AccountOpened"].ID
	TopicPaymentExecuted = AgentAccountABI.Events["PaymentExecuted"].ID
	TopicCallExecuted    = AgentAccountABI.Events["CallExecuted"].ID
	TopicTransfer        = TestTokenABI.Events["Transfer"].ID
	TopicEscrowOpened    = EscrowABI.Events["Opened"].ID
	TopicEscrowCaptured  = EscrowABI.Events["Captured"].ID
	TopicEscrowRefunded  = EscrowABI.Events["Refunded"].ID
	TopicEscrowResolved  = EscrowABI.Events["Resolved"].ID
)

// AllTopics is the topic-0 filter for the indexer.
var AllTopics = []common.Hash{
	TopicAccountOpened, TopicPaymentExecuted, TopicCallExecuted, TopicTransfer,
	TopicEscrowOpened, TopicEscrowCaptured, TopicEscrowRefunded, TopicEscrowResolved,
}

// Decode turns a raw log into a typed event. ok is false for logs the bank
// does not index (unknown topic, or an ERC-721 Transfer with 4 topics).
func Decode(l types.Log) (ev Event, ok bool, err error) {
	if len(l.Topics) == 0 || l.Removed {
		return nil, false, nil
	}
	m := Meta{Block: l.BlockNumber, TxHash: l.TxHash, LogIndex: l.Index, Address: l.Address}
	switch l.Topics[0] {
	case TopicAccountOpened:
		var e AccountOpened
		e.Meta = m
		err = unpack(AccountFactoryABI, "AccountOpened", l, &e)
		return e, err == nil, err
	case TopicPaymentExecuted:
		var e PaymentExecuted
		e.Meta = m
		err = unpack(AgentAccountABI, "PaymentExecuted", l, &e)
		return e, err == nil, err
	case TopicCallExecuted:
		var e CallExecuted
		e.Meta = m
		err = unpack(AgentAccountABI, "CallExecuted", l, &e)
		return e, err == nil, err
	case TopicTransfer:
		if len(l.Topics) != 3 { // ERC-721 Transfer indexes the token id too
			return nil, false, nil
		}
		var e Transfer
		e.Meta = m
		err = unpack(TestTokenABI, "Transfer", l, &e)
		return e, err == nil, err
	case TopicEscrowOpened:
		var e EscrowOpened
		e.Meta = m
		err = unpack(EscrowABI, "Opened", l, &e)
		return e, err == nil, err
	case TopicEscrowCaptured:
		var e EscrowCaptured
		e.Meta = m
		err = unpack(EscrowABI, "Captured", l, &e)
		return e, err == nil, err
	case TopicEscrowRefunded:
		var e EscrowRefunded
		e.Meta = m
		err = unpack(EscrowABI, "Refunded", l, &e)
		return e, err == nil, err
	case TopicEscrowResolved:
		var e EscrowResolved
		e.Meta = m
		err = unpack(EscrowABI, "Resolved", l, &e)
		return e, err == nil, err
	}
	return nil, false, nil
}

// unpack decodes data and indexed topics into out, matching ABI argument
// names to struct fields (go-ethereum's capitalisation rules).
func unpack(a abi.ABI, name string, l types.Log, out any) error {
	ev := a.Events[name]
	if len(l.Data) > 0 {
		vals := map[string]any{}
		if err := a.UnpackIntoMap(vals, name, l.Data); err != nil {
			return fmt.Errorf("chain: decode %s data: %w", name, err)
		}
		v := reflect.ValueOf(out).Elem()
		for k, val := range vals {
			f := fieldFor(v, k)
			if !f.IsValid() {
				return fmt.Errorf("chain: decode %s: no field for %q", name, k)
			}
			rv := reflect.ValueOf(val)
			if !rv.Type().AssignableTo(f.Type()) {
				return fmt.Errorf("chain: decode %s: %q is %s, field is %s", name, k, rv.Type(), f.Type())
			}
			f.Set(rv)
		}
	}
	var indexed abi.Arguments
	for _, arg := range ev.Inputs {
		if arg.Indexed {
			indexed = append(indexed, arg)
		}
	}
	if len(l.Topics)-1 != len(indexed) {
		return fmt.Errorf("chain: %s has %d indexed topics, want %d", name, len(l.Topics)-1, len(indexed))
	}
	for i, arg := range indexed {
		if err := setTopic(out, arg, l.Topics[i+1]); err != nil {
			return fmt.Errorf("chain: decode %s topic %s: %w", name, arg.Name, err)
		}
	}
	return nil
}

// setTopic writes one indexed argument into the struct field named by its
// `abi` tag, or by the capitalised argument name.
func setTopic(out any, arg abi.Argument, topic common.Hash) error {
	v := reflect.ValueOf(out).Elem()
	f := fieldFor(v, arg.Name)
	if !f.IsValid() {
		return fmt.Errorf("no field for %q", arg.Name)
	}
	switch arg.Type.T {
	case abi.AddressTy:
		f.Set(reflect.ValueOf(common.BytesToAddress(topic[:])))
	case abi.FixedBytesTy:
		if arg.Type.Size != 32 {
			return fmt.Errorf("unsupported indexed bytes%d", arg.Type.Size)
		}
		f.Set(reflect.ValueOf([32]byte(topic)))
	case abi.UintTy, abi.IntTy:
		f.Set(reflect.ValueOf(new(big.Int).SetBytes(topic[:])))
	case abi.BoolTy:
		f.SetBool(topic[31] == 1)
	default:
		return fmt.Errorf("unsupported indexed type %s", arg.Type)
	}
	return nil
}

func fieldFor(v reflect.Value, name string) reflect.Value {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Tag.Get("abi") == name {
			return v.Field(i)
		}
	}
	return v.FieldByName(abi.ToCamelCase(name))
}
