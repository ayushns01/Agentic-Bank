# Policy model

The model has two levels. The off-chain policy language (YAML/JSON) is compiled to on-chain node configuration and can only add constraints on top of it.

## On-chain node (enforced by `PolicyEngine`)

Each delegation node is identified by a `bytes32 nodeId` and has:

| Field | Meaning |
|---|---|
| `parent` | Parent node (`0` = root) |
| `account` | AgentAccount the node spends from |
| `perTxLimit` | Max amount per payment, in token units |
| `periodLimit` / `periodLength` | Rolling-window budget (fixed windows aligned to `periodStart`) |
| `spentInPeriod` | Running total for the current window |
| `coSignAbove` | Amounts above this need an owner co-signature |
| `validUntil` | Node expiry |
| `frozen` | Kill switch. Freezing a node blocks every descendant |
| token allowlist | Which stablecoins the node may spend |
| recipient allowlist | Optional. When enabled, only listed payees |

**Check:** a spend of `amount` of `token` to `to` by node `n` passes only if it passes for `n` and for every ancestor up to the root (maximum depth 8). On success, `amount` is charged to every node on that path.

## Off-chain policy language (example)

```yaml
node: research-agent
parent: acme-ops
limits:
  per_tx: 25 USDC
  period: { window: 1d, max: 200 USDC }
  co_sign_above: 100 USDC
tokens: [USDC]
recipients:
  mode: allowlist
  list: [api.dataco.eth, 0xMerchant...]
categories:            # off-chain only (merchant tags)
  data_apis: { period: 1d, max: 150 USDC }
time_windows: ["Mon-Fri 08:00-20:00 UTC"]   # off-chain only
escrow_above: 50 USDC                         # route through escrow
max_slippage_bps: 30                          # conversions
```

Rules that need off-chain context (merchant categories, time windows, risk scores) run in the orchestrator's pre-flight. Everything that bounds money movement also exists on-chain.
