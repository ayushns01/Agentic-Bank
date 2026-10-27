# Trade finance

The counterparties are agents, or the businesses they act for. Release of funds is tied to verifiable events.

| Instrument | Flow | Release condition |
|---|---|---|
| **Escrow** | Buyer funds escrow, seller delivers, funds are released | ERC-8004 validation response ≥ threshold, buyer approval, or timeout in seller's favor without a dispute |
| **Letter of credit** | Issuer (credit pool or buyer's bank agent) commits to pay the seller on presentation of documents | Attestations (EIP-712 signed by named attesters: shipper, inspector, oracle) matching the LC terms before expiry |
| **Milestone payments** | A contract splits the total into milestones | Per-milestone attestation |
| **Payment schedules** | Loans, bills and bonds settled on dated schedules | Time, via `CashflowStream` / loan instruments |
| **Invoice factoring** | Seller sells a receivable (an obligation NFT) to the credit pool at a discount `F(1 − d·t)` | Payer pays the NFT holder at maturity |
| **Supply-chain finance** | Buyer-approved invoices financed early at the buyer's credit spread | Buyer approval + maturity |

## Disputes

There are two ways to resolve a dispute:
- **Arbiter:** a named arbiter (an address or a validator contract) splits the escrowed funds.
- **Validation:** an ERC-8004 Validation Registry response decides automatically.

Both paths emit events that the ledger turns into entries, and both feed the counterparties' reputation.

## Settlement and the ledger

Every instrument payment follows the same lifecycle (Validated → … → Settled) as an ordinary payment. The obligation side is tracked in the ledger as a receivable or payable that the instrument creates and its payments run down.
