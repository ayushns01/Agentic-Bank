#!/usr/bin/env bash
# End-to-end run on a local anvil chain:
#   anvil -> deploy contracts -> bankd -> demo agent (open, fund, pay, co-sign)
#
# Requires anvil, forge and go on PATH (override with ANVIL=, FORGE=).
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
ANVIL=${ANVIL:-anvil}
FORGE=${FORGE:-forge}
FORGE_ARGS=${FORGE_ARGS:-}
PORT=${PORT:-8545}
API=${API:-8089}
RPC="http://127.0.0.1:$PORT"

# anvil's well-known dev keys: deployer/funder, relayer, account owner.
DEPLOYER_KEY=0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80
RELAYER_KEY=0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d
OWNER_KEY=0x5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a

work=$(mktemp -d)
pids=()
cleanup() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  rm -rf "$work"
}
trap cleanup EXIT

echo "==> anvil on :$PORT"
"$ANVIL" --port "$PORT" --silent &
pids+=($!)
for _ in $(seq 50); do
  curl -sf -X POST -H 'content-type: application/json' \
    --data '{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}' "$RPC" >/dev/null && break
  sleep 0.2
done

echo "==> deploy"
(cd "$root/contracts" && "$FORGE" script script/Deploy.s.sol $FORGE_ARGS \
  --rpc-url "$RPC" --private-key "$DEPLOYER_KEY" --broadcast) > "$work/deploy.log"
grep -E '^\s*(BANK_|FAUCET)' "$work/deploy.log" | sed 's/^ *//' > "$work/env"
cat "$work/env"
set -a; . "$work/env"; set +a

echo "==> bankd on :$API"
(cd "$root/services" && go build -o "$work/bankd" ./cmd/bankd && go build -o "$work/demo" ./cmd/demo)
BANK_RPC_URL="$RPC" BANK_RELAYER_KEY="$RELAYER_KEY" BANK_CONFIRMATIONS=0 \
  BANK_LISTEN="127.0.0.1:$API" "$work/bankd" 2> "$work/bankd.log" &
pids+=($!)
for _ in $(seq 50); do curl -sf "http://127.0.0.1:$API/healthz" >/dev/null && break; sleep 0.2; done

echo "==> demo agent"
DEMO_RPC_URL="$RPC" DEMO_BANK_URL="http://127.0.0.1:$API" \
  DEMO_OWNER_KEY="$OWNER_KEY" DEMO_FUNDER_KEY="$DEPLOYER_KEY" "$work/demo" || {
  echo "--- bankd log"; cat "$work/bankd.log"; exit 1;
}
