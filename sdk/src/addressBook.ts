import { getAddress, isAddress, type Address } from "viem";

/**
 * Named counterparties. Agents pay "acme-data" rather than a 42-character hex
 * string they might mangle or be tricked into (address poisoning). Entries
 * are checksummed when added; lookups are by exact name.
 */
export class AddressBook {
  private readonly byName = new Map<string, Address>();

  constructor(entries: Record<string, string> = {}) {
    for (const [name, addr] of Object.entries(entries)) this.add(name, addr);
  }

  add(name: string, address: string): void {
    if (!/^[a-z0-9][a-z0-9._-]{0,63}$/.test(name)) {
      throw new Error(`invalid payee name "${name}": lowercase letters, digits, . _ -`);
    }
    if (!isAddress(address, { strict: false })) throw new Error(`invalid address for "${name}"`);
    const existing = this.byName.get(name);
    const checksummed = getAddress(address);
    if (existing && existing !== checksummed) throw new Error(`payee "${name}" already maps to ${existing}`);
    this.byName.set(name, checksummed);
  }

  /** Resolve a payee name. Raw addresses are refused on purpose. */
  resolve(name: string): Address {
    const addr = this.byName.get(name);
    if (addr) return addr;
    if (isAddress(name, { strict: false })) {
      throw new Error("raw addresses are not accepted; add the payee to the address book and pay it by name");
    }
    throw new Error(`unknown payee "${name}"`);
  }

  names(): string[] {
    return [...this.byName.keys()].sort();
  }

  entries(): Array<{ name: string; address: Address }> {
    return this.names().map((name) => ({ name, address: this.byName.get(name)! }));
  }
}
