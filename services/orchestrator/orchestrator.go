package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ayushns01/agentic-bank/services/ledger"
)

// Payment is one agent payment intent and its lifecycle.
type Payment struct {
	ID           string // idempotency key, unique per payer
	Node         string // policy node (agent) that issued the intent
	PayerAccount string // ledger liability account of the paying agent
	Payee        string // on-chain recipient
	Asset        string // e.g. "USDC:base"
	Amount       *big.Int
	Rail         string // x402, mpp, transfer, cctp...

	State   State
	HoldID  string
	TxHash  string
	EntryID string
	History []Event
	Updated time.Time
}

// Event is one recorded state change.
type Event struct {
	From, To State
	Reason   string
	At       time.Time
}

// Verdict is a compliance screening outcome.
type Verdict int

const (
	Clear Verdict = iota
	Review
	Block
)

// Gate is a pre-funding check (authentication, instruction, address risk).
type Gate interface {
	Name() string
	Check(ctx context.Context, p *Payment) error
}

// Supply checks stablecoin inventory on the target chain (gate 5).
type Supply interface {
	Reserve(ctx context.Context, p *Payment) error
}

// Screener runs sanctions/AML screening (gate 6).
type Screener interface {
	Screen(ctx context.Context, p *Payment) (Verdict, string, error)
}

// Signer produces the session-key signature (gate 7). NeedsCoSign means the
// amount exceeds the node's co-sign threshold and an owner must approve.
type Signer interface {
	Sign(ctx context.Context, p *Payment) (needsCoSign bool, err error)
}

// Broadcaster submits the signed intent on-chain (gate 8).
type Broadcaster interface {
	Broadcast(ctx context.Context, p *Payment) (txHash string, err error)
}

// Config wires the orchestrator to the ledger and gate implementations.
type Config struct {
	Ledger *ledger.Ledger
	// Wallets maps an asset to the ledger asset account holding it on-chain.
	Wallets map[string]string
	// GasAccount is the expense account charged with network fees.
	GasAccount  string
	Gates       []Gate
	Supply      Supply
	Screener    Screener
	Signer      Signer
	Broadcaster Broadcaster
	Now         func() time.Time
}

var (
	ErrNotFound       = errors.New("orchestrator: payment not found")
	ErrIdempotency    = errors.New("orchestrator: payment id reused with different content")
	ErrInvalidPayment = errors.New("orchestrator: invalid payment")
)

// Orchestrator owns payment state. Methods are safe for concurrent use; each
// payment's transitions are serialized.
type Orchestrator struct {
	cfg      Config
	mu       sync.Mutex
	payments map[string]*Payment
}

func New(cfg Config) *Orchestrator {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Orchestrator{cfg: cfg, payments: map[string]*Payment{}}
}

// Get returns a copy of the payment.
func (o *Orchestrator) Get(id string) (Payment, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.payments[id]
	if !ok {
		return Payment{}, ErrNotFound
	}
	return clonePayment(p), nil
}

