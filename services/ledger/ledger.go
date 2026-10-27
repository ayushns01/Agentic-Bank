// Package ledger is the double-entry core ledger. Every value movement in the
// bank (payments, fees, holds, reversals, instrument settlements) is recorded
// as an immutable, balanced journal entry.
//
// Invariants enforced on every write:
//   - each entry balances per asset (sum of debits == sum of credits);
//   - an account's available balance (balance minus active holds) never goes
//     negative unless the account allows overdraft;
//   - entries are immutable; corrections are reversals;
//   - posting is idempotent by entry ID.
package ledger

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
)

// AccountType determines an account's normal balance side.
type AccountType int

const (
	Asset AccountType = iota
	Liability
	Equity
	Revenue
	Expense
)

func (t AccountType) String() string {
	return [...]string{"asset", "liability", "equity", "revenue", "expense"}[t]
}

// debitNormal reports whether the account's balance increases with debits.
func (t AccountType) debitNormal() bool { return t == Asset || t == Expense }

// Side is the side of a journal line.
type Side int

const (
	Debit Side = iota
	Credit
)

// Account is a ledger account denominated in a single asset (e.g. "USDC:base").
type Account struct {
	ID             string
	Type           AccountType
	Asset          string
	AllowOverdraft bool
}

// Line is one leg of a journal entry.
type Line struct {
	Account string
	Side    Side
	Amount  *big.Int
}

// Entry is a balanced journal entry. Holds lists the holds this entry
// consumes; they are released atomically with the posting.
type Entry struct {
	ID       string
	Ref      string // payment / instrument reference
	Memo     string
	Lines    []Line
	Holds    []string
	Reverses string // ID of the entry this one reverses, if any
}

// Hold reserves part of an account's available balance without posting.
type Hold struct {
	ID      string
	Account string
	Amount  *big.Int
	Ref     string
}

var (
	ErrUnknownAccount   = errors.New("ledger: unknown account")
	ErrDuplicateAccount = errors.New("ledger: account already exists")
	ErrUnbalanced       = errors.New("ledger: entry does not balance")
	ErrInvalidAmount    = errors.New("ledger: amount must be positive")
	ErrEmptyEntry       = errors.New("ledger: entry needs at least two lines")
	ErrInsufficient     = errors.New("ledger: insufficient available balance")
	ErrConflict         = errors.New("ledger: id reused with different content")
	ErrUnknownHold      = errors.New("ledger: unknown or inactive hold")
	ErrUnknownEntry     = errors.New("ledger: unknown entry")
	ErrAlreadyReversed  = errors.New("ledger: entry already reversed")
)

// Ledger is an in-memory implementation. Persistence (Postgres) sits behind
// the same API in a later phase; the invariants live here.
type Ledger struct {
	mu       sync.Mutex
	accounts map[string]Account
	balances map[string]*big.Int // in normal-balance terms
	held     map[string]*big.Int // sum of active holds per account
	holds    map[string]Hold     // active holds
	entries  map[string]Entry
	order    []string // entry IDs in posting order
	reversed map[string]string
}

func New() *Ledger {
	return &Ledger{
		accounts: map[string]Account{},
		balances: map[string]*big.Int{},
		held:     map[string]*big.Int{},
		holds:    map[string]Hold{},
		entries:  map[string]Entry{},
		reversed: map[string]string{},
	}
}

func (l *Ledger) CreateAccount(a Account) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.accounts[a.ID]; ok {
		return fmt.Errorf("%w: %s", ErrDuplicateAccount, a.ID)
	}
	l.accounts[a.ID] = a
	l.balances[a.ID] = new(big.Int)
	l.held[a.ID] = new(big.Int)
	return nil
}

// Balance returns the account balance in normal-balance terms.
func (l *Ledger) Balance(id string) (*big.Int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.balances[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownAccount, id)
	}
	return new(big.Int).Set(b), nil
}

