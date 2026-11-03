# @agentic-bank/sdk

TypeScript SDK and MCP server for agents that bank with `bankd`.

The design rule is **the LLM decides, code executes**:

- Agents pay **named payees** from an address book. Raw hex addresses are refused, which removes address poisoning and mangled hex.
- Amounts are **decimal strings parsed strictly** (`"12.50"`). No exponents, separators, or more decimals than the token has.
- Every payment is an EIP-712 intent signed by the agent's **session key**. It still passes the bank's gates and the on-chain `PolicyEngine`.
- The payment id is the idempotency key, so retrying with the same reference never pays twice.

## Library

```ts
import { AddressBook, BankClient } from "@agentic-bank/sdk";
import { privateKeyToAccount } from "viem/accounts";

const bank = new BankClient({
  bankUrl: "http://127.0.0.1:8080",
  rpcUrl: "http://127.0.0.1:8545",
  chainId: 31337,
  account: "0xYourAgentAccount",
  sessionKey: privateKeyToAccount(process.env.AGENT_SESSION_KEY as `0x${string}`),
  token: { symbol: "tUSDC", address: "0xToken", decimals: 6 },
  payees: new AddressBook({ "acme-data": "0x..." }),
});

const p = await bank.pay({ id: "inv-1042", payee: "acme-data", amount: "12.50" });
await bank.waitFor(p.id); // settled | rejected | awaiting_cosign ...
```

## MCP server

The server exposes these tools: `list_payees`, `balance`, `pay`, `payment_status` and `history`.

```bash
BANK_URL=http://127.0.0.1:8080 BANK_RPC_URL=http://127.0.0.1:8545 BANK_CHAIN_ID=31337 \
AGENT_ACCOUNT=0x... AGENT_SESSION_KEY=0x... BANK_TOKEN=0x... \
BANK_PAYEES='{"acme-data":"0x..."}' npm run mcp
```

Register that command as a stdio MCP server in any MCP client.

## Tests

```bash
npm install
npm test          # EIP-712 parity with the contract, signing, amounts, address book, MCP tools
```

`scripts/smoke.ts` drives the MCP tools against a live `bankd`; `../scripts/e2e.sh` runs it.
