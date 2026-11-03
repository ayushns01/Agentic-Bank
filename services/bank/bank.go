// Package bank wires the chain to the books. It is the indexer's sink (chain
// events become ledger postings), and it supplies the orchestrator's signing
// and broadcast gates (on-chain policy preflight, then the relayer).
//
// Books per AgentAccount and token:
//
//	wallet:<account>:<token>  asset      tokens the account holds on-chain
//	funds:<account>:<token>   liability  what the bank owes the account's owner
//
// A deposit is Dr wallet / Cr funds; a payment out is Dr funds / Cr wallet.
// The two always move together, so per account and token the books mirror
// the on-chain balance, and reconciliation can check exactly that.
package bank

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/ayushns01/agentic-bank/services/chain"
	"github.com/ayushns01/agentic-bank/services/ledger"
	"github.com/ayushns01/agentic-bank/services/orchestrator"
)

// Chain is what the bank needs from the contracts (chain.Bank satisfies it).
type Chain interface {
	IntentDigest(ctx context.Context, account common.Address, i chain.PaymentIntent) ([32]byte, error)
	SessionKey(ctx context.Context, account, key common.Address) (chain.SessionKey, error)
	Owner(ctx context.Context, account common.Address) (common.Address, error)
	WouldAuthorize(ctx context.Context, node [32]byte, token, to common.Address, amount *big.Int, coSigned bool) error
}

// Relayer submits signed intents (chain.Relayer satisfies it).
type Relayer interface {
	ExecutePayment(ctx context.Context, account common.Address, i chain.PaymentIntent, sessionSig, ownerSig []byte) (common.Hash, error)
}

// PaymentRequest is an agent's signed payment as received by the API.
type PaymentRequest struct {
	ID         string // idempotency key; the intent's ref must be RefFor(ID)
	Account    common.Address
	Token      common.Address
	To         common.Address
	Amount     *big.Int
	Nonce      *big.Int
	Deadline   uint64
	SessionSig []byte
	OwnerSig   []byte // optional
}

// AccountInfo is what the bank knows about an AgentAccount.
type AccountInfo struct {
	Address  common.Address
	Owner    common.Address
	OpenedAt uint64 // block
}

// Hold is the bank's view of an escrow hold.
type Hold struct {
	ID        string
	Payer     common.Address
	Payee     common.Address
	Token     common.Address
	Amount    *big.Int
	Remaining *big.Int
	Expiry    uint64
	Closed    bool
}

type pending struct {
	req    PaymentRequest
	intent chain.PaymentIntent
	node   [32]byte
}

type Bank struct {
	ledger *ledger.Ledger
	chain  Chain
	relay  Relayer
	orch   *orchestrator.Orchestrator

	mu       sync.Mutex
	accounts map[common.Address]*AccountInfo
	tokens   map[common.Address]bool
	pending  map[string]*pending // payment id -> chain intent
	refs     map[[32]byte]string // intent ref -> payment id
	holds    map[string]*Hold
	spent    map[string]*big.Int // "<account>:<node>:<token>" -> total paid by that agent
}

// New builds the bank and its orchestrator. Extra gates (address risk,
// screening) can be passed in cfg; Signer and Broadcaster are the bank's own.
func New(l *ledger.Ledger, c Chain, r Relayer, tokens []common.Address, cfg orchestrator.Config) *Bank {
	b := &Bank{
		ledger:   l,
		chain:    c,
		relay:    r,
		accounts: map[common.Address]*AccountInfo{},
		tokens:   map[common.Address]bool{},
		pending:  map[string]*pending{},
		refs:     map[[32]byte]string{},
		holds:    map[string]*Hold{},
		spent:    map[string]*big.Int{},
	}
	for _, t := range tokens {
		b.tokens[t] = true
	}
	cfg.Ledger = l
	cfg.Signer = signer{b}
	cfg.Broadcaster = broadcaster{b}
	b.orch = orchestrator.New(cfg)
	return b
}

