#!/usr/bin/env bash
# ============================================================================
# Atoshi tokenomics + oracle integration test (localnet)
# ============================================================================
# End-to-end smoke test that exercises the new modules over a single-node
# localnet. Verifies:
#   1. Both modules register Msg/Query gRPC routes (proto codegen sanity).
#   2. A whitelisted feeder can submit MsgReportPrice and the value is queried.
#   3. tokenomics queries (params, release-status, circulating-supply,
#      block-reward, project-claimable) all return successfully.
#   4. A user can submit MsgClaimMigrationTokens with a Merkle proof generated
#      by cmd/migration-merkle and receive ATOS from the migration pool.
#
# Usage:
#   ./scripts/integration_test_tokenomics.sh [--no-build]
#
# Requirements: go, jq. Uses a throwaway HOME_DIR under /tmp.
# ============================================================================
set -euo pipefail

NO_BUILD=0
for arg in "$@"; do
  case "$arg" in
    --no-build) NO_BUILD=1 ;;
    *) echo "unknown arg: $arg"; exit 2 ;;
  esac
done

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

CHAIN_ID="atoshi_88388-1"
HOME_DIR="$(mktemp -d /tmp/atoshid-itest.XXXXXX)"
KEYRING="test"
DENOM="liao"
# Use NON-default ports so we don't collide with a running local_node.sh
# instance on the same machine. Shift everything by +30 from defaults.
RPC_PORT=26687     # CometBFT RPC      (default 26657)
P2P_PORT=26686     # CometBFT P2P      (default 26656)
ABCI_PORT=26688    # ABCI proxy_app    (default 26658)
GRPC_PORT=9092     # Cosmos gRPC       (default 9090)
API_PORT=1347      # Cosmos REST       (default 1317)
EVM_PORT=8575      # Ethereum JSON-RPC (default 8545)
NODE_RPC="tcp://localhost:${RPC_PORT}"
ATOSHID="$ROOT/build/atoshid"
MERKLE_TOOL="$ROOT/build/migration-merkle"

cleanup() {
  if [[ -n "${ATOSHID_PID:-}" ]] && kill -0 "$ATOSHID_PID" 2>/dev/null; then
    kill "$ATOSHID_PID" >/dev/null 2>&1 || true
    wait "$ATOSHID_PID" 2>/dev/null || true
  fi
  rm -rf "$HOME_DIR"
}
trap cleanup EXIT

log() { printf '\n\033[1;34m[itest]\033[0m %s\n' "$*"; }
fail() { printf '\n\033[1;31m[fail]\033[0m %s\n' "$*"; exit 1; }

if [[ "$NO_BUILD" -eq 0 ]]; then
  log "building atoshid + migration-merkle"
  mkdir -p build
  go build -o "$ATOSHID" ./cmd/atoshid
  go build -o "$MERKLE_TOOL" ./cmd/migration-merkle
fi

[[ -x "$ATOSHID" ]] || fail "atoshid not built at $ATOSHID"
[[ -x "$MERKLE_TOOL" ]] || fail "migration-merkle not built at $MERKLE_TOOL"

ATOSHID_HOME=( --home "$HOME_DIR" )

log "initializing chain $CHAIN_ID under $HOME_DIR"
"$ATOSHID" "${ATOSHID_HOME[@]}" init itest --chain-id "$CHAIN_ID" >/dev/null

# Add validator + feeder + claimant + treasury keys.
for key in validator feeder claimant treasury; do
  "$ATOSHID" "${ATOSHID_HOME[@]}" keys add "$key" --keyring-backend "$KEYRING" --algo eth_secp256k1 >/dev/null
done
VAL_ADDR="$("$ATOSHID" "${ATOSHID_HOME[@]}" keys show validator -a --keyring-backend "$KEYRING")"
FEEDER_ADDR="$("$ATOSHID" "${ATOSHID_HOME[@]}" keys show feeder -a --keyring-backend "$KEYRING")"
CLAIMANT_ADDR="$("$ATOSHID" "${ATOSHID_HOME[@]}" keys show claimant -a --keyring-backend "$KEYRING")"
TREASURY_ADDR="$("$ATOSHID" "${ATOSHID_HOME[@]}" keys show treasury -a --keyring-backend "$KEYRING")"

log "validator=$VAL_ADDR feeder=$FEEDER_ADDR claimant=$CLAIMANT_ADDR"

# Give validator a generous staking balance and the feeder/treasury enough
# fee gas to broadcast.
GENESIS="$HOME_DIR/config/genesis.json"
"$ATOSHID" "${ATOSHID_HOME[@]}" add-genesis-account "$VAL_ADDR"      100000000000000000000000000$DENOM >/dev/null
"$ATOSHID" "${ATOSHID_HOME[@]}" add-genesis-account "$FEEDER_ADDR"   1000000000000000000000$DENOM >/dev/null
"$ATOSHID" "${ATOSHID_HOME[@]}" add-genesis-account "$CLAIMANT_ADDR" 1000000000000000000000$DENOM >/dev/null
"$ATOSHID" "${ATOSHID_HOME[@]}" add-genesis-account "$TREASURY_ADDR" 1000000000000000000000$DENOM >/dev/null

