# Continuous controls

| Control | What runs | Trigger / action |
|---|---|---|
| Wallet & chain risk | RPC health and cross-validation, address analytics, **depeg monitor** (oracle price per stablecoin) | Depeg beyond band → halt conversions and treasury sweep for that asset |
| Financial crime | Sanctions screening (on-chain oracle + off-chain list), AML transaction monitoring (velocity, structuring, counterparties) | Hit → payment `Exception`, node frozen pending review |
| Travel Rule | Originator/beneficiary data exchanged off-chain with counterparty VASPs | Data never written on-chain |
| Treasury & liquidity | Inventory per chain/asset, rebalancing via CCTP, per-issuer exposure caps (USDC/EURC/USDT/PYUSD) | Cap reached → route to another asset or hold |
| Custody governance | Owner and treasury keys in MPC/HSM/Safe, dual approval for policy increases, key rotation, session-key expiry | — |
| Operations & recovery | Idempotent retries, returns, exception console, runbooks | SLA breaches alert |
