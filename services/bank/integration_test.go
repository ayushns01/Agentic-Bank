package bank_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ayushns01/agentic-bank/services/bank"
	"github.com/ayushns01/agentic-bank/services/chain"
	"github.com/ayushns01/agentic-bank/services/internal/testchain"
	"github.com/ayushns01/agentic-bank/services/orchestrator"
)

func TestAccountOpeningAndDepositAreBooked(t *testing.T) {
	e := testchain.Setup(t)
	info, ok := e.Bank.Account(e.Account)
	if !ok || info.Owner != e.Owner.From {
		t.Fatalf("account not indexed: %+v", info)
	}
	onChain, owed, avail := e.Books()
	if onChain != 1_000*testchain.USD || owed != 1_000*testchain.USD || avail != 1_000*testchain.USD {
		t.Fatalf("books %d/%d/%d", onChain, owed, avail)
	}
	st := e.Bank.Statement(e.Account, e.Token)
	if len(st) != 1 || !strings.HasPrefix(st[0].Memo, "deposit from") {
		t.Fatalf("statement %+v", st)
	}
}

func TestPaymentLifecycleEndToEnd(t *testing.T) {
	e := testchain.Setup(t)
	ctx := context.Background()

	p, err := e.Bank.SubmitPayment(ctx, e.AgentRequest("pay-1", 30*testchain.USD, 0))
	if err != nil {
		t.Fatal(err)
	}
	if p.State != orchestrator.Broadcast {
		t.Fatalf("state %s, history %+v", p.State, p.History)
	}
	if _, _, avail := e.Books(); avail != 970*testchain.USD {
		t.Fatalf("funds not held while in flight: available %d", avail)
	}

	e.Sim.Commit()
	e.Sync()

	p, _ = e.Bank.Orchestrator().Get("pay-1")
	if p.State != orchestrator.Settled {
		t.Fatalf("state %s, history %+v", p.State, p.History)
	}
	if got := e.TokenBalance(testchain.Merchant); got != 30*testchain.USD {
		t.Fatalf("testchain.Merchant got %d", got)
	}
	onChain, owed, avail := e.Books()
	if onChain != 970*testchain.USD || owed != 970*testchain.USD || avail != 970*testchain.USD {
		t.Fatalf("books %d/%d/%d", onChain, owed, avail)
	}
	// Books equal the chain, and the transfer was booked once, not twice.
	if int64(onChain) != e.TokenBalance(e.Account) {
		t.Fatalf("ledger %d != chain %d", onChain, e.TokenBalance(e.Account))
	}
	if n := len(e.Bank.Statement(e.Account, e.Token)); n != 2 {
		t.Fatalf("statement has %d entries, want deposit + payment", n)
	}
	if got := e.Bank.AgentSpend(e.Account, testchain.AgentNode, e.Token); got.Int64() != 30*testchain.USD {
		t.Fatalf("agent spend %s", got)
	}

	// Re-delivering the same blocks changes nothing.
	if err := e.Bank.Handle(ctx, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyViolationRejectedBeforeBroadcast(t *testing.T) {
	e := testchain.Setup(t)
	before := e.TokenBalance(e.Account)
	p, err := e.Bank.SubmitPayment(context.Background(), e.AgentRequest("drain", 900*testchain.USD, 0))
	if err != nil {
		t.Fatal(err)
	}
	if p.State != orchestrator.Rejected || p.TxHash != "" {
		t.Fatalf("state %s tx %q", p.State, p.TxHash)
	}
	last := p.History[len(p.History)-1].Reason
	if !strings.Contains(last, "PerTxLimit") {
		t.Fatalf("reason %q", last)
	}
	if _, _, avail := e.Books(); avail != 1_000*testchain.USD {
		t.Fatal("rejected payment must release its hold")
	}
	if e.TokenBalance(e.Account) != before {
		t.Fatal("nothing should move on-chain")
	}
}

func TestCoSignFlow(t *testing.T) {
	e := testchain.Setup(t)
	ctx := context.Background()
	req := e.AgentRequest("big-1", 45*testchain.USD, 0)
	p, err := e.Bank.SubmitPayment(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if p.State != orchestrator.AwaitingCoSign {
		t.Fatalf("state %s, history %+v", p.State, p.History)
	}

	// A signature from anyone but the owner is refused.
	digest, _ := chain.NewBank(e.Client, e.Engine).IntentDigest(ctx, e.Account, chain.PaymentIntent{
		Token: e.Token, To: testchain.Merchant, Amount: req.Amount, Ref: bank.RefFor("big-1"), Nonce: req.Nonce, Deadline: req.Deadline,
	})
	if _, err := e.Bank.CoSign(ctx, "big-1", testchain.Sign(t, digest, e.AgentKey)); err == nil {
		t.Fatal("agent co-signing for itself must fail")
	}

	p, err = e.Bank.CoSign(ctx, "big-1", testchain.Sign(t, digest, e.OwnerKey))
	if err != nil {
		t.Fatal(err)
	}
	if p.State != orchestrator.Broadcast {
		t.Fatalf("state %s", p.State)
	}
	e.Sim.Commit()
	e.Sync()
	if p, _ = e.Bank.Orchestrator().Get("big-1"); p.State != orchestrator.Settled {
		t.Fatalf("state %s", p.State)
	}
	if e.TokenBalance(testchain.Merchant) != 45*testchain.USD {
		t.Fatal("testchain.Merchant not paid")
	}
}

func TestDirectOnChainPaymentStillBooked(t *testing.T) {
	// An agent can relay its own intent without the bank's API. The indexer
	// must still book it from the Transfer event.
	e := testchain.Setup(t)
	req := e.AgentRequest("self-relayed", 10*testchain.USD, 0)
	intent := chain.PaymentIntent{Token: e.Token, To: testchain.Merchant, Amount: req.Amount, Ref: [32]byte{0xEE}, Nonce: req.Nonce, Deadline: req.Deadline}
	digest, _ := chain.NewBank(e.Client, e.Engine).IntentDigest(context.Background(), e.Account, intent)
	e.Send(e.Owner, e.Account, chain.AgentAccountABI, "executePayment", intent, testchain.Sign(t, digest, e.AgentKey), []byte{})
	e.Sync()
	onChain, owed, _ := e.Books()
	if onChain != 990*testchain.USD || owed != 990*testchain.USD {
		t.Fatalf("books %d/%d", onChain, owed)
	}
}

func TestUnknownAccountAndToken(t *testing.T) {
	e := testchain.Setup(t)
	req := e.AgentRequest("x", 1, 0)
	req.Account = testchain.Merchant
	if _, err := e.Bank.SubmitPayment(context.Background(), req); err == nil {
		t.Fatal("unknown account must be refused")
	}
	req = e.AgentRequest("y", 1, 0)
	req.Token = testchain.Merchant
	if _, err := e.Bank.SubmitPayment(context.Background(), req); err == nil {
		t.Fatal("unsupported token must be refused")
	}
}
