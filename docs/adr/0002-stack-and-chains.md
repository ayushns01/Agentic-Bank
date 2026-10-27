# ADR 0002: Stack and chains

**Status:** accepted

## Decision

- **Contracts:** Solidity with Foundry (unit, fuzz and invariant tests).
- **Services:** Go, with Postgres for storage.
- **Agent tooling:** a TypeScript SDK and MCP server using viem.
- **Chains:** Base first (Base Sepolia for development, close to the x402/Coinbase ecosystem), then Arbitrum via CCTP v2. Arc and Tempo adapters later.

## Why

These match the team's strengths. Base has the deepest agent-payments ecosystem today. CCTP v2 gives native USDC across chains.
