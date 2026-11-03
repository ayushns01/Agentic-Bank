package chain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// PaymentIntent mirrors AgentAccount.PaymentIntent (field names match the
// ABI tuple so it packs directly).
type PaymentIntent struct {
	Token    common.Address
	To       common.Address
	Amount   *big.Int
	Ref      [32]byte
	Nonce    *big.Int
	Deadline uint64
}

// SessionKey mirrors AgentAccount.sessionKeys(key).
type SessionKey struct {
	Node       [32]byte
	ValidUntil uint64
	Active     bool
}

// Bank reads bank contracts. Read-only: safe to share.
type Bank struct {
	backend bind.ContractBackend
	Engine  common.Address
}

func NewBank(backend bind.ContractBackend, engine common.Address) *Bank {
	return &Bank{backend: backend, Engine: engine}
}

func (b *Bank) account(addr common.Address) *bind.BoundContract {
	return bind.NewBoundContract(addr, AgentAccountABI, b.backend, b.backend, b.backend)
}

func (b *Bank) call(ctx context.Context, c *bind.BoundContract, method string, args ...any) ([]any, error) {
	var out []any
	err := c.Call(&bind.CallOpts{Context: ctx}, &out, method, args...)
	return out, err
}

// IntentDigest returns the EIP-712 digest the account expects signatures over.
func (b *Bank) IntentDigest(ctx context.Context, account common.Address, i PaymentIntent) ([32]byte, error) {
	out, err := b.call(ctx, b.account(account), "intentDigest", i)
	if err != nil {
		return [32]byte{}, err
	}
	return out[0].([32]byte), nil
}

func (b *Bank) SessionKey(ctx context.Context, account, key common.Address) (SessionKey, error) {
	out, err := b.call(ctx, b.account(account), "sessionKeys", key)
	if err != nil {
		return SessionKey{}, err
	}
	return SessionKey{Node: out[0].([32]byte), ValidUntil: out[1].(uint64), Active: out[2].(bool)}, nil
}

func (b *Bank) Nonce(ctx context.Context, account, key common.Address) (*big.Int, error) {
	out, err := b.call(ctx, b.account(account), "nonces", key)
	if err != nil {
		return nil, err
	}
	return out[0].(*big.Int), nil
}

func (b *Bank) Owner(ctx context.Context, account common.Address) (common.Address, error) {
	out, err := b.call(ctx, b.account(account), "owner")
	if err != nil {
		return common.Address{}, err
	}
	return out[0].(common.Address), nil
}

// PolicyError is a decoded PolicyEngine revert, e.g. PeriodLimit.
type PolicyError struct {
	Name string
	Args []any
}

func (e *PolicyError) Error() string {
	parts := make([]string, len(e.Args))
	for i, a := range e.Args {
		switch v := a.(type) {
		case [32]byte:
			parts[i] = common.Hash(v).Hex()
		case common.Address:
			parts[i] = v.Hex()
		default:
			parts[i] = fmt.Sprint(v)
		}
	}
	return fmt.Sprintf("policy: %s(%s)", e.Name, strings.Join(parts, ", "))
}

// WouldAuthorize dry-runs the on-chain policy. A nil error means the spend
// would pass; otherwise the error is a *PolicyError when it can be decoded.
func (b *Bank) WouldAuthorize(ctx context.Context, node [32]byte, token, to common.Address, amount *big.Int, coSigned bool) error {
	engine := bind.NewBoundContract(b.Engine, PolicyEngineABI, b.backend, b.backend, b.backend)
	out, err := b.call(ctx, engine, "wouldAuthorize", node, token, to, amount, coSigned)
	if err != nil {
		return err
	}
	if out[0].(bool) {
		return nil
	}
	return DecodePolicyError(out[1].([]byte))
}

// DecodePolicyError maps revert data to a named PolicyEngine error.
func DecodePolicyError(data []byte) error {
	if len(data) < 4 {
		return &PolicyError{Name: "Unknown"}
	}
	for name, e := range PolicyEngineABI.Errors {
		if bytes.Equal(e.ID[:4], data[:4]) {
			args, err := e.Inputs.Unpack(data[4:])
			if err != nil {
				return &PolicyError{Name: name}
			}
			return &PolicyError{Name: name, Args: args}
		}
	}
	return &PolicyError{Name: fmt.Sprintf("0x%x", data[:4])}
}

// IsPolicyError reports whether err is the named policy error.
func IsPolicyError(err error, name string) bool {
	var pe *PolicyError
	return errors.As(err, &pe) && pe.Name == name
}

// RecoverSigner returns the address that produced a 65-byte (r,s,v)
// signature over digest, rejecting high-s malleable signatures as the
// contract does.
func RecoverSigner(digest [32]byte, sig []byte) (common.Address, error) {
	if len(sig) != 65 {
		return common.Address{}, errors.New("chain: signature must be 65 bytes")
	}
	s := append([]byte(nil), sig...)
	if s[64] >= 27 {
		s[64] -= 27
	}
	if !crypto.ValidateSignatureValues(s[64], new(big.Int).SetBytes(s[0:32]), new(big.Int).SetBytes(s[32:64]), true) {
		return common.Address{}, errors.New("chain: invalid signature values")
	}
	pub, err := crypto.SigToPub(digest[:], s)
	if err != nil {
		return common.Address{}, err
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// SignDigest signs an EIP-712 digest in the contract's (r,s,v) format with v
// in {27,28}. Used by agents' SDKs and tests.
func SignDigest(digest [32]byte, key interface{ Sign([]byte) ([]byte, error) }) ([]byte, error) {
	sig, err := key.Sign(digest[:])
	if err != nil {
		return nil, err
	}
	sig[64] += 27
	return sig, nil
}

// Relayer submits signed intents. It pays gas but cannot alter intents: the
// account verifies the agent's signature on-chain.
type Relayer struct {
	backend bind.ContractBackend
	opts    *bind.TransactOpts
}

func NewRelayer(backend bind.ContractBackend, opts *bind.TransactOpts) *Relayer {
	return &Relayer{backend: backend, opts: opts}
}

func (r *Relayer) ExecutePayment(ctx context.Context, account common.Address, i PaymentIntent, sessionSig, ownerSig []byte) (common.Hash, error) {
	c := bind.NewBoundContract(account, AgentAccountABI, r.backend, r.backend, r.backend)
	opts := *r.opts
	opts.Context = ctx
	if ownerSig == nil {
		ownerSig = []byte{}
	}
	tx, err := c.Transact(&opts, "executePayment", i, sessionSig, ownerSig)
	if err != nil {
		return common.Hash{}, err
	}
	return tx.Hash(), nil
}
