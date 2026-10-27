package orchestrator

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ayushns01/agentic-bank/services/ledger"
)

const usdc = "USDC:base"

type gateFunc struct {
	name string
	fn   func(*Payment) error
}

func (g gateFunc) Name() string                              { return g.name }
func (g gateFunc) Check(_ context.Context, p *Payment) error { return g.fn(p) }

type fakeScreener struct {
	v   Verdict
	err error
}

func (s fakeScreener) Screen(context.Context, *Payment) (Verdict, string, error) {
	return s.v, "list hit", s.err
}

type fakeSigner struct {
	coSignAbove int64
	err         error
}

func (s fakeSigner) Sign(_ context.Context, p *Payment) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.coSignAbove > 0 && p.Amount.Int64() > s.coSignAbove, nil
}

type fakeBroadcaster struct{ err error }

func (b fakeBroadcaster) Broadcast(_ context.Context, p *Payment) (string, error) {
	if b.err != nil {
		return "", b.err
	}
	return "0xtx-" + p.ID, nil
}

type fakeSupply struct{ err error }

func (s fakeSupply) Reserve(context.Context, *Payment) error { return s.err }

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

type harness struct {
	o   *Orchestrator
	l   *ledger.Ledger
	clk *clock
	cfg Config
}

func newHarness(t *testing.T, mut func(*Config)) *harness {
	t.Helper()
	l := ledger.New()
	for _, a := range []ledger.Account{
		{ID: "wallet:usdc", Type: ledger.Asset, Asset: usdc},
		{ID: "agent:a", Type: ledger.Liability, Asset: usdc},
		{ID: "gas", Type: ledger.Expense, Asset: usdc},
	} {
		if err := l.CreateAccount(a); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Post(ledger.Entry{ID: "dep", Lines: []ledger.Line{
		{Account: "wallet:usdc", Side: ledger.Debit, Amount: big.NewInt(1000)},
		{Account: "agent:a", Side: ledger.Credit, Amount: big.NewInt(1000)},
	}}); err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	cfg := Config{
		Ledger:      l,
		Wallets:     map[string]string{usdc: "wallet:usdc"},
		GasAccount:  "gas",
		Screener:    fakeScreener{v: Clear},
		Signer:      fakeSigner{coSignAbove: 500},
		Broadcaster: fakeBroadcaster{},
		Now:         clk.now,
	}
	if mut != nil {
		mut(&cfg)
	}
	return &harness{o: New(cfg), l: l, clk: clk, cfg: cfg}
}

func pay(id string, v int64) Payment {
	return Payment{ID: id, Node: "research-agent", PayerAccount: "agent:a", Payee: "0xmerchant",
		Asset: usdc, Amount: big.NewInt(v), Rail: "x402"}
}

func (h *harness) available(t *testing.T) int64 {
	t.Helper()
	a, err := h.l.Available("agent:a")
	if err != nil {
		t.Fatal(err)
	}
	return a.Int64()
}

func states(p Payment) []State {
	out := []State{}
	for _, e := range p.History {
		out = append(out, e.To)
	}
	return out
}

func wantState(t *testing.T, p Payment, s State) {
	t.Helper()
	if p.State != s {
		t.Fatalf("state = %s, want %s (history %v)", p.State, s, p.History)
	}
}

func TestHappyPathSettles(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	p, err := h.o.Submit(ctx, pay("p1", 200))
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, p, Broadcast)
	if got := h.available(t); got != 800 {
		t.Fatalf("held: available = %d", got)
	}
	p, _ = h.o.OnConfirmed("p1", p.TxHash, big.NewInt(3))
	wantState(t, p, Confirmed)
	p, _ = h.o.Reconcile("p1", big.NewInt(200))
	wantState(t, p, Settled)

	want := []State{Validated, Screening, Signed, Broadcast, Confirmed, Reconciled, Settled}
	got := states(p)
	if len(got) != len(want) {
		t.Fatalf("history %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("history %v, want %v", got, want)
		}
	}
	if b, _ := h.l.Balance("agent:a"); b.Int64() != 800 {
		t.Fatalf("agent balance = %s", b)
	}
	if b, _ := h.l.Balance("wallet:usdc"); b.Int64() != 797 {
		t.Fatalf("wallet balance = %s (amount + gas should leave)", b)
	}
	if h.l.HoldActive(p.HoldID) {
		t.Fatal("hold should be consumed by posting")
	}
}