# Build the migration snapshot. Need ≥2 entries because the on-chain
# ValidateBasic rejects an empty Merkle proof; with 1 entry the leaf
# IS the root and the proof is empty. Use validator's address as the
# second filler entry.
SNAPSHOT_DIR="$HOME_DIR/migration"
mkdir -p "$SNAPSHOT_DIR"
{
  echo "$CLAIMANT_ADDR,1000"
  echo "$VAL_ADDR,2000"
} > "$SNAPSHOT_DIR/snapshot.csv"
"$MERKLE_TOOL" -in "$SNAPSHOT_DIR/snapshot.csv" -out "$SNAPSHOT_DIR" >/dev/null
MERKLE_ROOT="$(tr -d '\n' < "$SNAPSHOT_DIR/root.txt")"
# Find the claimant's proof in proofs.json (entry order is preserved).
PROOF_HEX="$(jq -r --arg a "$CLAIMANT_ADDR" '.proofs[] | select(.claimer==$a) | .proof | join(",")' "$SNAPSHOT_DIR/proofs.json")"
[[ -n "$PROOF_HEX" ]] || fail "claimant proof not found in proofs.json"

log "patching genesis: feeder whitelist + treasury + merkle root"
jq --arg feeder "$FEEDER_ADDR" \
   --arg treasury "$TREASURY_ADDR" \
   --arg root "$MERKLE_ROOT" '
  .app_state.oracle.params.allowed_feeders = [$feeder]
  | .app_state.tokenomics.params.project_treasury_address = $treasury
  | .app_state.tokenomics.params.migration_merkle_root = $root
  | .app_state.tokenomics.params.price_check_epoch_blocks = 5
' "$GENESIS" > "$GENESIS.tmp" && mv "$GENESIS.tmp" "$GENESIS"

"$ATOSHID" "${ATOSHID_HOME[@]}" gentx validator 100000000000000000000$DENOM \
  --chain-id "$CHAIN_ID" --keyring-backend "$KEYRING" >/dev/null
"$ATOSHID" "${ATOSHID_HOME[@]}" collect-gentxs >/dev/null
"$ATOSHID" "${ATOSHID_HOME[@]}" validate-genesis >/dev/null

# Rewrite EVERY listen port in config.toml / app.toml to avoid colliding
# with another atoshid (e.g. local_node.sh) on the default ports.
CONFIG="$HOME_DIR/config/config.toml"
APP_TOML="$HOME_DIR/config/app.toml"
sed -i.bak "s|tcp://127.0.0.1:26657|tcp://127.0.0.1:${RPC_PORT}|g"  "$CONFIG"
sed -i.bak "s|tcp://0.0.0.0:26656|tcp://0.0.0.0:${P2P_PORT}|g"      "$CONFIG"
sed -i.bak "s|tcp://127.0.0.1:26658|tcp://127.0.0.1:${ABCI_PORT}|g" "$CONFIG"
sed -i.bak "s|localhost:9090|localhost:${GRPC_PORT}|g"              "$APP_TOML"
sed -i.bak "s|tcp://localhost:1317|tcp://localhost:${API_PORT}|g"   "$APP_TOML"
sed -i.bak "s|127.0.0.1:8545|127.0.0.1:${EVM_PORT}|g"               "$APP_TOML"
rm -f "$CONFIG.bak" "$APP_TOML.bak"

log "starting localnet on rpc=${RPC_PORT} (logs at $HOME_DIR/node.log)"
# --chain-id is REQUIRED. Atoshi's AtoshiAppOptions(chainID) runs in
# NewAtoshi() before genesis.json is parsed, so without the flag the
# chain id is empty and the app panics with "unknown chain id:".
"$ATOSHID" "${ATOSHID_HOME[@]}" start --chain-id "$CHAIN_ID" \
  --minimum-gas-prices "0$DENOM" --log_level info \
  > "$HOME_DIR/node.log" 2>&1 &
ATOSHID_PID=$!

# Wait for RPC. Dump node.log if the process dies during startup.
NODE_UP=0
for i in $(seq 1 30); do
  if ! kill -0 "$ATOSHID_PID" 2>/dev/null; then
    echo "----- node.log (process exited) -----"
    tail -50 "$HOME_DIR/node.log" || true
    fail "atoshid process died during startup; see log above"
  fi
  if "$ATOSHID" status --home "$HOME_DIR" --node "$NODE_RPC" >/dev/null 2>&1; then
    NODE_UP=1; break
  fi
  sleep 1