// Available returns balance minus active holds.
func (l *Ledger) Available(id string) (*big.Int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.balances[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownAccount, id)
	}
	return new(big.Int).Sub(b, l.held[id]), nil
}

// PlaceHold reserves amount on an account. Idempotent by hold ID.
func (l *Ledger) PlaceHold(h Hold) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if h.Amount == nil || h.Amount.Sign() <= 0 {
		return ErrInvalidAmount
	}
	a, ok := l.accounts[h.Account]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownAccount, h.Account)
	}
	if prev, ok := l.holds[h.ID]; ok {
		if prev.Account == h.Account && prev.Amount.Cmp(h.Amount) == 0 {
			return nil
		}
		return fmt.Errorf("%w: hold %s", ErrConflict, h.ID)
	}
	avail := new(big.Int).Sub(l.balances[h.Account], l.held[h.Account])
	if !a.AllowOverdraft && avail.Cmp(h.Amount) < 0 {
		return fmt.Errorf("%w: %s has %s, hold needs %s", ErrInsufficient, h.Account, avail, h.Amount)
	}
	h.Amount = new(big.Int).Set(h.Amount)
	l.holds[h.ID] = h
	l.held[h.Account].Add(l.held[h.Account], h.Amount)
	return nil
}

// ReleaseHold drops an active hold without posting.
func (l *Ledger) ReleaseHold(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.releaseLocked(id)
}

func (l *Ledger) releaseLocked(id string) error {
	h, ok := l.holds[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownHold, id)
	}
	l.held[h.Account].Sub(l.held[h.Account], h.Amount)
	delete(l.holds, id)
	return nil
}

// HoldActive reports whether a hold is still reserved.
func (l *Ledger) HoldActive(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.holds[id]
	return ok
}

// Post validates and applies an entry atomically. Re-posting an identical
// entry ID is a no-op; reusing an ID with different content is ErrConflict.
func (l *Ledger) Post(e Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.postLocked(e)
}

func (l *Ledger) postLocked(e Entry) error {
	if prev, ok := l.entries[e.ID]; ok {
		if sameEntry(prev, e) {
			return nil
		}
		return fmt.Errorf("%w: entry %s", ErrConflict, e.ID)
	}
	if len(e.Lines) < 2 {
		return ErrEmptyEntry
	}

	// Per-asset balance check and per-account deltas (normal-balance terms).
	perAsset := map[string]*big.Int{}
	deltas := map[string]*big.Int{}
	for _, ln := range e.Lines {
		if ln.Amount == nil || ln.Amount.Sign() <= 0 {
			return ErrInvalidAmount
		}
		a, ok := l.accounts[ln.Account]
		if !ok {
			return fmt.Errorf("%w: %s", ErrUnknownAccount, ln.Account)
		}
		net, ok := perAsset[a.Asset]
		if !ok {
			net = new(big.Int)
			perAsset[a.Asset] = net
		}
		if ln.Side == Debit {
			net.Add(net, ln.Amount)
		} else {
			net.Sub(net, ln.Amount)
		}
		d, ok := deltas[ln.Account]
		if !ok {
			d = new(big.Int)
			deltas[ln.Account] = d
		}
		if (ln.Side == Debit) == a.Type.debitNormal() {
			d.Add(d, ln.Amount)
		} else {
			d.Sub(d, ln.Amount)
		}
	}
	for asset, net := range perAsset {
		if net.Sign() != 0 {
			return fmt.Errorf("%w: asset %s off by %s", ErrUnbalanced, asset, net)
		}
	}

	// Holds consumed by this entry are released as part of the posting, so
	// the funds they reserved become spendable by exactly this entry.
	heldAfter := map[string]*big.Int{}
	for _, hid := range e.Holds {
		h, ok := l.holds[hid]
		if !ok {
			return fmt.Errorf("%w: %s", ErrUnknownHold, hid)
		}
		cur, ok := heldAfter[h.Account]
		if !ok {
			cur = new(big.Int).Set(l.held[h.Account])
			heldAfter[h.Account] = cur
		}
		cur.Sub(cur, h.Amount)
	}

	// Overdraft check against post-entry available balance.
	accts := make([]string, 0, len(deltas))
	for id := range deltas {
		accts = append(accts, id)
	}
	sort.Strings(accts)
	for _, id := range accts {
		a := l.accounts[id]
		if a.AllowOverdraft {
			continue
		}
		held := l.held[id]
		if h, ok := heldAfter[id]; ok {
			held = h
		}
		after := new(big.Int).Add(l.balances[id], deltas[id])
		after.Sub(after, held)
		if after.Sign() < 0 {
			return fmt.Errorf("%w: %s would go to %s", ErrInsufficient, id, after)
		}
	}

	// Apply.
	for _, hid := range e.Holds {
		_ = l.releaseLocked(hid)
	}
	for id, d := range deltas {
		l.balances[id].Add(l.balances[id], d)
	}
	l.entries[e.ID] = cloneEntry(e)
	l.order = append(l.order, e.ID)
	return nil
}