func TestSubmitIdempotent(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	a, _ := h.o.Submit(ctx, pay("p1", 200))
	b, err := h.o.Submit(ctx, pay("p1", 200))
	if err != nil || b.State != a.State || len(b.History) != len(a.History) {
		t.Fatalf("resubmit changed payment: %v %+v", err, b)
	}
	if got := h.available(t); got != 800 {
		t.Fatalf("double hold: available = %d", got)
	}
	if _, err := h.o.Submit(ctx, pay("p1", 201)); !errors.Is(err, ErrIdempotency) {
		t.Fatalf("want ErrIdempotency, got %v", err)
	}
}

func TestInvalidSubmit(t *testing.T) {
	h := newHarness(t, nil)
	bad := pay("p", 0)
	if _, err := h.o.Submit(context.Background(), bad); !errors.Is(err, ErrInvalidPayment) {
		t.Fatalf("zero amount: %v", err)
	}
	bad = pay("p", 1)
	bad.Asset = "DOGE"
	if _, err := h.o.Submit(context.Background(), bad); !errors.Is(err, ErrInvalidPayment) {
		t.Fatalf("unsupported asset: %v", err)
	}
}

func TestGateRejectsBeforeFunding(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.Gates = []Gate{gateFunc{"address-risk", func(p *Payment) error {
			if p.Payee == "0xbad" {
				return errors.New("high risk address")
			}
			return nil
		}}}
	})
	in := pay("p1", 100)
	in.Payee = "0xbad"
	p, _ := h.o.Submit(context.Background(), in)
	wantState(t, p, Rejected)
	if p.HoldID != "" || h.available(t) != 1000 {
		t.Fatal("rejected-at-gate payment must not hold funds")
	}
}

func TestInsufficientFundsRejected(t *testing.T) {
	h := newHarness(t, nil)
	p, _ := h.o.Submit(context.Background(), pay("p1", 1001))
	wantState(t, p, Rejected)
	if h.available(t) != 1000 {
		t.Fatal("funds should be untouched")
	}
}

func TestScreeningBlockReleasesHold(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Screener = fakeScreener{v: Block} })
	p, _ := h.o.Submit(context.Background(), pay("p1", 100))
	wantState(t, p, Rejected)
	if h.available(t) != 1000 {
		t.Fatal("hold not released")
	}
}

func TestScreeningReviewKeepsHold(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Screener = fakeScreener{v: Review} })
	p, _ := h.o.Submit(context.Background(), pay("p1", 100))
	wantState(t, p, Exception)
	if h.available(t) != 900 {
		t.Fatal("funds must stay held during review")
	}
	p, _ = h.o.Return("p1", "cleared as false positive, returned")
	wantState(t, p, Returned)
	if h.available(t) != 1000 {
		t.Fatal("return must release hold")
	}
}

func TestScreeningErrorGoesToException(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Screener = fakeScreener{err: errors.New("provider down")} })
	p, _ := h.o.Submit(context.Background(), pay("p1", 100))
	wantState(t, p, Exception)
}

func TestNeverSignsBeforeScreening(t *testing.T) {
	signed := false
	h := newHarness(t, func(c *Config) {
		c.Screener = fakeScreener{v: Block}
		c.Signer = signerFunc(func() { signed = true })
	})
	h.o.Submit(context.Background(), pay("p1", 100))
	if signed {
		t.Fatal("signer called for a blocked payment")
	}
}

type signerFunc func()

func (f signerFunc) Sign(context.Context, *Payment) (bool, error) { f(); return false, nil }

func TestCoSignApprove(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	p, _ := h.o.Submit(ctx, pay("p1", 600))
	wantState(t, p, AwaitingCoSign)
	p, err := h.o.CoSign(ctx, "p1", true, "owner approved")
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, p, Broadcast)
}

func TestCoSignDecline(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	h.o.Submit(ctx, pay("p1", 600))
	p, _ := h.o.CoSign(ctx, "p1", false, "unexpected vendor")
	wantState(t, p, Rejected)
	if h.available(t) != 1000 {
		t.Fatal("hold not released")
	}
	if _, err := h.o.CoSign(ctx, "p1", true, ""); err == nil {
		t.Fatal("co-sign on a rejected payment must fail")
	}
}