func (b *Bank) Orchestrator() *orchestrator.Orchestrator { return b.orch }
func (b *Bank) Ledger() *ledger.Ledger                   { return b.ledger }

// RefFor is the intent ref an agent must sign for payment id.
func RefFor(id string) [32]byte { return crypto.Keccak256Hash([]byte(id)) }

func Asset(token common.Address) string { return strings.ToLower(token.Hex()) }

func WalletAccount(account, token common.Address) string {
	return "wallet:" + strings.ToLower(account.Hex()) + ":" + Asset(token)
}

func FundsAccount(account, token common.Address) string {
	return "funds:" + strings.ToLower(account.Hex()) + ":" + Asset(token)
}

// ensureBooks creates the account's ledger accounts for token if missing.
func (b *Bank) ensureBooks(account, token common.Address) error {
	for _, a := range []ledger.Account{
		{ID: WalletAccount(account, token), Type: ledger.Asset, Asset: Asset(token)},
		{ID: FundsAccount(account, token), Type: ledger.Liability, Asset: Asset(token)},
	} {
		if b.ledger.HasAccount(a.ID) {
			continue
		}
		if err := b.ledger.CreateAccount(a); err != nil && !errors.Is(err, ledger.ErrDuplicateAccount) {
			return err
		}
	}
	return nil
}

// --------------------------------------------------------------- payments

var (
	ErrUnknownAccount = errors.New("bank: unknown account")
	ErrUnknownToken   = errors.New("bank: token not supported")
	ErrBadRequest     = errors.New("bank: bad request")
)

// SubmitPayment validates an agent's signed intent and runs it through the
// orchestrator's gates. Re-submitting the same ID is idempotent.
func (b *Bank) SubmitPayment(ctx context.Context, req PaymentRequest) (orchestrator.Payment, error) {
	if req.ID == "" || req.Amount == nil || req.Amount.Sign() <= 0 || req.Nonce == nil || len(req.SessionSig) != 65 {
		return orchestrator.Payment{}, ErrBadRequest
	}
	b.mu.Lock()
	_, known := b.accounts[req.Account]
	supported := b.tokens[req.Token]
	if p, ok := b.pending[req.ID]; ok && (p.req.Account != req.Account || p.req.Nonce.Cmp(req.Nonce) != 0) {
		b.mu.Unlock()
		return orchestrator.Payment{}, orchestrator.ErrIdempotency
	}
	b.mu.Unlock()
	if !known {
		return orchestrator.Payment{}, fmt.Errorf("%w: %s", ErrUnknownAccount, req.Account.Hex())
	}
	if !supported {
		return orchestrator.Payment{}, fmt.Errorf("%w: %s", ErrUnknownToken, req.Token.Hex())
	}
	if err := b.ensureBooks(req.Account, req.Token); err != nil {
		return orchestrator.Payment{}, err
	}

	intent := chain.PaymentIntent{
		Token: req.Token, To: req.To, Amount: req.Amount, Ref: RefFor(req.ID),
		Nonce: req.Nonce, Deadline: req.Deadline,
	}
	b.mu.Lock()
	if _, ok := b.pending[req.ID]; !ok {
		b.pending[req.ID] = &pending{req: req, intent: intent}
		b.refs[intent.Ref] = req.ID
	}
	b.mu.Unlock()

	return b.orch.Submit(ctx, orchestrator.Payment{
		ID:            req.ID,
		PayerAccount:  FundsAccount(req.Account, req.Token),
		WalletAccount: WalletAccount(req.Account, req.Token),
		Payee:         strings.ToLower(req.To.Hex()),
		Asset:         Asset(req.Token),
		Amount:        req.Amount,
		Rail:          "transfer",
	})
}

