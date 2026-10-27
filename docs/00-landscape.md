# Landscape (October 2026)

Who is building agent finance, and where the gaps are.

| Layer | Large companies | Startups | Read |
|---|---|---|---|
| Rails / protocols | Coinbase **x402** (Linux Foundation x402 Foundation since Apr 2026; members include AWS, Google, Microsoft, Stripe, Visa, Mastercard, Circle). Stripe + Paradigm **Tempo** with **MPP** sessions. Google **AP2** (signed mandates). OpenAI + Stripe **ACP**. Visa Intelligent Commerce / Trusted Agent Protocol. Mastercard Agent Pay | Nevermined, Skyfire | Giants are standardizing the rails. Sit **above** them and stay rail-agnostic |
| Settlement chains | Circle **Arc** + Nanopayments (Gateway, batched sub-cent USDC), Tempo, Base | Kite AI | Chains with dollar gas and sub-second finality. Multi-chain from day one |
| Agent wallets + policy | Coinbase Agentic Wallets / AgentKit / CDP Server Wallets, Stripe (Privy, Bridge), Circle Programmable Wallets / Agent Stack, MoonPay Agents | Turnkey, Crossmint, Payman, Safe / Rhinestone / ZeroDev | **Crowded.** Policies are mostly off-chain (TEE-side), flat and per-wallet |
| Identity / trust | ERC-8004 (Identity, Reputation, Validation registries), Visa TAP, Skyfire KYA | Catena Agent Commerce Kit | Identity exists. Reputation rarely has financial consequences yet |
| AI-native bank | — | **Catena Labs** ($18M seed in 2025, $30M Series A in May 2026, applied for an OCC national trust charter) | The regulated "AI bank" path is taken |
| Agent credit / risk | — | bond.credit, Cork | Least crowded and very early |

## Gaps this project targets

1. **Policies that are hierarchical and enforced on-chain.** Budgets cascade from an org to its agents and on to sub-agents, and the account contract enforces them. A compromised backend or a prompt-injected agent cannot exceed them.
2. **Bank-grade payment operations for agents.** Control gates, a double-entry ledger, reconciliation, exception handling.
3. **Fixed-income and credit primitives for agents.** Loans, schedules, factoring and tranched pools, with real actuarial pricing.
4. **Trade finance between agents.** Escrow and letters of credit released by verified work (ERC-8004 validation).

## Caveat on metrics

Adoption numbers are noisy. x402 reports more than 160M transactions, while one March 2026 analysis put real daily volume near $28K. The infrastructure is ahead of demand.

## Sources

- https://cointelegraph.com/news/big-tech-companies-join-x402-protocol-agentic-ai
- https://www.paymentsjournal.com/stripe-launches-tempo-blockchain-to-power-ai-driven-payments/
- https://fortune.com/2026/05/20/catena-labs-series-a-sean-neville-ai-native-bank/
- https://eips.ethereum.org/EIPS/eip-8004
- https://www.pymnts.com/earnings/2026/circle-chases-agentic-growth-scale-stablecoin-infrastructure
- https://docs.turnkey.com/solutions/company-wallets/agentic-wallets
- https://docs.cdp.coinbase.com/agentic-wallet/cli/welcome.md
- https://www.cork.tech/blog/agents-trade-risk
