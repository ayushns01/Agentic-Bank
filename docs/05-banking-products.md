# Banking products (fintech rails)

Products are built on the account, policy and ledger layers. Each one is a set of intents plus ledger entry types.

| Product | Mechanics | On-chain piece | Ledger entries |
|---|---|---|---|
| Invoice | Merchant agent issues an EIP-712 invoice (amount, asset, due date, ref). Payer agent pays against its id | Invoice id recorded in the payment `ref` | receivable → cash on settle |
| Payment link | Reusable invoice template exposed over x402 / HTTP 402 | Same as invoice | — |
| Subscription / recurring pull | Payer authorizes a schedule; a keeper pulls each due payment | Scheduled-obligation instrument (annuity), bounded by policy | obligation accrual, payment |
| Hold & capture | Funds reserved, then captured in part or in full, or released | Off-chain hold in the ledger; escrow contract for trustless holds | hold, capture, release |
| Refund | Reverse of a settled payment, full or partial, linked to the original | Transfer back with `ref` = original | reversal entries |
| Payouts / batch disbursement | One intent pays N recipients (payroll, bounties) | Batch execution in AgentAccount | N debit/credit pairs |
| Fiat ramp | USDC ↔ USD/EUR via Circle / Bridge (ACH, wire, SEPA) | — (partner API + webhook) | in-transit account, then cash |
| Term deposit | Lock funds for a fixed term at a quoted rate | CertificateOfDeposit instrument | deposit, accrued interest |
| Treasury sweep | Idle balance above a floor → ERC-4626 vault | Vault | cash ↔ investment |