// Reverse posts a mirror of entry id under newID.
func (l *Ledger) Reverse(id, newID, memo string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	orig, ok := l.entries[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownEntry, id)
	}
	if by, ok := l.reversed[id]; ok {
		if by == newID {
			return nil
		}
		return fmt.Errorf("%w: %s by %s", ErrAlreadyReversed, id, by)
	}
	rev := Entry{ID: newID, Ref: orig.Ref, Memo: memo, Reverses: id}
	for _, ln := range orig.Lines {
		s := Debit
		if ln.Side == Debit {
			s = Credit
		}
		rev.Lines = append(rev.Lines, Line{Account: ln.Account, Side: s, Amount: new(big.Int).Set(ln.Amount)})
	}
	if err := l.postLocked(rev); err != nil {
		return err
	}
	l.reversed[id] = newID
	return nil
}

// Entry returns a posted entry.
func (l *Ledger) Entry(id string) (Entry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[id]
	if !ok {
		return Entry{}, false
	}
	return cloneEntry(e), true
}

// EntriesByRef returns entries tagged with ref, in posting order.
func (l *Ledger) EntriesByRef(ref string) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Entry
	for _, id := range l.order {
		if e := l.entries[id]; e.Ref == ref {
			out = append(out, cloneEntry(e))
		}
	}
	return out
}

// TrialBalance returns, per asset, total debits minus total credits across
// all accounts in raw (not normal-balance) terms. Always zero for every asset.
func (l *Ledger) TrialBalance() map[string]*big.Int {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]*big.Int{}
	for id, b := range l.balances {
		a := l.accounts[id]
		v, ok := out[a.Asset]
		if !ok {
			v = new(big.Int)
			out[a.Asset] = v
		}
		if a.Type.debitNormal() {
			v.Add(v, b)
		} else {
			v.Sub(v, b)
		}
	}
	return out
}

func sameEntry(a, b Entry) bool {
	if a.Ref != b.Ref || a.Reverses != b.Reverses || len(a.Lines) != len(b.Lines) || len(a.Holds) != len(b.Holds) {
		return false
	}
	for i := range a.Lines {
		x, y := a.Lines[i], b.Lines[i]
		if x.Account != y.Account || x.Side != y.Side || x.Amount.Cmp(y.Amount) != 0 {
			return false
		}
	}
	for i := range a.Holds {
		if a.Holds[i] != b.Holds[i] {
			return false
		}
	}
	return true
}

func cloneEntry(e Entry) Entry {
	c := e
	c.Lines = make([]Line, len(e.Lines))
	for i, ln := range e.Lines {
		c.Lines[i] = Line{Account: ln.Account, Side: ln.Side, Amount: new(big.Int).Set(ln.Amount)}
	}
	c.Holds = append([]string(nil), e.Holds...)
	return c
}
