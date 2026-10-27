# Instruments: review of `actuarial-evm`

The source library has 27 payment formats from Broverman's *Mathematics of Investment and Credit*: annuities, loans, bonds, bills and CDs, an interest-rate swap and forward, a generic cashflow stream, an MBS pass-through and a CDO waterfall. It is about 3.3k lines of Solidity 0.8.24 with no external dependencies.

- `base/PaymentStream.sol` defines each instrument by two functions, `offsetPeriods(k)` and `paymentAmount(k)`.
- `lib/WadMath.sol` provides wmul, wpow, expWad and lnWad.
- `lib/ActuarialMath.sol` holds the closed-form formulas.
- The library has 142 Python checks. It is unaudited.

**Verdict:** adopt it as the pricing and schedule engine for credit and trade finance, after the refactors below.

## Refactors required

1. **One deployment per instrument** → an `InstrumentFactory` creating ERC-1167 clones (or a single schedule engine with typed terms). Terms are EIP-712 signed by both parties.
2. **Immutable payer/payee** → each position is an **ERC-721 obligation NFT**. This makes factoring, pool assignment and secondary transfer possible.
3. **Raw allowance pulls, no collateral, defaults only reported** → the payer's AgentAccount grants a scheduled-obligation permission capped to the schedule. A keeper settles. A default feeds the credit module (freeze, collateral seizure, reputation).
4. **Single-EOA rate/index setters** (swap, forward, inflation-indexed bond) → an oracle adapter with staleness and deviation checks.
5. **Token handling** → SafeERC20 semantics, reject fee-on-transfer tokens, sweep wad-truncation dust.
6. **Gas** → schedule loops (`valueAtPeriod`, `arrears`, `remainingTotal`) stay view-only. Batch caps on `payDue`.
7. **Tests** → port the Python checks to Foundry. Add fuzz tests (closed form ≈ schedule sum) and invariants (Σpaid ≤ Σscheduled; senior tranche paid before junior).
8. **Regulation** → bonds, CDO and MBS offered to the public are securities. Keep them on testnet and permissioned (ERC-3643-style allowlist hook).

## Tier map

| Tier | Formats | Use |
|---|---|---|
| 1 | AmortizedLoan, LevelPrincipalLoan, InterestOnlyBalloonLoan | Agent term loans and credit-line repayment |
| 1 | AnnuityImmediate / Due / Deferred | Subscriptions, retainers, payroll, recurring pulls |
| 1 | ContinuousAnnuity | Per-second streaming |
| 1 | CashflowStream | Invoice, milestone and LC schedules |
| 1 | TreasuryBill math | Factoring discount pricing |
| 1 | CertificateOfDeposit | Agent term deposits |
| 1 | CollateralizedDebtObligation | Tranched credit pool |
| 2 | Coupon / Zero / Callable / Serial bonds, IRS, FRA | Treasury investments, pool rate hedging |
| 3 | MBS, Convertible, TIPS, perpetuities, geometric/increasing/decreasing, m-thly, SinkingFund | Pricing library only |
