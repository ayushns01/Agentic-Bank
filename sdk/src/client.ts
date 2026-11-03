import { createPublicClient, http, type Address, type LocalAccount, type PublicClient } from "viem";
import { AddressBook } from "./addressBook.js";
import { parseAmount, refFor, signIntent, type PaymentIntent } from "./intent.js";

export interface Token {
  symbol: string;
  address: Address;
  decimals: number;
}

export interface BankClientOptions {
  /** bankd REST base URL, e.g. http://127.0.0.1:8080 */
  bankUrl: string;
  /** JSON-RPC URL, used to read the session key's nonce. */
  rpcUrl: string;
  chainId: number;
  /** The AgentAccount this agent spends from. */
  account: Address;
  /** The agent's session key (never the owner key). */
  sessionKey: LocalAccount;
  token: Token;
  payees: AddressBook;
  fetch?: typeof fetch;
}

export type PaymentState =
  | "created" | "validated" | "screening" | "awaiting_cosign" | "signed" | "broadcast"
  | "confirmed" | "reconciled" | "settled" | "rejected" | "held" | "returned" | "exception";

export interface Payment {
  id: string;
  state: PaymentState;
  payee: string;
  asset: string;
  amount: string;
  txHash?: string;
  ref: string;
  history: Array<{ from: string; to: string; reason?: string; at: string }>;
}

export interface Balance {
  token: Address;
  onChain: string;
  owed: string;
  available: string;
}

export interface StatementEntry {
  id: string;
  ref?: string;
  memo?: string;
  lines: Array<{ account: string; side: "debit" | "credit"; amount: string }>;
}

export class BankError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

const nonceAbi = [
  {
    type: "function",
    name: "nonces",
    stateMutability: "view",
    inputs: [{ name: "", type: "address" }],
    outputs: [{ name: "", type: "uint256" }],
  },
] as const;

const terminal: PaymentState[] = ["settled", "rejected", "returned", "exception"];

export class BankClient {
  private readonly rpc: PublicClient;
  private readonly fetch: typeof fetch;

  constructor(private readonly opts: BankClientOptions) {
    this.rpc = createPublicClient({ transport: http(opts.rpcUrl) });
    this.fetch = opts.fetch ?? fetch;
  }

  get token(): Token {
    return this.opts.token;
  }

  get payees(): AddressBook {
    return this.opts.payees;
  }

  /** Next unused nonce of this agent's session key, read from chain. */
  nonce(): Promise<bigint> {
    return this.rpc.readContract({
      address: this.opts.account,
      abi: nonceAbi,
      functionName: "nonces",
      args: [this.opts.sessionKey.address],
    });
  }

  /**
   * Pay a named payee. Amount is a decimal string in token units ("12.50").
   * The id is the idempotency key: retrying with the same id never pays twice.
   */
  async pay(p: { id: string; payee: string; amount: string; deadlineSeconds?: number }): Promise<Payment> {
    const to = this.opts.payees.resolve(p.payee);
    const intent: PaymentIntent = {
      token: this.opts.token.address,
      to,
      amount: parseAmount(p.amount, this.opts.token.decimals),
      ref: refFor(p.id),
      nonce: await this.nonce(),
      deadline: BigInt(Math.floor(Date.now() / 1000) + (p.deadlineSeconds ?? 3600)),
    };
    const sessionSig = await signIntent(this.opts.sessionKey, this.opts.account, this.opts.chainId, intent);
    return this.request<Payment>("POST", "/v1/payments", {
      id: p.id,
      account: this.opts.account,
      token: intent.token,
      to: intent.to,
      amount: intent.amount.toString(),
      nonce: intent.nonce.toString(),
      deadline: Number(intent.deadline),
      sessionSig,
    }, [201, 202, 422]);
  }

  payment(id: string): Promise<Payment> {
    return this.request<Payment>("GET", `/v1/payments/${encodeURIComponent(id)}`);
  }

  /** Poll until the payment reaches one of `until` (default: any terminal state). */
  async waitFor(id: string, opts: { until?: PaymentState[]; timeoutMs?: number; intervalMs?: number } = {}): Promise<Payment> {
    const until = opts.until ?? terminal;
    const deadline = Date.now() + (opts.timeoutMs ?? 60_000);
    for (;;) {
      const p = await this.payment(id);
      if (until.includes(p.state) || terminal.includes(p.state)) return p;
      if (Date.now() > deadline) throw new Error(`payment ${id} still ${p.state} after timeout`);
      await new Promise((r) => setTimeout(r, opts.intervalMs ?? 500));
    }
  }

  async balance(): Promise<Balance | undefined> {
    const acct = await this.request<{ balances?: Balance[] }>("GET", `/v1/accounts/${this.opts.account}`);
    return acct.balances?.find((b) => b.token.toLowerCase() === this.opts.token.address.toLowerCase());
  }

  statement(): Promise<StatementEntry[]> {
    return this.request<StatementEntry[]>(
      "GET",
      `/v1/accounts/${this.opts.account}/statement?token=${this.opts.token.address}`,
    );
  }

  private async request<T>(method: string, path: string, body?: unknown, ok: number[] = [200]): Promise<T> {
    const res = await this.fetch(this.opts.bankUrl.replace(/\/$/, "") + path, {
      method,
      headers: body ? { "content-type": "application/json" } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
    const data = await res.json().catch(() => ({}));
    if (!ok.includes(res.status)) throw new BankError(res.status, (data as { error?: string }).error ?? res.statusText);
    return data as T;
  }
}
