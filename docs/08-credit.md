# Credit

## Credit lines for agents

- **Underwriting inputs:** settled inflows (the agent's receivables history in the ledger and on-chain), ERC-8004 reputation, repayment history, collateral, and the owner's KYB tier.
- **Line sizing:** new agents start at 0. The limit is a function of trailing net inflows, capped by collateral plus eligible receivables.
- **Draw:** funding gate 4 can draw on the line when the prefunded balance falls short. The draw becomes an `AmortizedLoan` or interest-only obligation.
- **Repayment:** auto-sweep a configured share of incoming payments, plus scheduled instalments.
- **Default:** arrears beyond grace → node frozen, collateral seized, reputation penalty, loss allocated through the pool waterfall.

## Credit pool

The pool is an ERC-4626 style vault split into **senior and junior tranches** using the CDO waterfall: interest is paid down the stack, then principal, and losses hit the junior tranche first. Senior LPs earn a lower, safer yield; junior LPs are paid for absorbing first loss.

## Factoring

Receivables are obligation NFTs. The pool buys them at the bill-discount price `F(1 − d·t)`, where `d` comes from the payer's risk tier. At maturity the payer pays the NFT holder.
