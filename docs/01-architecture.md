# Architecture

## Layers

```
 Agents (LLM frameworks, MCP clients, A2A)
        │  TS SDK · MCP server · REST
 ┌──────▼──────────────────────────────────────────────┐
 │ Gateway: EIP-712 intents, idempotency keys, authn    │
 ├──────────────────────────────────────────────────────┤
 │ Payment Orchestrator: state machine + control gates  │──► Approval queue (co-sign)
 │   pre-flight policy · screening · funding/FX · sign  │──► Exception console
 ├──────────────────────────────────────────────────────┤
 │ On-chain: AgentAccount + PolicyEngine (source of     │
 │   truth), delegation tree, treasury vault (4626)     │
 ├──────────────────────────────────────────────────────┤
 │ Rails: x402 · MPP · AP2 · CCTPv2 · swaps · streaming │
 │   · fiat ramp (Circle / Bridge)                      │
 ├──────────────────────────────────────────────────────┤
 │ Products: invoices · links · subscriptions · holds · │
 │   refunds · payouts                                  │
 │ Trade finance + credit: escrow · LC · milestones ·   │
 │   factoring · loans · tranched pool (instruments)    │
 ├──────────────────────────────────────────────────────┤
 │ Chains: Base (primary), Arbitrum; Arc/Tempo adapters │
 ├──────────────────────────────────────────────────────┤
 │ Indexer → Core Ledger (double-entry) → Reconciliation│
 │   → statements · intraday position · audit trail     │
 └──────────────────────────────────────────────────────┘
```

## Principles

1. **On-chain policy is authoritative.** Off-chain checks exist to reject early and cheaply. They may be stricter than the on-chain policy, never looser. A prompt-injected agent or a compromised operator cannot exceed what the account contract allows.
2. **Delegation is a tree.** Each node (org, agent, sub-agent) has its own limits. A spend must fit every node from the spender up to the root, and it is charged to all of them. Freezing a node freezes its whole subtree.
3. **Session keys, not owner keys.** Agents sign with scoped, expiring keys. Owner and treasury keys live in a Safe, MPC/HSM or passkey.
4. **Screen before signing.** Compliance and risk gates complete before any signature exists.
5. **Confirmation is a milestone, not settlement.** A payment is *settled* only when the ledger, the chain events and any partner confirmations reconcile.
6. **Every value movement is a balanced ledger entry.** Drift between the ledger and the chain trips a circuit breaker.
7. **Rail-agnostic intents.** Agents state *what* ("pay X up to Y for Z"). The orchestrator chooses *how*: the rail, the chain and any conversion.

## Components

| Component | Lang | Responsibility |
|---|---|---|
| `contracts/src/account/AgentAccount.sol` | Solidity | Holds funds. Executes EIP-712 intents signed by session keys. Owner admin |
| `contracts/src/policy/PolicyEngine.sol` | Solidity | Delegation tree, per-node limits, period budgets, allowlists, co-sign threshold, freeze |
| `contracts/src/instruments/*` | Solidity | Loans, schedules, deposits, tranched pool (from the actuarial library, refactored) |
| `services/ledger` | Go | Double-entry ledger: accounts, journal entries, holds, balance invariants |
| `services/orchestrator` | Go | Payment state machine, control gates, exception paths |
| `services/indexer` | Go | Chain events → ledger events, per-chain finality |
| `services/recon` | Go | Matches ledger ↔ chain ↔ partner records, opens breaks |
| `sdk/` | TS | Agent SDK and MCP server |

## Main flow: an agent pays a merchant over x402

```
Agent        Gateway/Orch          Policy(off)   Compliance   AgentAccount(on)   Chain   Indexer/Ledger
  │ intent ──►│                        │             │              │              │          │
  │           │ validate (gate 1-3) ──►│             │              │              │          │
  │           │ hold funds in ledger ───────────────────────────────────────────────────────► │ HOLD
  │           │ screen (gate 6) ────────────────────►│              │              │          │
  │           │ request session-key signature (gate 7, co-sign if over threshold)  │          │
  │           │ relay executeIntent ───────────────────────────────►│ policy check │          │
  │           │                                                     │──transfer───►│          │
  │           │                                                     │              │──event──►│ CONFIRMED
  │           │                                                                     finality  │ POST hold
  │           │ ◄──────────────────────────────── reconcile (ledger = chain) ───────────────── │ SETTLED
```

If any step fails, the payment moves to `Held`, then `Returned` or `Exception`. See [07-ledger-settlement.md](07-ledger-settlement.md).
