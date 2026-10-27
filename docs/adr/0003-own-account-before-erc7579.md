# ADR 0003: Purpose-built AgentAccount first, ERC-4337/7579 compatibility next

**Status:** accepted

## Context

ERC-7579 modules on Safe, Kernel or Nexus are the ecosystem direction. Building on them from day one pulls in a large dependency surface before the policy model has settled.

## Decision

P1 ships a minimal `AgentAccount`:
- an owner;
- session keys mapped to policy nodes;
- `executeIntent` accepting EIP-712 intents that anyone can relay.

`PolicyEngine` is a separate contract with a narrow interface (`authorize`). That way it can later be wrapped as an ERC-7579 validator or hook module without changes. ERC-4337 `validateUserOp` support follows in P1b.

## Consequences

- Faster iteration and a small audit surface.
- A migration step later. It is kept cheap by keeping the policy logic outside the account.
