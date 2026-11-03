// Command demo plays an org owner and its agent against a running bankd:
// open and fund an account, set a policy tree, then pay through the API.
// It exits non-zero if the bank does not behave as expected, so it doubles
// as the end-to-end test (scripts/e2e.sh).
//
//	DEMO_RPC_URL, DEMO_BANK_URL, DEMO_OWNER_KEY, DEMO_FUNDER_KEY,
//	BANK_ENGINE, BANK_FACTORY, BANK_TOKENS (first token used), FAUCET
//	DEMO_AGENT_KEY  optional fixed session key (default: random)
//	DEMO_OUT        optional file to write AGENT_ACCOUNT and MERCHANT to
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/ayushns01/agentic-bank/services/bank"
	"github.com/ayushns01/agentic-bank/services/chain"
)

const usd = 1_000_000

type limits struct {
	PerTxLimit         *big.Int
	PeriodLimit        *big.Int
	CoSignAbove        *big.Int
	PeriodLength       uint64
	ValidUntil         uint64
	RecipientAllowlist bool
}

type demo struct {
	ctx      context.Context
	client   *ethclient.Client
	bankURL  string
	owner    *bind.TransactOpts
	ownerKey *ecdsa.PrivateKey
	funder   *bind.TransactOpts
	agentKey *ecdsa.PrivateKey

	engine, factory, token, faucet, account common.Address
}

var (
	org      = crypto.Keccak256Hash([]byte("demo-org"))
	agent    = crypto.Keccak256Hash([]byte("demo-org/shopping-agent"))
	merchant = common.HexToAddress("0x000000000000000000000000000000000000Cafe")
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS: end-to-end demo")
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client, err := ethclient.DialContext(ctx, os.Getenv("DEMO_RPC_URL"))
	if err != nil {
		return err
	}
	chainID, err := client.ChainID(ctx)
	if err != nil {
		return err
	}
	d := &demo{ctx: ctx, client: client, bankURL: strings.TrimRight(os.Getenv("DEMO_BANK_URL"), "/")}
	if d.ownerKey, d.owner, err = signer(os.Getenv("DEMO_OWNER_KEY"), chainID); err != nil {
		return err
	}
	if _, d.funder, err = signer(os.Getenv("DEMO_FUNDER_KEY"), chainID); err != nil {
		return err
	}
	if k := os.Getenv("DEMO_AGENT_KEY"); k != "" {
		d.agentKey, err = crypto.HexToECDSA(strings.TrimPrefix(k, "0x"))
	} else {
		d.agentKey, err = crypto.GenerateKey()
	}
	if err != nil {
		return err
	}
	d.engine = common.HexToAddress(os.Getenv("BANK_ENGINE"))
	d.factory = common.HexToAddress(os.Getenv("BANK_FACTORY"))
	d.token = common.HexToAddress(strings.Split(os.Getenv("BANK_TOKENS"), ",")[0])
	d.faucet = common.HexToAddress(os.Getenv("FAUCET"))

	steps := []struct {
		name string
		fn   func() error
	}{
		{"open account", d.openAccount},
		{"fund from faucet", d.fund},
		{"set policy tree and session key", d.configure},
		{"bank indexes the deposit", d.waitForDeposit},
		{"agent pays within policy", d.payWithinPolicy},
		{"prompt-injected drain is rejected", d.drainRejected},
		{"large payment needs owner co-sign", d.coSigned},
		{"books equal the chain", d.reconciled},
	}
	for i, s := range steps {
		fmt.Printf("[%d/%d] %s ... ", i+1, len(steps), s.name)
		if err := s.fn(); err != nil {
			fmt.Println("failed")
			return fmt.Errorf("%s: %w", s.name, err)
		}
		fmt.Println("ok")
	}
	if out := os.Getenv("DEMO_OUT"); out != "" {
		env := fmt.Sprintf("AGENT_ACCOUNT=%s\nMERCHANT=%s\n", d.account.Hex(), merchant.Hex())
		return os.WriteFile(out, []byte(env), 0o600)
	}
	return nil
}

