# ADR 0001: On-chain policy is the source of truth

**Status:** accepted

## Context

Most agent-wallet products enforce spend policy off-chain, inside a TEE or in the signing service. If that service, or the agent driving it, is compromised or prompt-injected, the policy is gone.

## Decision

Every limit that bounds money movement is enforced by `PolicyEngine`, which `AgentAccount` calls on every spend. The off-chain orchestrator runs the same checks first, plus context-dependent ones (categories, time windows, risk scores). It may only be stricter.

## Consequences

- Extra gas per payment, roughly one storage write per ancestor node. This is acceptable on L2s.
- Policies need a compact on-chain representation (see `03-policy-dsl.md`).
- Security reviews can reason about the contracts alone.