// Submit takes a new intent through gates 1-8. It returns the payment in the
// state it reached: Broadcast on the happy path, AwaitingCoSign, Rejected,
// Held or Exception otherwise. Submitting the same ID again returns the
// existing payment unchanged.
func (o *Orchestrator) Submit(ctx context.Context, in Payment) (Payment, error) {
	if in.ID == "" || in.PayerAccount == "" || in.Payee == "" || in.Asset == "" ||
		in.Amount == nil || in.Amount.Sign() <= 0 {
		return Payment{}, ErrInvalidPayment
	}
	if _, ok := o.cfg.Wallets[in.Asset]; !ok {
		return Payment{}, fmt.Errorf("%w: unsupported asset %s", ErrInvalidPayment, in.Asset)
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if p, ok := o.payments[in.ID]; ok {
		if !sameIntent(p, &in) {
			return Payment{}, ErrIdempotency
		}
		return clonePayment(p), nil
	}
	p := &Payment{
		ID: in.ID, Node: in.Node, PayerAccount: in.PayerAccount, Payee: in.Payee,
		Asset: in.Asset, Amount: new(big.Int).Set(in.Amount), Rail: in.Rail,
		State: Created, Updated: o.cfg.Now(),
	}
	o.payments[p.ID] = p

	// Gates 1-3: authentication, instruction, address & risk.
	for _, g := range o.cfg.Gates {
		if err := g.Check(ctx, p); err != nil {
			o.move(p, Rejected, g.Name()+": "+err.Error())
			return clonePayment(p), nil
		}
	}

	// Gate 4: funding. Reserve the amount in the ledger before anything else.
	p.HoldID = "hold:" + p.ID
	if err := o.cfg.Ledger.PlaceHold(ledger.Hold{ID: p.HoldID, Account: p.PayerAccount, Amount: p.Amount, Ref: p.ID}); err != nil {
		p.HoldID = ""
		o.move(p, Rejected, "funding: "+err.Error())
		return clonePayment(p), nil
	}
	o.move(p, Validated, "gates passed, funds held")

	// Gate 5: stablecoin supply on the target chain.
	if o.cfg.Supply != nil {
		if err := o.cfg.Supply.Reserve(ctx, p); err != nil {
			o.move(p, Held, "supply: "+err.Error())
			return clonePayment(p), nil
		}
	}

	// Gate 6: compliance, strictly before signing.
	o.move(p, Screening, "")
	if o.cfg.Screener != nil {
		v, why, err := o.cfg.Screener.Screen(ctx, p)
		switch {
		case err != nil:
			o.move(p, Exception, "screening error: "+err.Error())
			return clonePayment(p), nil
		case v == Block:
			o.releaseHold(p)
			o.move(p, Rejected, "screening: "+why)
			return clonePayment(p), nil
		case v == Review:
			o.move(p, Exception, "screening review: "+why)
			return clonePayment(p), nil
		}
	}

	// Gate 7: signing.
	needsCoSign, err := o.cfg.Signer.Sign(ctx, p)
	if err != nil {
		o.releaseHold(p)
		o.move(p, Rejected, "signing: "+err.Error())
		return clonePayment(p), nil
	}
	if needsCoSign {
		o.move(p, AwaitingCoSign, "amount above co-sign threshold")
		return clonePayment(p), nil
	}
	o.move(p, Signed, "")
	o.broadcast(ctx, p)
	return clonePayment(p), nil
}

// CoSign records the owner's decision on a payment awaiting co-signature.
func (o *Orchestrator) CoSign(ctx context.Context, id string, approve bool, reason string) (Payment, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.payments[id]
	if !ok {
		return Payment{}, ErrNotFound
	}
	if p.State != AwaitingCoSign {
		return clonePayment(p), &TransitionError{From: p.State, To: Signed}
	}
	if !approve {
		o.releaseHold(p)
		o.move(p, Rejected, "co-sign declined: "+reason)
		return clonePayment(p), nil
	}
	o.move(p, Signed, "co-signed: "+reason)
	o.broadcast(ctx, p)
	return clonePayment(p), nil
}

func (o *Orchestrator) broadcast(ctx context.Context, p *Payment) {
	tx, err := o.cfg.Broadcaster.Broadcast(ctx, p)
	if err != nil {
		o.move(p, Held, "broadcast: "+err.Error())
		return
	}
	p.TxHash = tx
	o.move(p, Broadcast, tx)
}

// OnConfirmed is called by the indexer once the transaction reaches the
// chain's finality threshold. It posts the ledger entry, consuming the hold:
// Dr payer, Cr wallet for the amount; Dr gas, Cr wallet for the network fee.
func (o *Orchestrator) OnConfirmed(id, txHash string, networkFee *big.Int) (Payment, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.payments[id]
	if !ok {
		return Payment{}, ErrNotFound
	}
	if p.State != Broadcast {
		return clonePayment(p), &TransitionError{From: p.State, To: Confirmed}
	}
	if txHash != p.TxHash {
		o.move(p, Held, fmt.Sprintf("confirmation for unexpected tx %s", txHash))
		return clonePayment(p), nil
	}
	wallet := o.cfg.Wallets[p.Asset]
	e := ledger.Entry{
		ID:    "pay:" + p.ID,
		Ref:   p.ID,
		Memo:  "payment to " + p.Payee + " via " + p.Rail,
		Holds: []string{p.HoldID},
		Lines: []ledger.Line{
			{Account: p.PayerAccount, Side: ledger.Debit, Amount: p.Amount},
			{Account: wallet, Side: ledger.Credit, Amount: p.Amount},
		},
	}
	if networkFee != nil && networkFee.Sign() > 0 {
		e.Lines = append(e.Lines,
			ledger.Line{Account: o.cfg.GasAccount, Side: ledger.Debit, Amount: networkFee},
			ledger.Line{Account: wallet, Side: ledger.Credit, Amount: networkFee},
		)
	}
	if err := o.cfg.Ledger.Post(e); err != nil {
		// Funds left on-chain but the books refused the entry: never silent.
		o.move(p, Confirmed, txHash)
		o.move(p, Exception, "ledger post failed: "+err.Error())
		return clonePayment(p), nil
	}
	p.EntryID = e.ID
	o.move(p, Confirmed, txHash)
	return clonePayment(p), nil
}

// OnFailed is called when the transaction reverted or was dropped.
func (o *Orchestrator) OnFailed(id, reason string) (Payment, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.payments[id]
	if !ok {
		return Payment{}, ErrNotFound
	}
	if !canTransition(p.State, Held) {
		return clonePayment(p), &TransitionError{From: p.State, To: Held}
	}
	o.move(p, Held, "execution failed: "+reason)
	return clonePayment(p), nil
}

// ExpireBroadcasts moves payments stuck in Broadcast longer than timeout to
// Held. Returns the IDs moved.
func (o *Orchestrator) ExpireBroadcasts(timeout time.Duration) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.cfg.Now()
	var moved []string
	for id, p := range o.payments {
		if p.State == Broadcast && now.Sub(p.Updated) > timeout {
			o.move(p, Held, "confirmation timeout")
			moved = append(moved, id)
		}
	}
	return moved
}