done
if [[ "$NODE_UP" -ne 1 ]]; then
  echo "----- node.log (timeout, process still running) -----"
  tail -80 "$HOME_DIR/node.log" || true
  fail "node RPC did not respond on $NODE_RPC after 30s"
fi

# Confirm we are talking to OUR node and not another atoshid on the host.
ACTUAL_CHAIN=$("$ATOSHID" status --home "$HOME_DIR" --node "$NODE_RPC" --output json 2>/dev/null \
  | jq -r '.NodeInfo.network // .node_info.network // ""')
[[ "$ACTUAL_CHAIN" == "$CHAIN_ID" ]] || fail "RPC at $NODE_RPC reports chain_id='$ACTUAL_CHAIN' (expected '$CHAIN_ID') — wrong node?"

# Wait for the first block. Tx simulation (--gas auto) errors with
# "atoshid is not ready; please wait for first block: invalid height"
# if we broadcast before block 1.
log "waiting for first block..."
for i in $(seq 1 30); do
  HEIGHT=$("$ATOSHID" status --home "$HOME_DIR" --node "$NODE_RPC" --output json 2>/dev/null \
    | jq -r '.SyncInfo.latest_block_height // .sync_info.latest_block_height // "0"')
  [[ "$HEIGHT" =~ ^[0-9]+$ ]] && [[ "$HEIGHT" -ge 1 ]] && break
  sleep 1
done
[[ "$HEIGHT" -ge 1 ]] || fail "no block produced after 30s; tail $HOME_DIR/node.log"
echo "  chain at height $HEIGHT"

# Use a fixed gas limit (--gas auto needs the simulate gRPC path, which
# atoshid does not expose; it returns "unknown query path"). 500k covers
# every tx in this script.
TX_FLAGS=( --home "$HOME_DIR" --chain-id "$CHAIN_ID" --keyring-backend "$KEYRING" --node "$NODE_RPC" --yes --gas 500000 --gas-prices "1$DENOM" --output json )
QUERY_FLAGS=( --home "$HOME_DIR" --node "$NODE_RPC" --output json )

log "1) feeder reports price"
"$ATOSHID" "${ATOSHID_HOME[@]}" tx oracle report-price 0.15 150000 itest \
  --from feeder "${TX_FLAGS[@]}" >/dev/null
sleep 3

log "2) querying oracle current-price"
"$ATOSHID" query oracle current-price "${QUERY_FLAGS[@]}" | jq '.price_data | {price, volume_24h, source}'

log "3) querying tokenomics views"
for q in params release-status circulating-supply block-reward project-claimable; do
  "$ATOSHID" query tokenomics "$q" "${QUERY_FLAGS[@]}" >/dev/null \
    || fail "query tokenomics $q failed"
  echo "  - tokenomics $q OK"
done

log "4) claimant redeems migration tokens"
BEFORE="$("$ATOSHID" query bank balance "$CLAIMANT_ADDR" "$DENOM" "${QUERY_FLAGS[@]}" | jq -r '.balance.amount')"
# Capture the tx output so we can inspect failures instead of silently
# hiding them. Migration claim is in the SubsidizedMsgTypeUrls list, so
# claimant pays NO gas — balance should grow by exactly 1000.
CLAIM_OUT=$("$ATOSHID" "${ATOSHID_HOME[@]}" tx tokenomics claim-migration-tokens 1000 "$PROOF_HEX" \
  --from claimant "${TX_FLAGS[@]}" 2>&1) || {
    echo "----- claim tx output -----"
    echo "$CLAIM_OUT"
    fail "claim-migration-tokens broadcast failed"
  }
TXHASH=$(echo "$CLAIM_OUT" | jq -r '.txhash // empty' 2>/dev/null)
sleep 3
# Verify the tx actually succeeded on-chain (code 0).
if [[ -n "$TXHASH" ]]; then
  CODE=$("$ATOSHID" query tx "$TXHASH" "${QUERY_FLAGS[@]}" 2>/dev/null | jq -r '.code // 999')
  if [[ "$CODE" != "0" ]]; then
    echo "----- tx $TXHASH failed with code $CODE -----"
    "$ATOSHID" query tx "$TXHASH" "${QUERY_FLAGS[@]}" | jq '{code, raw_log, gas_used}'
    fail "claim tx reverted on-chain"
  fi
fi
AFTER="$("$ATOSHID" query bank balance "$CLAIMANT_ADDR" "$DENOM" "${QUERY_FLAGS[@]}" | jq -r '.balance.amount')"
DELTA=$(( AFTER - BEFORE ))
[[ "$DELTA" -ge 1000 ]] || fail "expected balance to grow by >=1000, got delta=$DELTA (txhash=$TXHASH)"
echo "  - migration claim credited (delta=$DELTA, txhash=$TXHASH)"

log "ALL CHECKS PASSED"
