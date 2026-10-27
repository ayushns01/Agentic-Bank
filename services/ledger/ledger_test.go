package ledger

import (
	"errors"
	"math/big"
	"math/rand"
	"testing"
)

const usdc = "USDC:base"

func amt(v int64) *big.Int { return big.NewInt(v) }

// setup: a wallet asset account (on-chain USDC held by the bank), two agent
// liability accounts, a fee revenue account and an external funding source.
func setup(t *testing.T) *Ledger {
	t.Helper()
	l := New()
	for _, a := range []Account{
		{ID: "wallet", Type: Asset, Asset: usdc},
		{ID: "agent:a", Type: Liability, Asset: usdc},
		{ID: "agent:b", Type: Liability, Asset: usdc},
		{ID: "fees", Type: Revenue, Asset: usdc},
		{ID: "gas", Type: Expense, Asset: usdc},
		{ID: "eurc", Type: Asset, Asset: "EURC:base"},
	} {
		if err := l.CreateAccount(a); err != nil {
			t.Fatal(err)
		}
	}
	// Agent A deposits 1000: Dr wallet, Cr agent:a.
	mustPost(t, l, Entry{ID: "dep-1", Lines: []Line{
		{Account: "wallet", Side: Debit, Amount: amt(1000)},
		{Account: "agent:a", Side: Credit, Amount: amt(1000)},
	}})
	return l
}

func mustPost(t *testing.T, l *Ledger, e Entry) {
	t.Helper()
	if err := l.Post(e); err != nil {
		t.Fatalf("post %s: %v", e.ID, err)
	}
}

func bal(t *testing.T, l *Ledger, id string) int64 {
	t.Helper()
	b, err := l.Balance(id)
	if err != nil {
		t.Fatal(err)
	}
	return b.Int64()
}

func avail(t *testing.T, l *Ledger, id string) int64 {
	t.Helper()
	b, err := l.Available(id)
	if err != nil {
		t.Fatal(err)
	}
	return b.Int64()
}

func transfer(id string, from, to string, v int64, holds ...string) Entry {
	return Entry{ID: id, Lines: []Line{
		{Account: from, Side: Debit, Amount: amt(v)},
		{Account: to, Side: Credit, Amount: amt(v)},
	}, Holds: holds}
}

func TestDepositAndTransfer(t *testing.T) {
	l := setup(t)
	mustPost(t, l, transfer("p-1", "agent:a", "agent:b", 300))
	if got := bal(t, l, "agent:a"); got != 700 {
		t.Fatalf("agent:a = %d", got)
	}
	if got := bal(t, l, "agent:b"); got != 300 {
		t.Fatalf("agent:b = %d", got)
	}
	if got := bal(t, l, "wallet"); got != 1000 {
		t.Fatalf("wallet = %d", got)
	}
}

func TestUnbalancedRejected(t *testing.T) {
	l := setup(t)
	err := l.Post(Entry{ID: "bad", Lines: []Line{
		{Account: "agent:a", Side: Debit, Amount: amt(10)},
		{Account: "agent:b", Side: Credit, Amount: amt(9)},
	}})
	if !errors.Is(err, ErrUnbalanced) {
		t.Fatalf("want ErrUnbalanced, got %v", err)
	}
}

func TestCrossAssetMustBalancePerAsset(t *testing.T) {
	l := setup(t)
	// 10 USDC debit against 10 EURC credit: totals match but assets differ.
	err := l.Post(Entry{ID: "fx", Lines: []Line{
		{Account: "agent:a", Side: Debit, Amount: amt(10)},
		{Account: "eurc", Side: Credit, Amount: amt(10)},
	}})
	if !errors.Is(err, ErrUnbalanced) {
		t.Fatalf("want ErrUnbalanced, got %v", err)
	}
}

func TestInvalidAndUnknown(t *testing.T) {
	l := setup(t)
	if err := l.Post(transfer("z", "agent:a", "agent:b", 0)); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("zero amount: %v", err)
	}
	if err := l.Post(transfer("u", "agent:a", "nobody", 1)); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("unknown account: %v", err)
	}
	if err := l.Post(Entry{ID: "one", Lines: []Line{{Account: "agent:a", Side: Debit, Amount: amt(1)}}}); !errors.Is(err, ErrEmptyEntry) {
		t.Fatalf("single line: %v", err)
	}
	if err := l.CreateAccount(Account{ID: "agent:a", Type: Liability, Asset: usdc}); !errors.Is(err, ErrDuplicateAccount) {
		t.Fatalf("duplicate account: %v", err)
	}
}

