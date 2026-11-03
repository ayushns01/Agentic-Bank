import { describe, expect, it, vi } from "vitest";
import { concat, encodeAbiParameters, getAddress, keccak256, recoverTypedDataAddress, toBytes, type Address } from "viem";
import { generatePrivateKey, privateKeyToAccount } from "viem/accounts";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import {
  AddressBook,
  BankClient,
  createBankMcpServer,
  domainFor,
  formatAmount,
  intentDigest,
  parseAmount,
  paymentIntentTypes,
  refFor,
  signIntent,
  type PaymentIntent,
} from "../src/index.js";

const account = "0x1000000000000000000000000000000000000001" as Address;
const token = "0x3000000000000000000000000000000000000003" as Address;
const merchant = getAddress("0x000000000000000000000000000000000000cafe");

const intent: PaymentIntent = {
  token,
  to: merchant,
  amount: 25_000_000n,
  ref: refFor("inv-1"),
  nonce: 3n,
  deadline: 1_900_000_000n,
};

describe("EIP-712 intent", () => {
  it("matches the contract's type string and domain", () => {
    // Recompute exactly as AgentAccount.intentDigest does on-chain.
    const typeHash = keccak256(
      toBytes("PaymentIntent(address token,address to,uint256 amount,bytes32 ref,uint256 nonce,uint64 deadline)"),
    );
    const domainHash = keccak256(
      encodeAbiParameters(
        [{ type: "bytes32" }, { type: "bytes32" }, { type: "bytes32" }, { type: "uint256" }, { type: "address" }],
        [
          keccak256(toBytes("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")),
          keccak256(toBytes("AgentAccount")),
          keccak256(toBytes("1")),
          31337n,
          account,
        ],
      ),
    );
    const structHash = keccak256(
      encodeAbiParameters(
        [{ type: "bytes32" }, { type: "address" }, { type: "address" }, { type: "uint256" }, { type: "bytes32" }, { type: "uint256" }, { type: "uint64" }],
        [typeHash, intent.token, intent.to, intent.amount, intent.ref, intent.nonce, intent.deadline],
      ),
    );
    const expected = keccak256(concat(["0x1901", domainHash, structHash]));
    expect(intentDigest(account, 31337, intent)).toBe(expected);
  });

  it("signs in the contract's r||s||v format", async () => {
    const key = privateKeyToAccount(generatePrivateKey());
    const sig = await signIntent(key, account, 31337, intent);
    expect(sig.length).toBe(2 + 65 * 2);
    expect([27, 28]).toContain(parseInt(sig.slice(-2), 16));
    const signer = await recoverTypedDataAddress({
      domain: domainFor(account, 31337),
      types: paymentIntentTypes,
      primaryType: "PaymentIntent",
      message: intent,
      signature: sig,
    });
    expect(signer).toBe(key.address);
  });

  it("binds signatures to one account and chain", () => {
    const other = "0x2000000000000000000000000000000000000002" as Address;
    expect(intentDigest(account, 31337, intent)).not.toBe(intentDigest(other, 31337, intent));
    expect(intentDigest(account, 31337, intent)).not.toBe(intentDigest(account, 8453, intent));
  });

  it("derives the ref from the payment id", () => {
    expect(refFor("inv-1")).toBe(keccak256(toBytes("inv-1")));
    expect(refFor("inv-1")).not.toBe(refFor("inv-2"));
  });
});

describe("amounts", () => {
  it("parses strictly", () => {
    expect(parseAmount("12.5", 6)).toBe(12_500_000n);
    expect(parseAmount("0.000001", 6)).toBe(1n);
    expect(parseAmount("1000", 6)).toBe(1_000_000_000n);
    for (const bad of ["", "-1", "1e6", "1,000", "0.0000001", "0", ".5", "5.", "abc"]) {
      expect(() => parseAmount(bad, 6), bad).toThrow();
    }
  });

  it("formats base units", () => {
    expect(formatAmount(12_500_000n, 6)).toBe("12.5");
    expect(formatAmount(1_000_000_000n, 6)).toBe("1000");
    expect(formatAmount(1n, 6)).toBe("0.000001");
  });
});