// CoSign attaches the owner's co-signature to a payment awaiting it and
// releases it for broadcast.
func (b *Bank) CoSign(ctx context.Context, id string, ownerSig []byte) (orchestrator.Payment, error) {
	b.mu.Lock()
	p, ok := b.pending[id]
	b.mu.Unlock()
	if !ok {
		return orchestrator.Payment{}, orchestrator.ErrNotFound
	}
	if err := b.verifyOwnerSig(ctx, p, ownerSig); err != nil {
		return orchestrator.Payment{}, err
	}
	b.mu.Lock()
	p.req.OwnerSig = ownerSig
	b.mu.Unlock()
	return b.orch.CoSign(ctx, id, true, "owner co-signature verified")
}

// Decline rejects a payment awaiting co-signature.
func (b *Bank) Decline(ctx context.Context, id, reason string) (orchestrator.Payment, error) {
	return b.orch.CoSign(ctx, id, false, reason)
}

func (b *Bank) verifyOwnerSig(ctx context.Context, p *pending, sig []byte) error {
	digest, err := b.chain.IntentDigest(ctx, p.req.Account, p.intent)
	if err != nil {
		return err
	}
	owner, err := b.chain.Owner(ctx, p.req.Account)
	if err != nil {
		return err
	}
	signer, err := chain.RecoverSigner(digest, sig)
	if err != nil {
		return fmt.Errorf("%w: owner signature: %v", ErrBadRequest, err)
	}
	if signer != owner {
		return fmt.Errorf("%w: owner signature is from %s, owner is %s", ErrBadRequest, signer.Hex(), owner.Hex())
	}
	return nil
}

// signer is gate 7: verify the agent's signature and session key, then dry-run
// the on-chain policy so a payment that would revert never gets broadcast.
type signer struct{ b *Bank }

func (s signer) Sign(ctx context.Context, op *orchestrator.Payment) (bool, error) {
	b := s.b
	b.mu.Lock()
	p, ok := b.pending[op.ID]
	b.mu.Unlock()
	if !ok {
		return false, errors.New("no signed intent")
	}
	digest, err := b.chain.IntentDigest(ctx, p.req.Account, p.intent)
	if err != nil {
		return false, err
	}
	key, err := chain.RecoverSigner(digest, p.req.SessionSig)
	if err != nil {
		return false, err
	}
	sk, err := b.chain.SessionKey(ctx, p.req.Account, key)
	if err != nil {
		return false, err
	}
	if !sk.Active {
		return false, fmt.Errorf("session key %s is not active", key.Hex())
	}
	if sk.ValidUntil < uint64(time.Now().Unix()) {
		return false, fmt.Errorf("session key %s expired", key.Hex())
	}
	b.mu.Lock()
	p.node = sk.Node
	ownerSig := p.req.OwnerSig
	b.mu.Unlock()

	coSigned := len(ownerSig) > 0
	if coSigned {
		if err := b.verifyOwnerSig(ctx, p, ownerSig); err != nil {
			return false, err
		}
	}
	err = b.chain.WouldAuthorize(ctx, sk.Node, p.intent.Token, p.intent.To, p.intent.Amount, coSigned)
	switch {
	case err == nil:
		return false, nil
	case chain.IsPolicyError(err, "CoSignRequired") && !coSigned:
		return true, nil
	default:
		return false, err
	}
}

// broadcaster is gate 8: relay the signed intent.
type broadcaster struct{ b *Bank }

func (x broadcaster) Broadcast(ctx context.Context, op *orchestrator.Payment) (string, error) {
	b := x.b
	b.mu.Lock()
	p, ok := b.pending[op.ID]
	var ownerSig []byte
	if ok {
		ownerSig = p.req.OwnerSig
	}
	b.mu.Unlock()
	if !ok {
		return "", errors.New("no signed intent")
	}
	tx, err := b.relay.ExecutePayment(ctx, p.req.Account, p.intent, p.req.SessionSig, ownerSig)
	if err != nil {
		return "", err
	}
	return tx.Hex(), nil
}

// -------------------------------------------------------------- the sink

