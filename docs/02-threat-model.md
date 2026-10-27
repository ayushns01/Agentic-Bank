# Threat model

Assets: funds in agent accounts and in the treasury, credit-pool deposits, policy configuration, ledger integrity.

| # | Threat | Vector | Mitigation |
|---|---|---|---|
| T1 | Prompt injection leads to an unwanted payment | Malicious content steers the agent | On-chain caps, recipient/target allowlists, co-sign threshold. The agent can only act inside its policy, whatever it "decides" |
| T2 | Session key stolen | Leaked agent runtime | Short expiry, per-key limits, owner revocation, freeze of the subtree, velocity limits |
| T3 | Compromised operator backend | Relayer or orchestrator breached | The relayer cannot forge intents (EIP-712 signature required) and on-chain policy still binds. A worst-case backend can only censor |
| T4 | Replay or malleability | Re-submitted or cross-chain intents | EIP-712 domain separator with chainId and account address, per-key nonces, deadlines |
| T5 | Policy-update race | Spend lands mid-update | Policy changes are owner-only and apply atomically. Limit decreases take effect immediately; increases can be time-locked (P6) |
| T6 | Merchant does not deliver | Prepaid x402/MPP | Escrow for amounts above the policy's escrow threshold, refunds, reputation |
| T7 | Price/FX manipulation | Swap during conversion | Max-slippage policy, oracle sanity bounds, depeg circuit breaker |
| T8 | Cross-chain failure | CCTP attestation delay or failure | Payment held with timeout, then return path. Ledger records the in-flight state |
| T9 | Ledger/chain drift | Missed event, reorg | Finality thresholds per chain, reconciliation close, a breaker on unexplained drift |
| T10 | Credit default / Sybil reputation | Fresh identities borrowing | Credit lines start at zero, grow from repaid cash flow, are capped by collateral and receivables, and junior tranches absorb losses first |
| T11 | Reentrancy / token quirks | Malicious ERC-20, USDT no-bool | Checks-effects-interactions, reentrancy guard, SafeERC20 semantics |
| T12 | Sanctions exposure | Paying a listed address | Screening gate before signing. Travel Rule data kept off-chain |

## Trust assumptions

- The account owner (a human or an org multisig) is trusted to configure policy.
- The relayer is untrusted for safety but trusted for liveness. Anyone can relay a valid intent.
- Oracles (rates, sanctions lists) are trusted within staleness and deviation bounds.