describe("address book", () => {
  it("resolves names and refuses raw addresses", () => {
    const book = new AddressBook({ "acme-data": merchant.toLowerCase() });
    expect(book.resolve("acme-data")).toBe(merchant);
    expect(() => book.resolve(merchant)).toThrow(/raw addresses/);
    expect(() => book.resolve("someone-else")).toThrow(/unknown payee/);
  });

  it("validates entries", () => {
    const book = new AddressBook();
    expect(() => book.add("Bad Name", merchant)).toThrow();
    expect(() => book.add("x", "0x123")).toThrow();
    book.add("x", merchant);
    expect(() => book.add("x", account)).toThrow(/already maps/);
  });
});

describe("MCP server", () => {
  function setup(responses: Record<string, unknown>) {
    const calls: Array<{ url: string; body?: unknown }> = [];
    const fakeFetch = vi.fn(async (url: string | URL, init?: RequestInit) => {
      const u = String(url);
      calls.push({ url: u, body: init?.body ? JSON.parse(String(init.body)) : undefined });
      const key = Object.keys(responses).find((k) => u.includes(k))!;
      const [status, body] = responses[key] as [number, unknown];
      return new Response(JSON.stringify(body), { status });
    }) as unknown as typeof fetch;
    const client = new BankClient({
      bankUrl: "http://bank",
      rpcUrl: "http://127.0.0.1:1",
      chainId: 31337,
      account,
      sessionKey: privateKeyToAccount(generatePrivateKey()),
      token: { symbol: "tUSDC", address: token, decimals: 6 },
      payees: new AddressBook({ "acme-data": merchant }),
      fetch: fakeFetch,
    });
    vi.spyOn(client, "nonce").mockResolvedValue(7n);
    return { client, calls };
  }

  async function connect(client: BankClient) {
    const [a, b] = InMemoryTransport.createLinkedPair();
    await createBankMcpServer(client).connect(a);
    const mcp = new Client({ name: "test", version: "0" });
    await mcp.connect(b);
    return mcp;
  }

  const textOf = (r: unknown) => (r as { content: Array<{ text: string }> }).content[0].text;

  it("exposes the banking tools", async () => {
    const mcp = await connect(setup({}).client);
    const { tools } = await mcp.listTools();
    expect(tools.map((t) => t.name).sort()).toEqual(["balance", "history", "list_payees", "pay", "payment_status"]);
  });

  it("pays a named payee with a strictly parsed amount", async () => {
    const payment = { id: "inv-9", state: "settled", payee: merchant, asset: token, amount: "12500000", ref: refFor("inv-9"), history: [] };
    const { client, calls } = setup({ "/v1/payments/inv-9": [200, payment], "/v1/payments": [201, payment] });
    const mcp = await connect(client);
    const res = await mcp.callTool({ name: "pay", arguments: { payee: "acme-data", amount: "12.5", reference: "inv-9" } });
    expect(JSON.parse(textOf(res)).amount).toBe("12.5 tUSDC");
    const body = calls[0].body as Record<string, string>;
    expect(body.to.toLowerCase()).toBe(merchant.toLowerCase());
    expect(body.amount).toBe("12500000");
    expect(body.nonce).toBe("7");
    expect(body.sessionSig).toMatch(/^0x[0-9a-f]{130}$/);
  });

  it("refuses raw addresses and bad amounts without calling the bank", async () => {
    const { client, calls } = setup({});
    const mcp = await connect(client);
    let res = await mcp.callTool({ name: "pay", arguments: { payee: merchant, amount: "1", reference: "r-1" } });
    expect(res.isError).toBe(true);
    expect(textOf(res)).toMatch(/raw addresses/);
    res = await mcp.callTool({ name: "pay", arguments: { payee: "acme-data", amount: "1e9", reference: "r-2" } });
    expect(res.isError).toBe(true);
    expect(calls).toHaveLength(0);
  });

  it("explains rejections and co-sign holds", async () => {
    const rejected = { id: "big", state: "rejected", payee: merchant, asset: token, amount: "900000000", ref: "0x", history: [{ from: "screening", to: "rejected", reason: "policy: PerTxLimit", at: "" }] };
    const { client } = setup({ "/v1/payments": [422, rejected] });
    const mcp = await connect(client);
    const res = JSON.parse(textOf(await mcp.callTool({ name: "pay", arguments: { payee: "acme-data", amount: "900", reference: "big" } })));
    expect(res.state).toBe("rejected");
    expect(res.reason).toMatch(/PerTxLimit/);
    expect(res.next).toMatch(/do not retry/);
  });
});
