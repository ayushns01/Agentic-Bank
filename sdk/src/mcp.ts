#!/usr/bin/env node
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { getAddress, type Hex } from "viem";
import { privateKeyToAccount } from "viem/accounts";
import { z } from "zod";
import { AddressBook } from "./addressBook.js";
import { BankClient, BankError, type Payment } from "./client.js";
import { formatAmount } from "./intent.js";

/**
 * MCP tools that let an LLM agent bank safely:
 *  - it names payees from an address book, never raw addresses;
 *  - it states amounts as decimal strings, parsed strictly;
 *  - every payment still passes the bank's gates and the on-chain policy.
 * The LLM decides; deterministic code executes.
 */
export function createBankMcpServer(client: BankClient): McpServer {
  const server = new McpServer({ name: "agentic-bank", version: "0.1.0" });
  const { symbol, decimals } = client.token;
  const text = (v: unknown) => ({ content: [{ type: "text" as const, text: typeof v === "string" ? v : JSON.stringify(v, null, 2) }] });
  const fail = (e: unknown) => ({
    isError: true,
    content: [{ type: "text" as const, text: e instanceof BankError ? `bank refused (${e.status}): ${e.message}` : String((e as Error).message ?? e) }],
  });
  const summarize = (p: Payment) => ({
    id: p.id,
    state: p.state,
    amount: `${formatAmount(BigInt(p.amount), decimals)} ${symbol}`,
    txHash: p.txHash,
    reason: p.history.at(-1)?.reason,
    next:
      p.state === "awaiting_cosign" ? "This amount needs the account owner's co-signature; they have been asked to approve it."
      : p.state === "rejected" ? "The bank rejected this payment; do not retry the same payment."
      : undefined,
  });

  server.registerTool(
    "list_payees",
    { description: "List the payees this agent may pay, by name.", inputSchema: {} },
    async () => text(client.payees.entries()),
  );

  server.registerTool(
    "balance",
    { description: `Show this agent's account balance in ${symbol}.`, inputSchema: {} },
    async () => {
      try {
        const b = await client.balance();
        if (!b) return text(`No ${symbol} balance yet.`);
        return text({
          available: `${formatAmount(BigInt(b.available), decimals)} ${symbol}`,
          onChain: `${formatAmount(BigInt(b.onChain), decimals)} ${symbol}`,
        });
      } catch (e) {
        return fail(e);
      }
    },
  );

  server.registerTool(
    "pay",
    {
      description:
        `Pay a named payee in ${symbol}. Spending limits and owner co-signature are enforced by the bank and on-chain; ` +
        "a refusal is final. Reuse the same reference to retry safely.",
      inputSchema: {
        payee: z.string().describe("Payee name from list_payees"),
        amount: z.string().describe(`Decimal amount in ${symbol}, e.g. "12.50"`),
        reference: z.string().min(3).max(80).describe("Unique reference, e.g. an invoice number"),
      },
    },
    async ({ payee, amount, reference }) => {
      try {
        const p = await client.pay({ id: reference, payee, amount });
        const done = p.state === "broadcast" ? await client.waitFor(p.id, { timeoutMs: 30_000 }) : p;
        return text(summarize(done));
      } catch (e) {
        return fail(e);
      }
    },
  );

  server.registerTool(
    "payment_status",
    { description: "Look up a payment by its reference.", inputSchema: { reference: z.string() } },
    async ({ reference }) => {
      try {
        return text(summarize(await client.payment(reference)));
      } catch (e) {
        return fail(e);
      }
    },
  );

  server.registerTool(
    "history",
    { description: "Recent entries on this agent's account statement.", inputSchema: { limit: z.number().int().min(1).max(50).optional() } },
    async ({ limit }) => {
      try {
        const entries = await client.statement();
        return text(entries.slice(-(limit ?? 10)).map((e) => ({ id: e.id, memo: e.memo })));
      } catch (e) {
        return fail(e);
      }
    },
  );

  return server;
}

/** Builds a client from environment variables (see README). */
export function clientFromEnv(env: NodeJS.ProcessEnv = process.env): BankClient {
  const need = (k: string) => {
    const v = env[k];
    if (!v) throw new Error(`${k} is required`);
    return v;
  };
  const payees = env.BANK_PAYEES_FILE
    ? JSON.parse(readFileSync(env.BANK_PAYEES_FILE, "utf8"))
    : JSON.parse(env.BANK_PAYEES ?? "{}");
  return new BankClient({
    bankUrl: need("BANK_URL"),
    rpcUrl: need("BANK_RPC_URL"),
    chainId: Number(need("BANK_CHAIN_ID")),
    account: getAddress(need("AGENT_ACCOUNT")),
    sessionKey: privateKeyToAccount(need("AGENT_SESSION_KEY") as Hex),
    token: {
      symbol: env.BANK_TOKEN_SYMBOL ?? "tUSDC",
      address: getAddress(need("BANK_TOKEN")),
      decimals: Number(env.BANK_TOKEN_DECIMALS ?? 6),
    },
    payees: new AddressBook(payees),
  });
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  const server = createBankMcpServer(clientFromEnv());
  await server.connect(new StdioServerTransport());
}
