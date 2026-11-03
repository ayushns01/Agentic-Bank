import { hashTypedData, keccak256, toBytes, type Address, type Hex, type LocalAccount } from "viem";

/** EIP-712 types; must match AgentAccount.PAYMENT_INTENT_TYPEHASH exactly. */
export const paymentIntentTypes = {
  PaymentIntent: [
    { name: "token", type: "address" },
    { name: "to", type: "address" },
    { name: "amount", type: "uint256" },
    { name: "ref", type: "bytes32" },
    { name: "nonce", type: "uint256" },
    { name: "deadline", type: "uint64" },
  ],
} as const;

export interface PaymentIntent {
  token: Address;
  to: Address;
  amount: bigint;
  ref: Hex;
  nonce: bigint;
  deadline: bigint;
}

/** The ref the bank expects for a payment id: keccak256(utf8(id)). */
export function refFor(id: string): Hex {
  return keccak256(toBytes(id));
}

/** Each AgentAccount is its own EIP-712 domain, so a signature for one
 *  account (or chain) can never be replayed on another. */
export function domainFor(account: Address, chainId: number) {
  return { name: "AgentAccount", version: "1", chainId, verifyingContract: account } as const;
}

export function intentDigest(account: Address, chainId: number, intent: PaymentIntent): Hex {
  return hashTypedData({
    domain: domainFor(account, chainId),
    types: paymentIntentTypes,
    primaryType: "PaymentIntent",
    message: intent,
  });
}

/** Signs with the agent's session key. The result is 65-byte r||s||v with
 *  v in {27, 28}, the format AgentAccount verifies. */
export function signIntent(
  sessionKey: LocalAccount,
  account: Address,
  chainId: number,
  intent: PaymentIntent,
): Promise<Hex> {
  return sessionKey.signTypedData({
    domain: domainFor(account, chainId),
    types: paymentIntentTypes,
    primaryType: "PaymentIntent",
    message: intent,
  });
}

/** Parses a human decimal amount ("12.5") into base units, refusing anything
 *  ambiguous: signs, exponents, separators, or more decimals than the token
 *  has. Decimal mistakes are a classic way humans lose money on-chain. */
export function parseAmount(amount: string, decimals: number): bigint {
  const m = /^(\d+)(?:\.(\d+))?$/.exec(amount.trim());
  if (!m) throw new Error(`invalid amount "${amount}": use digits and an optional decimal point`);
  const [, whole, frac = ""] = m;
  if (frac.length > decimals) {
    throw new Error(`invalid amount "${amount}": at most ${decimals} decimal places`);
  }
  const value = BigInt(whole) * 10n ** BigInt(decimals) + BigInt(frac.padEnd(decimals, "0") || "0");
  if (value === 0n) throw new Error("amount must be greater than zero");
  return value;
}

export function formatAmount(units: bigint, decimals: number): string {
  const neg = units < 0n;
  const abs = neg ? -units : units;
  const base = 10n ** BigInt(decimals);
  const frac = (abs % base).toString().padStart(decimals, "0").replace(/0+$/, "");
  return `${neg ? "-" : ""}${abs / base}${frac ? "." + frac : ""}`;
}
