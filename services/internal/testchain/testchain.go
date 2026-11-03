// Package testchain deploys the bank's contracts on an in-process simulated
// chain for integration tests.
package testchain

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"

	"github.com/ayushns01/agentic-bank/services/bank"
	"github.com/ayushns01/agentic-bank/services/chain"
	"github.com/ayushns01/agentic-bank/services/indexer"
	"github.com/ayushns01/agentic-bank/services/ledger"
	"github.com/ayushns01/agentic-bank/services/orchestrator"
)

// testdata locates chain/testdata regardless of the caller's directory.
func testdata() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "chain", "testdata")
}

// USD is one unit of the 6-decimal test stablecoin.
const USD = 1_000_000

// Limits mirrors PolicyEngine.Limits for ABI packing.
type Limits struct {
	PerTxLimit         *big.Int
	PeriodLimit        *big.Int
	CoSignAbove        *big.Int
	PeriodLength       uint64
	ValidUntil         uint64
	RecipientAllowlist bool
}

// Env is a deployed bank on a simulated chain: contracts, a funded account
// with an org -> agent policy tree, and a running bank + indexer.
type Env struct {
	t        *testing.T
	Sim      *simulated.Backend
	Client   simulated.Client
	Deployer *bind.TransactOpts
	Owner    *bind.TransactOpts
	OwnerKey *ecdsa.PrivateKey
	AgentKey *ecdsa.PrivateKey

	Engine, Factory, Token, Faucet, Escrow, Account common.Address

	Bank *bank.Bank
	Ix   *indexer.Indexer
}

var (
	OrgNode   = crypto.Keccak256Hash([]byte("acme"))
	AgentNode = crypto.Keccak256Hash([]byte("acme/research-agent"))
	Merchant  = common.HexToAddress("0x000000000000000000000000000000000000bEEF")
)

// Key makes a funded-chain signer.
func Key(t *testing.T) (*ecdsa.PrivateKey, *bind.TransactOpts) {
	k, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	opts, err := bind.NewKeyedTransactorWithChainID(k, big.NewInt(1337))
	if err != nil {
		t.Fatal(err)
	}
	return k, opts
}

func bytecode(t *testing.T, name string) []byte {
	raw, err := os.ReadFile(testdata() + "/" + name + ".bin")
	if err != nil {
		t.Fatal(err)
	}
	return common.FromHex(strings.TrimSpace(string(raw)))
}

func (e *Env) deploy(name string, a abi.ABI, args ...any) common.Address {
	addr, _, _, err := bind.DeployContract(e.Deployer, a, bytecode(e.t, name), e.Client, args...)
	if err != nil {
		e.t.Fatalf("deploy %s: %v", name, err)
	}
	e.Sim.Commit()
	return addr
}

// Send transacts and mines a block, failing the test on revert.
func (e *Env) Send(from *bind.TransactOpts, to common.Address, a abi.ABI, method string, args ...any) {
	e.t.Helper()
	c := bind.NewBoundContract(to, a, e.Client, e.Client, e.Client)
	tx, err := c.Transact(from, method, args...)
	if err != nil {
		e.t.Fatalf("%s: %v", method, err)
	}
	e.Sim.Commit()
	e.mustSucceed(tx)
}

func (e *Env) mustSucceed(tx *types.Transaction) {
	e.t.Helper()
	r, err := e.Client.TransactionReceipt(context.Background(), tx.Hash())
	if err != nil || r.Status != types.ReceiptStatusSuccessful {
		e.t.Fatalf("tx %s failed: %v", tx.Hash().Hex(), err)
	}
}

// OwnerAdmin calls the PolicyEngine through the account, as its owner.
func (e *Env) OwnerAdmin(method string, args ...any) {
	e.t.Helper()
	data, err := chain.PolicyEngineABI.Pack(method, args...)
	if err != nil {
		e.t.Fatal(err)
	}
	e.Send(e.Owner, e.Account, chain.AgentAccountABI, "ownerCall", e.Engine, big.NewInt(0), data)
}

// Call reads a contract.
func (e *Env) Call(to common.Address, a abi.ABI, method string, args ...any) []any {
	e.t.Helper()
	var out []any
	c := bind.NewBoundContract(to, a, e.Client, e.Client, e.Client)
	if err := c.Call(&bind.CallOpts{}, &out, method, args...); err != nil {
		e.t.Fatalf("%s: %v", method, err)
	}
	return out
}

// TokenBalance is the on-chain test-token balance of a.
func (e *Env) TokenBalance(a common.Address) int64 {
	return e.Call(e.Token, chain.TestTokenABI, "balanceOf", a)[0].(*big.Int).Int64()
}

// Sync runs the indexer until it has caught up.
func (e *Env) Sync() {
	e.t.Helper()
	for {
		ok, err := e.Ix.Step(context.Background())
		if err != nil {
			e.t.Fatal(err)
		}
		if !ok {
			return
		}
	}
}