// Return releases a held or excepted payment's funds back to the payer.
// Only valid when nothing was posted.
func (o *Orchestrator) Return(id, reason string) (Payment, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.payments[id]
	if !ok {
		return Payment{}, ErrNotFound
	}
	if !canTransition(p.State, Returned) {
		return clonePayment(p), &TransitionError{From: p.State, To: Returned}
	}
	if p.EntryID != "" {
		if err := o.cfg.Ledger.Reverse(p.EntryID, "rev:"+p.ID, reason); err != nil {
			return clonePayment(p), err
		}
	}
	o.releaseHold(p)
	o.move(p, Returned, reason)
	return clonePayment(p), nil
}

// Escalate sends a held payment to manual review.
func (o *Orchestrator) Escalate(id, reason string) (Payment, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.payments[id]
	if !ok {
		return Payment{}, ErrNotFound
	}
	if !canTransition(p.State, Exception) {
		return clonePayment(p), &TransitionError{From: p.State, To: Exception}
	}
	o.move(p, Exception, reason)
	return clonePayment(p), nil
}

// Reconcile compares the observed on-chain transfer with the ledger posting.
// A match settles the payment; any break goes to Exception.
func (o *Orchestrator) Reconcile(id string, observed *big.Int) (Payment, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.payments[id]
	if !ok {
		return Payment{}, ErrNotFound
	}
	if p.State != Confirmed {
		return clonePayment(p), &TransitionError{From: p.State, To: Reconciled}
	}
	e, ok := o.cfg.Ledger.Entry(p.EntryID)
	if !ok {
		o.move(p, Exception, "break: no ledger entry")
		return clonePayment(p), nil
	}
	posted := e.Lines[0].Amount
	if observed == nil || posted.Cmp(observed) != 0 {
		o.move(p, Exception, fmt.Sprintf("break: ledger %s, chain %v", posted, observed))
		return clonePayment(p), nil
	}
	o.move(p, Reconciled, "ledger = chain")
	o.move(p, Settled, "")
	return clonePayment(p), nil
}

func (o *Orchestrator) releaseHold(p *Payment) {
	if p.HoldID != "" && o.cfg.Ledger.HoldActive(p.HoldID) {
		_ = o.cfg.Ledger.ReleaseHold(p.HoldID)
	}
}

// move applies a transition. Internal callers only request legal moves; an
// illegal one is a programming error.
func (o *Orchestrator) move(p *Payment, to State, reason string) {
	if !canTransition(p.State, to) {
		panic((&TransitionError{From: p.State, To: to}).Error())
	}
	now := o.cfg.Now()
	p.History = append(p.History, Event{From: p.State, To: to, Reason: reason, At: now})
	p.State = to
	p.Updated = now
}

func sameIntent(p, in *Payment) bool {
	return p.Node == in.Node && p.PayerAccount == in.PayerAccount && p.Payee == in.Payee &&
		p.Asset == in.Asset && p.Amount.Cmp(in.Amount) == 0 && p.Rail == in.Rail
}

func clonePayment(p *Payment) Payment {
	c := *p
	c.Amount = new(big.Int).Set(p.Amount)
	c.History = append([]Event(nil), p.History...)
	return c
}