// Handle applies one confirmed block range. It is idempotent: ledger entries
// are keyed by tx hash and log index.
func (b *Bank) Handle(ctx context.Context, from, to uint64, events []chain.Event) error {
	// Transfers that settle an orchestrated payment are posted by the
	// orchestrator (consuming its hold), not again here.
	settled := map[string]bool{}
	for _, ev := range events {
		pe, ok := ev.(chain.PaymentExecuted)
		if !ok {
			continue
		}
		b.recordSpend(pe)
		b.mu.Lock()
		id, orchestrated := b.refs[pe.Ref]
		b.mu.Unlock()
		if !orchestrated {
			continue
		}
		handled, err := b.settle(id, pe)
		if err != nil {
			return err
		}
		if handled {
			settled[transferKey(pe.TxHash, pe.Token, pe.Address, pe.To, pe.Amount)] = true
		}
	}

	for _, ev := range events {
		switch e := ev.(type) {
		case chain.AccountOpened:
			b.mu.Lock()
			if _, ok := b.accounts[e.Account]; !ok {
				b.accounts[e.Account] = &AccountInfo{Address: e.Account, Owner: e.Owner, OpenedAt: e.Block}
			}
			b.mu.Unlock()
		case chain.Transfer:
			k := transferKey(e.TxHash, e.Address, e.From, e.To, e.Amount)
			if settled[k] {
				delete(settled, k) // consume once: identical transfers in one tx stay distinct
				continue
			}
			if err := b.postTransfer(e); err != nil {
				return err
			}
		case chain.EscrowOpened:
			b.mu.Lock()
			b.holds[e.ID.String()] = &Hold{ID: e.ID.String(), Payer: e.Payer, Payee: e.Payee, Token: e.Token,
				Amount: e.Amount, Remaining: new(big.Int).Set(e.Amount), Expiry: e.Expiry}
			b.mu.Unlock()
		case chain.EscrowCaptured:
			b.updateHold(e.ID, func(h *Hold) { h.Remaining = e.Remaining })
		case chain.EscrowRefunded, chain.EscrowResolved:
			id := escrowID(e)
			b.updateHold(id, func(h *Hold) { h.Remaining = new(big.Int); h.Closed = true })
		}
	}
	return nil
}

func escrowID(ev chain.Event) *big.Int {
	switch e := ev.(type) {
	case chain.EscrowRefunded:
		return e.ID
	case chain.EscrowResolved:
		return e.ID
	}
	return nil
}

func (b *Bank) updateHold(id *big.Int, f func(*Hold)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if h, ok := b.holds[id.String()]; ok {
		f(h)
		if h.Remaining.Sign() == 0 {
			h.Closed = true
		}
	}
}

// settle confirms and reconciles an orchestrated payment from its on-chain
// event. It reports whether the payment's transfer is now on the books via
// the orchestrator. A payment the bank had already given up on (Held after a
// timeout) but which executed anyway is not: its transfer is booked like any
// other, and the payment goes to manual review because its hold is still
// reserved.
func (b *Bank) settle(id string, pe chain.PaymentExecuted) (bool, error) {
	p, err := b.orch.Get(id)
	if err != nil {
		return false, nil // ref of a payment this instance never saw
	}
	if p.State == orchestrator.Broadcast {
		if p, err = b.orch.OnConfirmed(id, pe.TxHash.Hex(), nil); err != nil {
			return false, err
		}
	}
	if p.State == orchestrator.Confirmed {
		if p, err = b.orch.Reconcile(id, pe.Amount); err != nil {
			return false, err
		}
	}
	switch p.State {
	case orchestrator.Settled, orchestrator.Exception:
		return p.EntryID != "", nil
	case orchestrator.Held:
		_, err := b.orch.Escalate(id, "executed on-chain after it was held: "+pe.TxHash.Hex())
		return false, err
	}
	return false, nil
}

