// Package api is the bank's REST gateway for agents and operators.
//
//	GET  /healthz
//	GET  /v1/accounts
//	GET  /v1/accounts/{address}                 account + balances
//	GET  /v1/accounts/{address}/statement?token= ledger entries
//	POST /v1/payments                           submit a signed intent
//	GET  /v1/payments/{id}
//	POST /v1/payments/{id}/cosign               {"ownerSig": "0x..."}
//	POST /v1/payments/{id}/decline              {"reason": "..."}
//	GET  /v1/escrows/{id}
//
// Amounts are decimal strings in token base units; addresses and signatures
// are 0x-hex.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/ayushns01/agentic-bank/services/bank"
	"github.com/ayushns01/agentic-bank/services/ledger"
	"github.com/ayushns01/agentic-bank/services/orchestrator"
)

type Server struct {
	bank *bank.Bank
	mux  *http.ServeMux
}

func New(b *bank.Bank) *Server {
	s := &Server{bank: b, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	s.mux.HandleFunc("GET /v1/accounts", s.listAccounts)
	s.mux.HandleFunc("GET /v1/accounts/{address}", s.getAccount)
	s.mux.HandleFunc("GET /v1/accounts/{address}/statement", s.statement)
	s.mux.HandleFunc("POST /v1/payments", s.submitPayment)
	s.mux.HandleFunc("GET /v1/payments/{id}", s.getPayment)
	s.mux.HandleFunc("POST /v1/payments/{id}/cosign", s.cosign)
	s.mux.HandleFunc("POST /v1/payments/{id}/decline", s.decline)
	s.mux.HandleFunc("GET /v1/escrows/{id}", s.getEscrow)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// ------------------------------------------------------------------ views

type balanceView struct {
	Token     string `json:"token"`
	OnChain   string `json:"onChain"`
	Owed      string `json:"owed"`
	Available string `json:"available"`
}

type accountView struct {
	Address  string        `json:"address"`
	Owner    string        `json:"owner"`
	OpenedAt uint64        `json:"openedAtBlock"`
	Balances []balanceView `json:"balances,omitempty"`
}

type lineView struct {
	Account string `json:"account"`
	Side    string `json:"side"`
	Amount  string `json:"amount"`
}

type entryView struct {
	ID       string     `json:"id"`
	Ref      string     `json:"ref,omitempty"`
	Memo     string     `json:"memo,omitempty"`
	Reverses string     `json:"reverses,omitempty"`
	Lines    []lineView `json:"lines"`
}

type eventView struct {
	From   string    `json:"from"`
	To     string    `json:"to"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

type paymentView struct {
	ID      string      `json:"id"`
	State   string      `json:"state"`
	Payee   string      `json:"payee"`
	Asset   string      `json:"asset"`
	Amount  string      `json:"amount"`
	TxHash  string      `json:"txHash,omitempty"`
	Ref     string      `json:"ref"`
	History []eventView `json:"history"`
}

func viewAccount(a bank.AccountInfo, bals []bank.Balance) accountView {
	v := accountView{Address: a.Address.Hex(), Owner: a.Owner.Hex(), OpenedAt: a.OpenedAt}
	for _, b := range bals {
		v.Balances = append(v.Balances, balanceView{
			Token: b.Token.Hex(), OnChain: b.OnChain.String(), Owed: b.Owed.String(), Available: b.Available.String(),
		})
	}
	return v
}

func viewPayment(p orchestrator.Payment) paymentView {
	v := paymentView{ID: p.ID, State: string(p.State), Payee: p.Payee, Asset: p.Asset,
		Amount: p.Amount.String(), TxHash: p.TxHash, Ref: refHex(p.ID)}
	for _, e := range p.History {
		v.History = append(v.History, eventView{From: string(e.From), To: string(e.To), Reason: e.Reason, At: e.At})
	}
	return v
}

func refHex(id string) string {
	ref := bank.RefFor(id)
	return hexutil.Encode(ref[:])
}

func viewEntry(e ledger.Entry) entryView {
	v := entryView{ID: e.ID, Ref: e.Ref, Memo: e.Memo, Reverses: e.Reverses}
	for _, l := range e.Lines {
		side := "debit"
		if l.Side == ledger.Credit {
			side = "credit"
		}
		v.Lines = append(v.Lines, lineView{Account: l.Account, Side: side, Amount: l.Amount.String()})
	}
	return v
}

// --------------------------------------------------------------- handlers

func (s *Server) listAccounts(w http.ResponseWriter, _ *http.Request) {
	out := []accountView{}
	for _, a := range s.bank.Accounts() {
		out = append(out, viewAccount(a, nil))
	}
	writeJSON(w, 200, out)
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	addr, ok := parseAddress(w, r.PathValue("address"))
	if !ok {
		return
	}
	a, found := s.bank.Account(addr)
	if !found {
		writeError(w, 404, "account not found")
		return
	}
	writeJSON(w, 200, viewAccount(a, s.bank.Balances(addr)))
}

func (s *Server) statement(w http.ResponseWriter, r *http.Request) {
	addr, ok := parseAddress(w, r.PathValue("address"))
	if !ok {
		return
	}
	token, ok := parseAddress(w, r.URL.Query().Get("token"))
	if !ok {
		return
	}
	out := []entryView{}
	for _, e := range s.bank.Statement(addr, token) {
		out = append(out, viewEntry(e))
	}
	writeJSON(w, 200, out)
}

type paymentRequest struct {
	ID         string `json:"id"`
	Account    string `json:"account"`
	Token      string `json:"token"`
	To         string `json:"to"`
	Amount     string `json:"amount"`
	Nonce      string `json:"nonce"`
	Deadline   uint64 `json:"deadline"`
	SessionSig string `json:"sessionSig"`
	OwnerSig   string `json:"ownerSig,omitempty"`
}

func (s *Server) submitPayment(w http.ResponseWriter, r *http.Request) {
	var in paymentRequest
	if !readJSON(w, r, &in) {
		return
	}
	req, err := in.decode()
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	p, err := s.bank.SubmitPayment(r.Context(), req)
	if err != nil {
		writeBankError(w, err)
		return
	}
	writeJSON(w, statusFor(p), viewPayment(p))
}

func (in paymentRequest) decode() (bank.PaymentRequest, error) {
	var req bank.PaymentRequest
	var err error
	req.ID = in.ID
	if req.Account, err = addr(in.Account, "account"); err != nil {
		return req, err
	}
	if req.Token, err = addr(in.Token, "token"); err != nil {
		return req, err
	}
	if req.To, err = addr(in.To, "to"); err != nil {
		return req, err
	}
	if req.Amount, err = uint256(in.Amount, "amount"); err != nil {
		return req, err
	}
	if req.Nonce, err = uint256(in.Nonce, "nonce"); err != nil {
		return req, err
	}
	req.Deadline = in.Deadline
	if req.SessionSig, err = hexutil.Decode(in.SessionSig); err != nil {
		return req, fmt.Errorf("sessionSig: %w", err)
	}
	if in.OwnerSig != "" {
		if req.OwnerSig, err = hexutil.Decode(in.OwnerSig); err != nil {
			return req, fmt.Errorf("ownerSig: %w", err)
		}
	}
	return req, nil
}

func (s *Server) getPayment(w http.ResponseWriter, r *http.Request) {
	p, err := s.bank.Orchestrator().Get(r.PathValue("id"))
	if err != nil {
		writeBankError(w, err)
		return
	}
	writeJSON(w, 200, viewPayment(p))
}

func (s *Server) cosign(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OwnerSig string `json:"ownerSig"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	sig, err := hexutil.Decode(in.OwnerSig)
	if err != nil {
		writeError(w, 400, "ownerSig: "+err.Error())
		return
	}
	p, err := s.bank.CoSign(r.Context(), r.PathValue("id"), sig)
	if err != nil {
		writeBankError(w, err)
		return
	}
	writeJSON(w, 200, viewPayment(p))
}

func (s *Server) decline(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	p, err := s.bank.Decline(r.Context(), r.PathValue("id"), in.Reason)
	if err != nil {
		writeBankError(w, err)
		return
	}
	writeJSON(w, 200, viewPayment(p))
}

func (s *Server) getEscrow(w http.ResponseWriter, r *http.Request) {
	h, ok := s.bank.Hold(r.PathValue("id"))
	if !ok {
		writeError(w, 404, "escrow hold not found")
		return
	}
	writeJSON(w, 200, map[string]any{
		"id": h.ID, "payer": h.Payer.Hex(), "payee": h.Payee.Hex(), "token": h.Token.Hex(),
		"amount": h.Amount.String(), "remaining": h.Remaining.String(), "expiry": h.Expiry, "closed": h.Closed,
	})
}

// ---------------------------------------------------------------- helpers

// statusFor: 201 when the payment is moving (or done), 202 when it waits on
// the owner, 422 when a gate rejected it.
func statusFor(p orchestrator.Payment) int {
	switch p.State {
	case orchestrator.Rejected:
		return http.StatusUnprocessableEntity
	case orchestrator.AwaitingCoSign:
		return http.StatusAccepted
	}
	return http.StatusCreated
}

func writeBankError(w http.ResponseWriter, err error) {
	var te *orchestrator.TransitionError
	switch {
	case errors.Is(err, orchestrator.ErrNotFound):
		writeError(w, 404, err.Error())
	case errors.Is(err, orchestrator.ErrIdempotency), errors.As(err, &te):
		writeError(w, 409, err.Error())
	case errors.Is(err, bank.ErrBadRequest), errors.Is(err, bank.ErrUnknownAccount),
		errors.Is(err, bank.ErrUnknownToken), errors.Is(err, orchestrator.ErrInvalidPayment):
		writeError(w, 400, err.Error())
	case errors.Is(err, context.Canceled):
		writeError(w, 499, err.Error())
	default:
		writeError(w, 500, err.Error())
	}
}

func parseAddress(w http.ResponseWriter, s string) (common.Address, bool) {
	a, err := addr(s, "address")
	if err != nil {
		writeError(w, 400, err.Error())
		return common.Address{}, false
	}
	return a, true
}

func addr(s, field string) (common.Address, error) {
	if !common.IsHexAddress(s) {
		return common.Address{}, fmt.Errorf("%s: not a hex address", field)
	}
	return common.HexToAddress(s), nil
}

func uint256(s, field string) (*big.Int, error) {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok || v.Sign() < 0 || v.BitLen() > 256 {
		return nil, fmt.Errorf("%s: not a uint256 decimal string", field)
	}
	return v, nil
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
