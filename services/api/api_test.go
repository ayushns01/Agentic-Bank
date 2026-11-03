package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/ayushns01/agentic-bank/services/api"
	"github.com/ayushns01/agentic-bank/services/bank"
	"github.com/ayushns01/agentic-bank/services/chain"
	"github.com/ayushns01/agentic-bank/services/internal/testchain"
)

func do(t *testing.T, srv http.Handler, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func paymentBody(e *testchain.Env, id string, amount int64) map[string]any {
	req := e.AgentRequest(id, amount, 0)
	return map[string]any{
		"id": req.ID, "account": req.Account.Hex(), "token": req.Token.Hex(), "to": req.To.Hex(),
		"amount": req.Amount.String(), "nonce": req.Nonce.String(), "deadline": req.Deadline,
		"sessionSig": hexutil.Encode(req.SessionSig),
	}
}

func TestPayFlowOverHTTP(t *testing.T) {
	e := testchain.Setup(t)
	srv := api.New(e.Bank)

	code, acct := do(t, srv, "GET", "/v1/accounts/"+e.Account.Hex(), nil)
	if code != 200 {
		t.Fatalf("get account: %d %v", code, acct)
	}
	bal := acct["balances"].([]any)[0].(map[string]any)
	if bal["available"] != "1000000000" {
		t.Fatalf("balances %v", bal)
	}

	code, p := do(t, srv, "POST", "/v1/payments", paymentBody(e, "inv-77", 25*testchain.USD))
	if code != 201 || p["state"] != "broadcast" {
		t.Fatalf("submit: %d %v", code, p)
	}
	if ref := bank.RefFor("inv-77"); p["ref"] != hexutil.Encode(ref[:]) {
		t.Fatalf("ref %v", p["ref"])
	}

	e.Sim.Commit()
	e.Sync()

	code, p = do(t, srv, "GET", "/v1/payments/inv-77", nil)
	if code != 200 || p["state"] != "settled" {
		t.Fatalf("get payment: %d %v", code, p)
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/accounts/"+e.Account.Hex()+"/statement?token="+e.Token.Hex(), nil))
	var st []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || len(st) != 2 {
		t.Fatalf("statement: %v %s", err, rec.Body.String())
	}
}

func TestRejectionAndCoSignOverHTTP(t *testing.T) {
	e := testchain.Setup(t)
	srv := api.New(e.Bank)

	code, p := do(t, srv, "POST", "/v1/payments", paymentBody(e, "too-big", 500*testchain.USD))
	if code != 422 || p["state"] != "rejected" {
		t.Fatalf("over-limit payment: %d %v", code, p)
	}

	code, p = do(t, srv, "POST", "/v1/payments", paymentBody(e, "needs-owner", 45*testchain.USD))
	if code != 202 || p["state"] != "awaiting_cosign" {
		t.Fatalf("co-sign payment: %d %v", code, p)
	}
	req := e.AgentRequest("needs-owner", 45*testchain.USD, 0)
	digest, err := chain.NewBank(e.Client, e.Engine).IntentDigest(context.Background(), e.Account, chain.PaymentIntent{
		Token: req.Token, To: req.To, Amount: req.Amount, Ref: bank.RefFor(req.ID), Nonce: req.Nonce, Deadline: req.Deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	code, p = do(t, srv, "POST", "/v1/payments/needs-owner/cosign",
		map[string]string{"ownerSig": hexutil.Encode(testchain.Sign(t, digest, e.OwnerKey))})
	if code != 200 || p["state"] != "broadcast" {
		t.Fatalf("cosign: %d %v", code, p)
	}
	// Co-signing again is a conflict, not a second broadcast.
	code, _ = do(t, srv, "POST", "/v1/payments/needs-owner/cosign",
		map[string]string{"ownerSig": hexutil.Encode(testchain.Sign(t, digest, e.OwnerKey))})
	if code != 409 {
		t.Fatalf("second cosign: %d", code)
	}
}

func TestBadRequests(t *testing.T) {
	e := testchain.Setup(t)
	srv := api.New(e.Bank)

	cases := []struct {
		name   string
		mutate func(map[string]any)
		want   int
	}{
		{"bad address", func(b map[string]any) { b["to"] = "0x123" }, 400},
		{"negative amount", func(b map[string]any) { b["amount"] = "-5" }, 400},
		{"bad signature hex", func(b map[string]any) { b["sessionSig"] = "zz" }, 400},
		{"unknown field", func(b map[string]any) { b["extra"] = true }, 400},
		{"unknown account", func(b map[string]any) { b["account"] = testchain.Merchant.Hex() }, 400},
	}
	for _, c := range cases {
		b := paymentBody(e, "p-"+c.name, 1)
		c.mutate(b)
		if code, out := do(t, srv, "POST", "/v1/payments", b); code != c.want {
			t.Errorf("%s: got %d %v", c.name, code, out)
		}
	}

	if code, _ := do(t, srv, "GET", "/v1/payments/nope", nil); code != 404 {
		t.Errorf("missing payment: %d", code)
	}
	if code, _ := do(t, srv, "GET", "/v1/accounts/not-an-address", nil); code != 400 {
		t.Errorf("bad account path: %d", code)
	}
	if code, _ := do(t, srv, "GET", "/v1/escrows/1", nil); code != 404 {
		t.Errorf("missing escrow: %d", code)
	}

	// Same id, different intent: conflict.
	do(t, srv, "POST", "/v1/payments", paymentBody(e, "dup", 1))
	b := paymentBody(e, "dup", 1)
	b["nonce"] = new(big.Int).SetInt64(9).String()
	if code, out := do(t, srv, "POST", "/v1/payments", b); code != 409 {
		t.Errorf("idempotency conflict: %d %v", code, out)
	}
}
