# Contracts

| Contract | Purpose |
|---|---|
| `src/policy/PolicyEngine.sol` | Delegation tree of policy nodes. Holds per-tx, per-period and co-sign limits, token and recipient allowlists, freeze, expiry, and the **call allowlist** (target + selector). Every spend is checked and charged at every ancestor |
| `src/account/AgentAccount.sol` | Holds funds. Executes EIP-712 intents signed by agent session keys; anyone may relay them. Three kinds: `PaymentIntent` (transfer), `BatchIntent` (payouts), `CallIntent` (approved venue call). Optional owner co-signature (EOA or EIP-1271) |
| `src/account/AccountFactory.sol` | Opens accounts at CREATE2 addresses bound to the owner, so an account can be funded before it is opened |
| `src/products/Escrow.sol` | Trustless holds: open, partial capture, refund, arbiter resolution, expiry |
| `src/testnet/TestToken.sol`, `Faucet.sol` | Mintable test stablecoin, plus a faucet with a cooldown and a lifetime cap per recipient |
| `src/lib/*` | SafeTransfer (works with no-return tokens), SignatureChecker (ECDSA low-s + EIP-1271) |

## Security notes

- A `CallIntent` that uses ERC-20 `transfer`, `transferFrom` (from the account), `approve` or `increaseAllowance` is charged against the spend limits. Approving a venue charges the approved amount up front. Revoking (amount 0) is free.
- Agents can never call the account itself or the `PolicyEngine`, so they cannot rewrite their own limits.
- Budgets are denominated in the account's stablecoin units. Keep the tokens allowed on a node at the same decimals. `CallIntent` carries no native value.

## Setup

```bash
cd contracts
forge install foundry-rs/forge-std
forge test -vv
```

## Tests

48 tests: unit, fuzz with 1,024 runs, and invariant with 256 runs × 64 calls.

| Suite | Covers |
|---|---|
| `AgentAccount.t.sol` | Payment intents: per-tx, period and co-sign limits; tree charging; freeze; allowlists; prompt-injected drain; replay; expiry; key revocation; no-return tokens |
| `BankCore.t.sol` | Factory and faucet; venue calls (approve + deposit, disallowed selectors, parent permissions, engine/self-call block, approve and transfer charging); batch payouts (atomicity, tampering) |
| `Escrow.t.sol` | Open, capture, refund, resolve, expiry and auth; fuzzed conservation of funds |
| `invariant/BudgetInvariant.t.sol` | Two sibling agents can never expose more than their org's budget, and tokens leave only through charged paths |
