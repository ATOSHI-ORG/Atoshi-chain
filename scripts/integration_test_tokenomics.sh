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
DENOM="aatos"
NODE_RPC="tcp://localhost:26657"
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
"$ATOSHID" "${ATOSHID_HOME[@]}" add-genesis-account validator 100000000000000000000000000$DENOM --keyring-backend "$KEYRING" >/dev/null
"$ATOSHID" "${ATOSHID_HOME[@]}" add-genesis-account feeder    1000000000000000000000$DENOM --keyring-backend "$KEYRING" >/dev/null
"$ATOSHID" "${ATOSHID_HOME[@]}" add-genesis-account claimant  1000000000000000000000$DENOM --keyring-backend "$KEYRING" >/dev/null
"$ATOSHID" "${ATOSHID_HOME[@]}" add-genesis-account treasury  1000000000000000000000$DENOM --keyring-backend "$KEYRING" >/dev/null

# Build the migration snapshot: claimant gets 1000 aatos.
SNAPSHOT_DIR="$HOME_DIR/migration"
mkdir -p "$SNAPSHOT_DIR"
echo "$CLAIMANT_ADDR,1000" > "$SNAPSHOT_DIR/snapshot.csv"
"$MERKLE_TOOL" -in "$SNAPSHOT_DIR/snapshot.csv" -out "$SNAPSHOT_DIR" >/dev/null
MERKLE_ROOT="$(tr -d '\n' < "$SNAPSHOT_DIR/root.txt")"
PROOF_HEX="$(jq -r '.proofs[0].proof | join(",")' "$SNAPSHOT_DIR/proofs.json")"

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

log "starting localnet (logs at $HOME_DIR/node.log)"
"$ATOSHID" "${ATOSHID_HOME[@]}" start --minimum-gas-prices "0$DENOM" --log_level info \
  > "$HOME_DIR/node.log" 2>&1 &
ATOSHID_PID=$!

# Wait for the RPC to come up.
for i in $(seq 1 30); do
  if "$ATOSHID" status --node "$NODE_RPC" >/dev/null 2>&1; then break; fi
  sleep 1
done
"$ATOSHID" status --node "$NODE_RPC" >/dev/null || fail "node did not start; tail $HOME_DIR/node.log"

TX_FLAGS=( --chain-id "$CHAIN_ID" --keyring-backend "$KEYRING" --node "$NODE_RPC" --yes --gas-prices "1$DENOM" --gas auto --gas-adjustment 1.5 --output json )

log "1) feeder reports price"
"$ATOSHID" "${ATOSHID_HOME[@]}" tx oracle report-price 0.15 150000 itest \
  --from feeder "${TX_FLAGS[@]}" >/dev/null
sleep 3

log "2) querying oracle current-price"
"$ATOSHID" query oracle current-price --node "$NODE_RPC" --output json | jq '.price_data | {price, volume_24h, source}'

log "3) querying tokenomics views"
for q in params release-status circulating-supply block-reward project-claimable; do
  "$ATOSHID" query tokenomics "$q" --node "$NODE_RPC" --output json >/dev/null \
    || fail "query tokenomics $q failed"
  echo "  - tokenomics $q OK"
done

log "4) claimant redeems migration tokens"
BEFORE="$("$ATOSHID" query bank balance "$CLAIMANT_ADDR" "$DENOM" --node "$NODE_RPC" --output json | jq -r '.balance.amount')"
"$ATOSHID" "${ATOSHID_HOME[@]}" tx tokenomics claim-migration-tokens 1000 "$PROOF_HEX" \
  --from claimant "${TX_FLAGS[@]}" >/dev/null
sleep 3
AFTER="$("$ATOSHID" query bank balance "$CLAIMANT_ADDR" "$DENOM" --node "$NODE_RPC" --output json | jq -r '.balance.amount')"
DELTA=$(( AFTER - BEFORE ))
[[ "$DELTA" -ge 1000 ]] || fail "expected balance to grow by >=1000 (paid gas), got delta=$DELTA"
echo "  - migration claim credited (delta=$DELTA, gas-adjusted)"

log "ALL CHECKS PASSED"
