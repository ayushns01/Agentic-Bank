# Ledger, payment lifecycle and settlement

This adapts the operating model banks use for stablecoin payments: screen before signing, treat on-chain confirmation as an execution milestone, and settle only after accounting, reconciliation and control checks.

## Control gates

| # | Gate | Agent-bank implementation | Fails to |
|---|---|---|---|
| 1 | Authentication | Agent identity (ERC-8004 + KYA), live session key, node entitlements, owner KYC/KYB | Rejected |
| 2 | Instruction | EIP-712 intent: amount, recipient, asset, rail, maxFee, deadline, idempotency key | Rejected |
| 3 | Address & risk | Chain check, recipient ownership, allowlist, address risk score | Rejected |
| 4 | Funding & FX | Prefunded balance or credit draw. FX lock with slippage bound. Ledger **hold** placed | Rejected |
| 5 | Stablecoin supply | Treasury inventory on the target chain/asset; rebalance via CCTP or mint | Held |
| 6 | Compliance | Sanctions/AML screen, counterparty re-screen, Travel Rule exchange (off-chain) | Rejected or Exception |
| 7 | Signing | Session-key signature; co-sign above threshold; owner keys in MPC/HSM | Rejected or Awaiting co-sign |
| 8 | Execution | Broadcast, per-chain finality threshold, fee cap | Held → Returned or Exception |
| 9 | Credit / off-ramp | Recipient credited from chain receipt, or fiat payout status | Exception |

## State machine (`services/orchestrator`)

```
Created ─► Validated ─► Screening ─► Signed ─► Broadcast ─► Confirmed ─► Reconciled ─► Settled
   │           │            │          │          │             │             │
   └─► Rejected◄┴────────────┘          │          ▼             ▼             ▼
                                       │        Held ─────► Returned       Exception
                          AwaitingCoSign◄┘        └────────► Exception
```

- `Rejected` happens before any funds move. The hold is released.
- `Held`: execution failed or timed out after funds were reserved. Funds stay reserved until the payment is returned or an operator resolves it.
- `Returned`: the hold is released, or reversed if anything had been posted.
- `Exception`: needs manual review. Funds stay held.
- Every transition is appended to the payment's audit history.

## Event accounting (`services/ledger`)

Double-entry. Every journal entry balances per asset. Account types:

| Type | Normal balance | Examples |
|---|---|---|
| Asset | Debit | on-chain wallet balances, in-transit, receivables |
| Liability | Credit | customer (agent) balances, payables |
| Revenue / Expense | Credit / Debit | fees, network gas |
| Equity | Credit | protocol reserves |

Payment entries:
1. **Hold:** reserves `amount` against the payer's available balance. No posting yet.
2. **Post:** on confirmation the hold is consumed: Dr payer liability, Cr in-transit or payee liability, Dr network fee expense, Cr wallet asset.
3. **Reverse:** a mirror entry linked to the original.

Invariants (tested):
- Each entry balances per asset (Σdebits = Σcredits).
- No account's available balance (balance minus holds) drops below zero unless the account allows overdraft (credit lines).
- Entries are immutable. Corrections are new entries.
- Idempotency: posting the same entry id twice is a no-op.

## Reconciliation

The engine matches three sources: ledger postings, chain events (indexer) and partner confirmations (Circle, Bridge).

| Result | Meaning | Action |
|---|---|---|
| Matched | All present and equal | Mark payment `Settled` |
| Break: missing on chain | Posted, no event after the finality timeout | Exception |
| Break: unexpected on chain | Event with no ledger record | Exception + breaker if above threshold |
| Break: amount mismatch | Amounts differ | Exception |

The daily close produces the matched count, the breaks, the intraday positions per agent/org/chain/asset, and the audit trail.
