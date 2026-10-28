# Contracts

| Contract | Purpose |
|---|---|
| `src/policy/PolicyEngine.sol` | Delegation tree of policy nodes. Per-tx, per-period and co-sign limits; token and recipient allowlists; freeze; expiry. Each spend is checked and charged at every ancestor |
| `src/account/AgentAccount.sol` | Holds funds. Executes EIP-712 payment intents signed by agent session keys, which anyone may relay. Optional owner co-signature (EOA or EIP-1271) |
| `src/lib/*` | SafeTransfer (works with no-return tokens), SignatureChecker (ECDSA low-s + EIP-1271) |

## Setup

```bash
cd contracts
forge install foundry-rs/forge-std
forge test -vv
```

## Tests

`test/AgentAccount.t.sol` covers:

- payment within policy, charged up the tree;
- per-tx limit, co-sign threshold and a forged co-signature;
- period limit and window reset;
- the parent budget binding a child;
- freezing a parent freezes its subtree;
- token and recipient allowlists;
- a prompt-injected drain attempt;
- replay, account binding, expired intent, revoked and expired keys, a malformed signature;
- admin access control;
- a USDT-style token that returns nothing;
- a fuzz test that total outflow never exceeds the period budget.
