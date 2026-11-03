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
| `contracts/` | Solidity (Foundry): policy engine, agent accounts, account factory, escrow, testnet faucet |
| `services/` | Go: double-entry ledger, payment orchestrator, chain indexer, relayer, REST API (`bankd`) |
| `sdk/` | TypeScript SDK and MCP server for agents |
| `scripts/` | `e2e.sh` (full local run on anvil), `gen-abi.sh` |

Start with [`docs/01-architecture.md`](docs/01-architecture.md).

## Run it

```bash
cd contracts && forge install foundry-rs/forge-std && forge test   # 48 tests: unit, fuzz, invariant
cd services && go test -race ./...                                  # incl. integration on a simulated chain
cd sdk && npm install && npm test
./scripts/e2e.sh   # anvil -> deploy -> bankd -> agent pays, gets blocked, gets co-signed -> MCP tools
```

What the end-to-end run shows:

1. An org owner opens an account at a predicted address and funds it from the faucet.
2. The owner sets an org → agent policy tree and gives the agent a session key.
3. The agent pays a merchant through the API. Funds are held, the policy is dry-run, the intent is relayed and indexed, and the payment is settled and reconciled.
4. A prompt-injected "drain" is rejected before anything is signed on-chain.
5. A payment over the co-sign threshold waits for the owner's signature, then settles.
6. The ledger matches the on-chain balance exactly.
7. An LLM-facing MCP client pays by payee name. Raw addresses and over-limit amounts are refused.

## Status

| Phase | Scope | State |
|---|---|---|
| P0 | Design docs | done |
| P1 | Accounts + on-chain policy core (payments, batch payouts, venue calls), factory, escrow, SDK/MCP | done (x402 rail pending) |
| P2 | Orchestrator, ledger, indexer, relayer, API, reconciliation | done (treasury sweep, screening adapter, Postgres pending) |
| P3 | Fintech rails, trade finance | planned |
| P4 | Convert + cross-chain | planned |
| P5 | Credit, factoring, tranched pool | planned |
| P6 | Hardening, audit report, benchmarks | planned |