// Setup deploys everything and syncs the indexer.
func Setup(t *testing.T) *Env {
	e := &Env{t: t}
	_, e.Deployer = Key(t)
	e.OwnerKey, e.Owner = Key(t)
	e.AgentKey, _ = Key(t)
	_, relayer := Key(t)

	fund := new(big.Int).Mul(big.NewInt(100), big.NewInt(1e18))
	e.Sim = simulated.NewBackend(types.GenesisAlloc{
		e.Deployer.From: {Balance: fund},
		e.Owner.From:    {Balance: fund},
		relayer.From:    {Balance: fund},
	})
	t.Cleanup(func() { e.Sim.Close() })
	e.Client = e.Sim.Client()

	e.Engine = e.deploy("PolicyEngine", chain.PolicyEngineABI)
	e.Factory = e.deploy("AccountFactory", chain.AccountFactoryABI, e.Engine)
	e.Token = e.deploy("TestToken", chain.TestTokenABI, "Test USD Coin", "tUSDC", uint8(6), e.Deployer.From)
	e.Faucet = e.deploy("Faucet", chain.FaucetABI, e.Deployer.From)
	e.Escrow = e.deploy("Escrow", chain.EscrowABI)

	e.Send(e.Deployer, e.Token, chain.TestTokenABI, "setMinter", e.Faucet, true)
	e.Send(e.Deployer, e.Faucet, chain.FaucetABI, "setDrip", e.Token, big.NewInt(1_000*USD), big.NewInt(5_000*USD), uint64(0), true)

	// The bank and its indexer start before any account exists.
	l := ledger.New()
	e.Bank = bank.New(l, chain.NewBank(e.Client, e.Engine), chain.NewRelayer(e.Client, relayer),
		[]common.Address{e.Token}, orchestrator.Config{})
	e.Ix = indexer.New(e.Client, e.Bank, indexer.Config{
		Factory: e.Factory, Escrow: e.Escrow, Tokens: []common.Address{e.Token},
	})

	// Open an account and fund it from the faucet.
	salt := [32]byte{'m', 'a', 'i', 'n'}
	e.Account = e.Call(e.Factory, chain.AccountFactoryABI, "predictAddress", e.Owner.From, salt)[0].(common.Address)
	e.Send(e.Owner, e.Factory, chain.AccountFactoryABI, "openAccount", e.Owner.From, salt)
	e.Send(e.Deployer, e.Faucet, chain.FaucetABI, "claim", e.Token, e.Account)

	// Org 1000/day; research agent 200/day, 50 per payment, co-sign above 40.
	far := uint64(1) << 62
	e.OwnerAdmin("createNode", OrgNode, [32]byte{}, Limits{big.NewInt(1_000 * USD), big.NewInt(1_000 * USD), new(big.Int).Lsh(big.NewInt(1), 127), 86400, 0, false})
	e.OwnerAdmin("createNode", AgentNode, OrgNode, Limits{big.NewInt(50 * USD), big.NewInt(200 * USD), big.NewInt(40 * USD), 86400, 0, false})
	e.OwnerAdmin("setToken", OrgNode, e.Token, true)
	e.OwnerAdmin("setToken", AgentNode, e.Token, true)
	e.Send(e.Owner, e.Account, chain.AgentAccountABI, "setSessionKey", crypto.PubkeyToAddress(e.AgentKey.PublicKey), AgentNode, far)

	e.Sync()
	return e
}

// Sign produces a contract-format (r,s,v) signature over digest.
func Sign(t *testing.T, digest [32]byte, k *ecdsa.PrivateKey) []byte {
	sig, err := crypto.Sign(digest[:], k)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27
	return sig
}

// AgentRequest builds a request the way an agent SDK would: fetch the digest
// for the intent (ref derived from the payment id) and sign it.
func (e *Env) AgentRequest(id string, amount int64, nonce int64) bank.PaymentRequest {
	e.t.Helper()
	intent := chain.PaymentIntent{
		Token: e.Token, To: Merchant, Amount: big.NewInt(amount), Ref: bank.RefFor(id),
		Nonce: big.NewInt(nonce), Deadline: uint64(1) << 62,
	}
	digest, err := chain.NewBank(e.Client, e.Engine).IntentDigest(context.Background(), e.Account, intent)
	if err != nil {
		e.t.Fatal(err)
	}
	return bank.PaymentRequest{
		ID: id, Account: e.Account, Token: e.Token, To: Merchant, Amount: intent.Amount,
		Nonce: intent.Nonce, Deadline: intent.Deadline, SessionSig: Sign(e.t, digest, e.AgentKey),
	}
}

// Books returns the account's token position in the bank's books.
func (e *Env) Books() (onChain, owed, available int64) {
	for _, b := range e.Bank.Balances(e.Account) {
		if b.Token == e.Token {
			return b.OnChain.Int64(), b.Owed.Int64(), b.Available.Int64()
		}
	}
	e.t.Fatal("no balance for token")
	return
}
