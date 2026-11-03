// Drives the MCP server the way an LLM client would, against a live bankd.
// Run by scripts/e2e.sh after the Go demo has opened and funded an account.
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { clientFromEnv, createBankMcpServer } from "../src/index.js";

function textOf(r: unknown): string {
  return (r as { content: Array<{ text: string }> }).content[0].text;
}

async function main() {
  const [a, b] = InMemoryTransport.createLinkedPair();
  await createBankMcpServer(clientFromEnv()).connect(a);
  const mcp = new Client({ name: "smoke", version: "0" });
  await mcp.connect(b);

  const call = async (name: string, args: Record<string, unknown> = {}) => {
    const res = await mcp.callTool({ name, arguments: args });
    console.log(`  ${name}(${JSON.stringify(args)}) -> ${textOf(res).replace(/\s+/g, " ")}`);
    return res;
  };

  await call("list_payees");
  await call("balance");

  const paid = await call("pay", { payee: "cafe-merchant", amount: "12.5", reference: `mcp-${Date.now()}` });
  if (paid.isError || JSON.parse(textOf(paid)).state !== "settled") throw new Error("MCP payment did not settle");

  const raw = await call("pay", { payee: "0x000000000000000000000000000000000000dEaD", amount: "1", reference: "raw-addr" });
  if (!raw.isError) throw new Error("raw address should have been refused");

  const big = await call("pay", { payee: "cafe-merchant", amount: "500", reference: `mcp-big-${Date.now()}` });
  if (JSON.parse(textOf(big)).state !== "rejected") throw new Error("over-limit payment should be rejected");

  await call("history", { limit: 3 });
  console.log("PASS: MCP smoke");
}

main().catch((e) => {
  console.error("FAIL:", e);
  process.exit(1);
});