func TestSupplyShortfallHolds(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Supply = fakeSupply{err: errors.New("no USDC on base")} })
	p, _ := h.o.Submit(context.Background(), pay("p1", 100))
	wantState(t, p, Held)
	if h.available(t) != 900 {
		t.Fatal("held payment keeps funds reserved")
	}
}

func TestBroadcastFailureThenReturn(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Broadcaster = fakeBroadcaster{err: errors.New("rpc down")} })
	p, _ := h.o.Submit(context.Background(), pay("p1", 100))
	wantState(t, p, Held)
	p, _ = h.o.Return("p1", "returned to payer")
	wantState(t, p, Returned)
	if h.available(t) != 1000 {
		t.Fatal("funds not returned")
	}
}

func TestOnFailedThenEscalate(t *testing.T) {
	h := newHarness(t, nil)
	h.o.Submit(context.Background(), pay("p1", 100))
	p, _ := h.o.OnFailed("p1", "reverted: policy period limit")
	wantState(t, p, Held)
	p, _ = h.o.Escalate("p1", "agent retried 3 times")
	wantState(t, p, Exception)
	if _, err := h.o.OnFailed("p1", "x"); err == nil {
		t.Fatal("OnFailed from Exception must be illegal")
	}
}

func TestConfirmationTimeout(t *testing.T) {
	h := newHarness(t, nil)
	h.o.Submit(context.Background(), pay("p1", 100))
	if moved := h.o.ExpireBroadcasts(time.Minute); len(moved) != 0 {
		t.Fatalf("expired too early: %v", moved)
	}
	h.clk.t = h.clk.t.Add(2 * time.Minute)
	if moved := h.o.ExpireBroadcasts(time.Minute); len(moved) != 1 {
		t.Fatalf("moved %v", moved)
	}
	p, _ := h.o.Get("p1")
	wantState(t, p, Held)
}

func TestConfirmationForWrongTx(t *testing.T) {
	h := newHarness(t, nil)
	h.o.Submit(context.Background(), pay("p1", 100))
	p, _ := h.o.OnConfirmed("p1", "0xother", nil)
	wantState(t, p, Held)
	if b, _ := h.l.Balance("agent:a"); b.Int64() != 1000 {
		t.Fatal("nothing should be posted for an unexpected tx")
	}
}

func TestReconcileBreak(t *testing.T) {
	h := newHarness(t, nil)
	p, _ := h.o.Submit(context.Background(), pay("p1", 100))
	h.o.OnConfirmed("p1", p.TxHash, nil)
	p, _ = h.o.Reconcile("p1", big.NewInt(99))
	wantState(t, p, Exception)
}

func TestReturnAfterPostReverses(t *testing.T) {
	h := newHarness(t, nil)
	p, _ := h.o.Submit(context.Background(), pay("p1", 100))
	h.o.OnConfirmed("p1", p.TxHash, nil)
	h.o.Reconcile("p1", nil) // break -> Exception
	p, err := h.o.Return("p1", "funds recovered from merchant")
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, p, Returned)
	if b, _ := h.l.Balance("agent:a"); b.Int64() != 1000 {
		t.Fatalf("reversal not applied: %s", b)
	}
}

func TestIllegalTransitionsRejected(t *testing.T) {
	h := newHarness(t, nil)
	p, _ := h.o.Submit(context.Background(), pay("p1", 100))
	if _, err := h.o.Reconcile("p1", big.NewInt(100)); err == nil {
		t.Fatal("reconcile before confirm must fail")
	}
	h.o.OnConfirmed("p1", p.TxHash, nil)
	if _, err := h.o.OnConfirmed("p1", p.TxHash, nil); err == nil {
		t.Fatal("double confirm must fail")
	}
	h.o.Reconcile("p1", big.NewInt(100))
	if _, err := h.o.Return("p1", ""); err == nil {
		t.Fatal("return of a settled payment must fail")
	}
	if _, err := h.o.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("want ErrNotFound")
	}
}

func TestTransitionTable(t *testing.T) {
	for from, tos := range transitions {
		for _, to := range tos {
			if from == to {
				t.Fatalf("self transition %s", from)
			}
		}
	}
	for _, s := range []State{Settled, Rejected, Returned} {
		if !s.Terminal() {
			t.Fatalf("%s should be terminal", s)
		}
	}
	if canTransition(Created, Signed) || canTransition(Validated, Signed) {
		t.Fatal("signing must not skip screening")
	}
}
