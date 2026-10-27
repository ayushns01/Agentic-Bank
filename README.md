# Agentic Bank

An on-chain banking layer for AI agents. Agents hold, move, receive, convert and spend stablecoins, and they can borrow and run trade-finance flows. Programmable financial policies are enforced **on-chain**.

```
Agent → Policy Engine → Wallet/Vault → Payment Rails → Blockchain → Settlement
```

This is a protocol and operator stack, not a chartered bank. It covers six areas:

- **Accounts and treasury:** per-agent smart accounts, an org → agent → sub-agent delegation tree, and an idle-balance yield sweep.
- **Policy:** two tiers. Off-chain pre-flight checks may only be *stricter* than the on-chain enforcement, which is the source of truth.
- **Rails:** x402, MPP and AP2 mandates; CCTP v2 for cross-chain; stablecoin swaps; fiat ramps through Circle and Bridge.
- **Banking products:** invoices, payment links, subscriptions, holds and captures, refunds and payouts.
- **Trade finance and credit:** escrow, letters of credit, milestone payments, factoring, term loans and a tranched credit pool. The pricing comes from the actuarial instruments library.
- **Settlement:** a payment state machine with bank-style control gates, a double-entry ledger, and reconciliation against the chain.

## Layout

| Path | What |
|---|---|
| `docs/` | Landscape, architecture, threat model, specs, ADRs |
| `contracts/` | Solidity (Foundry): accounts, policy engine, instruments |
| `services/` | Go: ledger, payment orchestrator, indexer, reconciliation |

Start with [`docs/01-architecture.md`](docs/01-architecture.md).

## Status

| Phase | Scope | State |
|---|---|---|
| P0 | Design docs | done |
| P1 | Accounts + on-chain policy core, SDK/MCP, x402 pay | in progress |
| P2 | Orchestrator, ledger, indexer, reconciliation, treasury | in progress (ledger + state machine) |
| P3 | Fintech rails, trade finance | planned |
| P4 | Convert + cross-chain | planned |
| P5 | Credit, factoring, tranched pool | planned |
| P6 | Hardening, audit report, benchmarks | planned |