func TestOverdraftBlocked(t *testing.T) {
	l := setup(t)
	if err := l.Post(transfer("p", "agent:a", "agent:b", 1001)); !errors.Is(err, ErrInsufficient) {
		t.Fatalf("want ErrInsufficient, got %v", err)
	}
	if got := bal(t, l, "agent:a"); got != 1000 {
		t.Fatalf("failed post must not change balances, agent:a = %d", got)
	}
}

func TestOverdraftAllowedForCreditLine(t *testing.T) {
	l := setup(t)
	if err := l.CreateAccount(Account{ID: "agent:c", Type: Liability, Asset: usdc, AllowOverdraft: true}); err != nil {
		t.Fatal(err)
	}
	mustPost(t, l, transfer("draw", "agent:c", "agent:b", 50))
	if got := bal(t, l, "agent:c"); got != -50 {
		t.Fatalf("agent:c = %d", got)
	}
}

func TestIdempotentPost(t *testing.T) {
	l := setup(t)
	mustPost(t, l, transfer("p", "agent:a", "agent:b", 100))
	mustPost(t, l, transfer("p", "agent:a", "agent:b", 100))
	if got := bal(t, l, "agent:b"); got != 100 {
		t.Fatalf("re-post applied twice: agent:b = %d", got)
	}
	if err := l.Post(transfer("p", "agent:a", "agent:b", 101)); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

func TestHoldReservesAndPostConsumes(t *testing.T) {
	l := setup(t)
	if err := l.PlaceHold(Hold{ID: "h1", Account: "agent:a", Amount: amt(800)}); err != nil {
		t.Fatal(err)
	}
	if got := avail(t, l, "agent:a"); got != 200 {
		t.Fatalf("available = %d", got)
	}
	// Another spend may not dip into held funds.
	if err := l.Post(transfer("other", "agent:a", "agent:b", 300)); !errors.Is(err, ErrInsufficient) {
		t.Fatalf("spend into held funds: %v", err)
	}
	// A second hold beyond available fails.
	if err := l.PlaceHold(Hold{ID: "h2", Account: "agent:a", Amount: amt(201)}); !errors.Is(err, ErrInsufficient) {
		t.Fatalf("over-hold: %v", err)
	}
	// Posting with the hold consumes it: 790 to merchant + 10 fee.
	mustPost(t, l, Entry{ID: "pay", Holds: []string{"h1"}, Lines: []Line{
		{Account: "agent:a", Side: Debit, Amount: amt(800)},
		{Account: "agent:b", Side: Credit, Amount: amt(790)},
		{Account: "fees", Side: Credit, Amount: amt(10)},
	}})
	if l.HoldActive("h1") {
		t.Fatal("hold should be consumed")
	}
	if got := avail(t, l, "agent:a"); got != 200 {
		t.Fatalf("available after = %d", got)
	}
	if got := bal(t, l, "fees"); got != 10 {
		t.Fatalf("fees = %d", got)
	}
}

func TestReleaseHold(t *testing.T) {
	l := setup(t)
	if err := l.PlaceHold(Hold{ID: "h", Account: "agent:a", Amount: amt(500)}); err != nil {
		t.Fatal(err)
	}
	if err := l.ReleaseHold("h"); err != nil {
		t.Fatal(err)
	}
	if got := avail(t, l, "agent:a"); got != 1000 {
		t.Fatalf("available = %d", got)
	}
	if err := l.ReleaseHold("h"); !errors.Is(err, ErrUnknownHold) {
		t.Fatalf("double release: %v", err)
	}
	if err := l.Post(transfer("p", "agent:a", "agent:b", 1, "h")); !errors.Is(err, ErrUnknownHold) {
		t.Fatalf("post with released hold: %v", err)
	}
}

func TestHoldIdempotent(t *testing.T) {
	l := setup(t)
	h := Hold{ID: "h", Account: "agent:a", Amount: amt(400)}
	if err := l.PlaceHold(h); err != nil {
		t.Fatal(err)
	}
	if err := l.PlaceHold(h); err != nil {
		t.Fatal(err)
	}
	if got := avail(t, l, "agent:a"); got != 600 {
		t.Fatalf("hold applied twice: available = %d", got)
	}
	if err := l.PlaceHold(Hold{ID: "h", Account: "agent:a", Amount: amt(1)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

func TestReverse(t *testing.T) {
	l := setup(t)
	mustPost(t, l, transfer("p", "agent:a", "agent:b", 250))
	if err := l.Reverse("p", "p-rev", "refund"); err != nil {
		t.Fatal(err)
	}
	if got := bal(t, l, "agent:a"); got != 1000 {
		t.Fatalf("agent:a = %d", got)
	}
	if err := l.Reverse("p", "p-rev", "refund"); err != nil {
		t.Fatalf("idempotent reverse: %v", err)
	}
	if err := l.Reverse("p", "p-rev2", "again"); !errors.Is(err, ErrAlreadyReversed) {
		t.Fatalf("want ErrAlreadyReversed, got %v", err)
	}
	if err := l.Reverse("nope", "x", ""); !errors.Is(err, ErrUnknownEntry) {
		t.Fatalf("want ErrUnknownEntry, got %v", err)
	}
	e, _ := l.Entry("p-rev")
	if e.Reverses != "p" {
		t.Fatalf("reverses = %q", e.Reverses)
	}
}

func TestReverseBlockedWhenPayeeSpent(t *testing.T) {
	l := setup(t)
	mustPost(t, l, transfer("p", "agent:a", "agent:b", 250))
	mustPost(t, l, transfer("p2", "agent:b", "fees", 200))
	if err := l.Reverse("p", "p-rev", ""); !errors.Is(err, ErrInsufficient) {
		t.Fatalf("want ErrInsufficient, got %v", err)
	}
}

func TestEntriesImmutable(t *testing.T) {
	l := setup(t)
	e := transfer("p", "agent:a", "agent:b", 5)
	mustPost(t, l, e)
	e.Lines[0].Amount.SetInt64(999)
	got, _ := l.Entry("p")
	if got.Lines[0].Amount.Int64() != 5 {
		t.Fatal("caller mutation leaked into ledger")
	}
	got.Lines[0].Amount.SetInt64(999)
	again, _ := l.Entry("p")
	if again.Lines[0].Amount.Int64() != 5 {
		t.Fatal("returned entry aliases ledger state")
	}
}

func TestEntriesByRef(t *testing.T) {
	l := setup(t)
	a := transfer("p1", "agent:a", "agent:b", 1)
	a.Ref = "pay-42"
	b := transfer("p2", "agent:a", "agent:b", 2)
	b.Ref = "pay-42"
	mustPost(t, l, a)
	mustPost(t, l, transfer("p3", "agent:a", "agent:b", 3))
	mustPost(t, l, b)
	got := l.EntriesByRef("pay-42")
	if len(got) != 2 || got[0].ID != "p1" || got[1].ID != "p2" {
		t.Fatalf("got %+v", got)
	}
}

// Randomized invariant test: whatever sequence of operations is attempted,
// the trial balance stays zero per asset and no non-overdraft account's
// available balance goes negative.
func TestInvariantsRandomized(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	l := setup(t)
	accts := []string{"wallet", "agent:a", "agent:b", "fees", "gas"}
	holds := []string{}
	for i := 0; i < 5000; i++ {
		switch r.Intn(4) {
		case 0, 1:
			from, to := accts[r.Intn(len(accts))], accts[r.Intn(len(accts))]
			if from == to {
				continue
			}
			var hs []string
			if len(holds) > 0 && r.Intn(3) == 0 {
				hs = []string{holds[r.Intn(len(holds))]}
			}
			_ = l.Post(transfer(idOf("e", i), from, to, int64(r.Intn(400)+1), hs...))
		case 2:
			id := idOf("h", i)
			if l.PlaceHold(Hold{ID: id, Account: accts[r.Intn(len(accts))], Amount: amt(int64(r.Intn(200) + 1))}) == nil {
				holds = append(holds, id)
			}
		case 3:
			if len(holds) > 0 {
				_ = l.ReleaseHold(holds[r.Intn(len(holds))])
			}
		}
		for asset, v := range l.TrialBalance() {
			if v.Sign() != 0 {
				t.Fatalf("step %d: trial balance for %s = %s", i, asset, v)
			}
		}
		for _, id := range accts {
			if a, _ := l.Available(id); a.Sign() < 0 {
				t.Fatalf("step %d: %s available = %s", i, id, a)
			}
		}
	}
}

func idOf(p string, i int) string { return p + "-" + big.NewInt(int64(i)).String() }
