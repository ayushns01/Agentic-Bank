#!/bin/sh
# Copy contract ABIs from Foundry build output into the Go chain package.
# Run after `forge build` in contracts/.
set -e
root=$(cd "$(dirname "$0")/.." && pwd)
dst="$root/services/chain/abi"
mkdir -p "$dst"
bin="$root/services/chain/testdata"
mkdir -p "$bin"
for c in AgentAccount PolicyEngine AccountFactory Escrow TestToken Faucet; do
  python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1]))["abi"], indent=1))' \
    "$root/contracts/out/$c.sol/$c.json" > "$dst/$c.json"
  # Creation bytecode, used by the Go integration tests on a simulated chain.
  python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["bytecode"]["object"])' \
    "$root/contracts/out/$c.sol/$c.json" > "$bin/$c.bin"
done
echo "ABIs written to $dst"