func signer(hexKey string, chainID *big.Int) (*ecdsa.PrivateKey, *bind.TransactOpts, error) {
	k, err := crypto.HexToECDSA(strings.TrimPrefix(hexKey, "0x"))
	if err != nil {
		return nil, nil, err
	}
	opts, err := bind.NewKeyedTransactorWithChainID(k, chainID)
	return k, opts, err
}

func (d *demo) send(from *bind.TransactOpts, to common.Address, a abi.ABI, method string, args ...any) error {
	c := bind.NewBoundContract(to, a, d.client, d.client, d.client)
	opts := *from
	opts.Context = d.ctx
	tx, err := c.Transact(&opts, method, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	r, err := bind.WaitMined(d.ctx, d.client, tx)
	if err != nil {
		return err
	}
	if r.Status != 1 {
		return fmt.Errorf("%s reverted", method)
	}
	return nil
}

func (d *demo) admin(method string, args ...any) error {
	data, err := chain.PolicyEngineABI.Pack(method, args...)
	if err != nil {
		return err
	}
	return d.send(d.owner, d.account, chain.AgentAccountABI, "ownerCall", d.engine, big.NewInt(0), data)
}

func (d *demo) openAccount() error {
	salt := crypto.Keccak256Hash([]byte(fmt.Sprintf("demo-%d", time.Now().UnixNano())))
	var out []any
	c := bind.NewBoundContract(d.factory, chain.AccountFactoryABI, d.client, d.client, d.client)
	if err := c.Call(&bind.CallOpts{Context: d.ctx}, &out, "predictAddress", d.owner.From, salt); err != nil {
		return err
	}
	d.account = out[0].(common.Address)
	fmt.Printf("(%s) ", d.account.Hex())
	return d.send(d.owner, d.factory, chain.AccountFactoryABI, "openAccount", d.owner.From, salt)
}

func (d *demo) fund() error {
	return d.send(d.funder, d.faucet, chain.FaucetABI, "claim", d.token, d.account)
}

func (d *demo) configure() error {
	never := new(big.Int).Lsh(big.NewInt(1), 127)
	if err := d.admin("createNode", org, [32]byte{}, limits{big.NewInt(1_000 * usd), big.NewInt(1_000 * usd), never, 86400, 0, false}); err != nil {
		return err
	}
	if err := d.admin("createNode", agent, org, limits{big.NewInt(50 * usd), big.NewInt(200 * usd), big.NewInt(40 * usd), 86400, 0, false}); err != nil {
		return err
	}
	for _, n := range [][32]byte{org, agent} {
		if err := d.admin("setToken", n, d.token, true); err != nil {
			return err
		}
	}
	agentAddr := crypto.PubkeyToAddress(d.agentKey.PublicKey)
	return d.send(d.owner, d.account, chain.AgentAccountABI, "setSessionKey", agentAddr, agent, uint64(time.Now().Add(24*time.Hour).Unix()))
}

func (d *demo) waitForDeposit() error {
	return d.poll(func() (bool, error) {
		var acct struct {
			Balances []struct {
				Available string `json:"available"`
			} `json:"balances"`
		}
		code, err := d.get("/v1/accounts/"+d.account.Hex(), &acct)
		if err != nil || code != 200 {
			return false, nil
		}
		return len(acct.Balances) > 0 && acct.Balances[0].Available == fmt.Sprint(1_000*usd), nil
	})
}

type paymentView struct {
	State   string `json:"state"`
	TxHash  string `json:"txHash"`
	History []struct {
		To     string `json:"to"`
		Reason string `json:"reason"`
	} `json:"history"`
}

// intent builds and signs a payment the way an agent SDK does.
func (d *demo) intent(id string, amount int64, nonce int64) (chain.PaymentIntent, map[string]any, error) {
	i := chain.PaymentIntent{
		Token: d.token, To: merchant, Amount: big.NewInt(amount), Ref: bank.RefFor(id),
		Nonce: big.NewInt(nonce), Deadline: uint64(time.Now().Add(time.Hour).Unix()),
	}
	digest, err := chain.NewBank(d.client, d.engine).IntentDigest(d.ctx, d.account, i)
	if err != nil {
		return i, nil, err
	}
	sig, err := crypto.Sign(digest[:], d.agentKey)
	if err != nil {
		return i, nil, err
	}
	sig[64] += 27
	body := map[string]any{
		"id": id, "account": d.account.Hex(), "token": d.token.Hex(), "to": merchant.Hex(),
		"amount": i.Amount.String(), "nonce": i.Nonce.String(), "deadline": i.Deadline,
		"sessionSig": hexutil.Encode(sig),
	}
	return i, body, nil
}

func (d *demo) waitState(id, want string) error {
	return d.poll(func() (bool, error) {
		var p paymentView
		if _, err := d.get("/v1/payments/"+id, &p); err != nil {
			return false, err
		}
		if p.State == "rejected" || p.State == "exception" {
			return false, fmt.Errorf("payment %s ended %s: %+v", id, p.State, p.History)
		}
		return p.State == want, nil
	})
}

func (d *demo) payWithinPolicy() error {
	_, body, err := d.intent("demo-pay-1", 25*usd, 0)
	if err != nil {
		return err
	}
	var p paymentView
	if code, err := d.post("/v1/payments", body, &p); err != nil || code != 201 {
		return fmt.Errorf("submit: %d %v %+v", code, err, p)
	}
	return d.waitState("demo-pay-1", "settled")
}

func (d *demo) drainRejected() error {
	_, body, err := d.intent("demo-drain", 900*usd, 1)
	if err != nil {
		return err
	}
	var p paymentView
	code, err := d.post("/v1/payments", body, &p)
	if err != nil {
		return err
	}
	if code != 422 || p.State != "rejected" || p.TxHash != "" {
		return fmt.Errorf("want 422 rejected without a tx, got %d %+v", code, p)
	}
	return nil
}

func (d *demo) coSigned() error {
	i, body, err := d.intent("demo-big", 45*usd, 1) // nonce 1: the drain never consumed it
	if err != nil {
		return err
	}
	var p paymentView
	if code, err := d.post("/v1/payments", body, &p); err != nil || code != 202 {
		return fmt.Errorf("submit: %d %v %+v", code, err, p)
	}
	digest, err := chain.NewBank(d.client, d.engine).IntentDigest(d.ctx, d.account, i)
	if err != nil {
		return err
	}
	sig, err := crypto.Sign(digest[:], d.ownerKey)
	if err != nil {
		return err
	}
	sig[64] += 27
	if code, err := d.post("/v1/payments/demo-big/cosign", map[string]string{"ownerSig": hexutil.Encode(sig)}, &p); err != nil || code != 200 {
		return fmt.Errorf("cosign: %d %v %+v", code, err, p)
	}
	return d.waitState("demo-big", "settled")
}

func (d *demo) reconciled() error {
	var out []any
	c := bind.NewBoundContract(d.token, chain.TestTokenABI, d.client, d.client, d.client)
	if err := c.Call(&bind.CallOpts{Context: d.ctx}, &out, "balanceOf", d.account); err != nil {
		return err
	}
	onChain := out[0].(*big.Int)
	var acct struct {
		Balances []struct {
			OnChain string `json:"onChain"`
			Owed    string `json:"owed"`
		} `json:"balances"`
	}
	if _, err := d.get("/v1/accounts/"+d.account.Hex(), &acct); err != nil {
		return err
	}
	b := acct.Balances[0]
	want := fmt.Sprint(1_000*usd - 25*usd - 45*usd)
	if onChain.String() != want || b.OnChain != want || b.Owed != want {
		return fmt.Errorf("chain %s, books %s/%s, want %s", onChain, b.OnChain, b.Owed, want)
	}
	return nil
}

// ------------------------------------------------------------------ http

func (d *demo) get(path string, out any) (int, error) {
	req, _ := http.NewRequestWithContext(d.ctx, "GET", d.bankURL+path, nil)
	return d.do(req, out)
}

func (d *demo) post(path string, body, out any) (int, error) {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(d.ctx, "POST", d.bankURL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	return d.do(req, out)
}

func (d *demo) do(req *http.Request, out any) (int, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
}

func (d *demo) poll(f func() (bool, error)) error {
	for {
		ok, err := f()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-d.ctx.Done():
			return d.ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}