func (b *Bank) postTransfer(e chain.Transfer) error {
	b.mu.Lock()
	_, fromBank := b.accounts[e.From]
	_, toBank := b.accounts[e.To]
	b.mu.Unlock()
	if !fromBank && !toBank {
		return nil
	}
	if e.Amount.Sign() == 0 {
		return nil
	}
	entry := ledger.Entry{ID: "tx:" + e.ID(), Ref: e.TxHash.Hex()}
	if fromBank {
		if err := b.ensureBooks(e.From, e.Address); err != nil {
			return err
		}
		entry.Lines = append(entry.Lines,
			ledger.Line{Account: FundsAccount(e.From, e.Address), Side: ledger.Debit, Amount: e.Amount},
			ledger.Line{Account: WalletAccount(e.From, e.Address), Side: ledger.Credit, Amount: e.Amount})
	}
	if toBank {
		if err := b.ensureBooks(e.To, e.Address); err != nil {
			return err
		}
		entry.Lines = append(entry.Lines,
			ledger.Line{Account: WalletAccount(e.To, e.Address), Side: ledger.Debit, Amount: e.Amount},
			ledger.Line{Account: FundsAccount(e.To, e.Address), Side: ledger.Credit, Amount: e.Amount})
	}
	switch {
	case fromBank && toBank:
		entry.Memo = "internal transfer"
	case fromBank:
		entry.Memo = "payment to " + e.To.Hex()
	default:
		entry.Memo = "deposit from " + e.From.Hex()
	}
	return b.ledger.Post(entry)
}

func (b *Bank) recordSpend(pe chain.PaymentExecuted) {
	k := fmt.Sprintf("%s:%x:%s", strings.ToLower(pe.Address.Hex()), pe.Node, Asset(pe.Token))
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.spent[k] == nil {
		b.spent[k] = new(big.Int)
	}
	b.spent[k].Add(b.spent[k], pe.Amount)
}

func transferKey(tx common.Hash, token, from, to common.Address, amount *big.Int) string {
	return tx.Hex() + token.Hex() + from.Hex() + to.Hex() + amount.String()
}

// ----------------------------------------------------------------- views

func (b *Bank) Account(addr common.Address) (AccountInfo, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a, ok := b.accounts[addr]
	if !ok {
		return AccountInfo{}, false
	}
	return *a, true
}

func (b *Bank) Accounts() []AccountInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]AccountInfo, 0, len(b.accounts))
	for _, a := range b.accounts {
		out = append(out, *a)
	}
	return out
}

// Balance is one token position of an account in the bank's books.
type Balance struct {
	Token     common.Address
	OnChain   *big.Int // wallet asset account
	Owed      *big.Int // funds liability account
	Available *big.Int // owed minus holds for in-flight payments
}

func (b *Bank) Balances(addr common.Address) []Balance {
	b.mu.Lock()
	tokens := make([]common.Address, 0, len(b.tokens))
	for t := range b.tokens {
		tokens = append(tokens, t)
	}
	b.mu.Unlock()
	var out []Balance
	for _, t := range tokens {
		w, err1 := b.ledger.Balance(WalletAccount(addr, t))
		f, err2 := b.ledger.Balance(FundsAccount(addr, t))
		a, err3 := b.ledger.Available(FundsAccount(addr, t))
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		out = append(out, Balance{Token: t, OnChain: w, Owed: f, Available: a})
	}
	return out
}

func (b *Bank) Statement(addr, token common.Address) []ledger.Entry {
	return b.ledger.EntriesForAccount(FundsAccount(addr, token))
}

func (b *Bank) Hold(id string) (Hold, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h, ok := b.holds[id]
	if !ok {
		return Hold{}, false
	}
	return *h, true
}

// AgentSpend returns how much an agent (policy node) has paid out of an
// account in token, according to on-chain PaymentExecuted events.
func (b *Bank) AgentSpend(account common.Address, node [32]byte, token common.Address) *big.Int {
	k := fmt.Sprintf("%s:%x:%s", strings.ToLower(account.Hex()), node, Asset(token))
	b.mu.Lock()
	defer b.mu.Unlock()
	if v, ok := b.spent[k]; ok {
		return new(big.Int).Set(v)
	}
	return new(big.Int)
}
