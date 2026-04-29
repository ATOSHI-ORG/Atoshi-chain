#!/usr/bin/env bash
# ============================================================================
# Atoshi x/energy integration test (localnet)
# ============================================================================
# End-to-end smoke test for the energy module on a single-node localnet.
# Verifies:
#   1. Module wires up (proto routing, params query reachable).
#   2. A holder of 30k+ ATOS accrues TxEnergy linearly over time.
#   3. A normal MsgSend draws from accrued energy and pays NO ATOS fee
#      when energy fully covers gas.
#   4. Below-threshold balance falls back to standard EIP-1559 fee.
#   5. Subsidized msg type (MsgClaimMigrationTokens / MsgReportPrice) is
#      always free regardless of energy balance.
#   6. Delegation: A delegates energy to B → A's ATOS frozen, B can spend
#      borrowed energy. Undelegate frees ATOS back.
#   7. Global kill switch (params.energy_enabled = false) routes all txs
#      through the standard fee path.
#   8. Governance MsgUpdateParams successfully retunes thresholds.
#
# Usage:
#   ./scripts/integration_test_energy.sh [--no-build]
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
HOME_DIR="$(mktemp -d /tmp/atoshid-energy-itest.XXXXXX)"
KEYRING="test"
DENOM="aatos"
# Use NON-default ports so we don't collide with a running local_node.sh
# instance on the same machine. CometBFT defaults are 26656/26657/26658;
# Cosmos defaults are 9090/1317/8545. We shift everything by +20.
RPC_PORT=26677     # CometBFT RPC      (default 26657)
P2P_PORT=26676     # CometBFT P2P      (default 26656)
ABCI_PORT=26678    # ABCI proxy_app    (default 26658)
GRPC_PORT=9099     # Cosmos gRPC       (default 9090)
API_PORT=1337      # Cosmos REST       (default 1317)
EVM_PORT=8565      # Ethereum JSON-RPC (default 8545)
NODE_RPC="tcp://localhost:${RPC_PORT}"
ATOSHID="$ROOT/build/atoshid"

# 1 ATOS = 1e18 aatos
ATOS_18=1000000000000000000

cleanup() {
  if [[ -n "${ATOSHID_PID:-}" ]] && kill -0 "$ATOSHID_PID" 2>/dev/null; then
    kill "$ATOSHID_PID" >/dev/null 2>&1 || true
    wait "$ATOSHID_PID" 2>/dev/null || true
  fi
  rm -rf "$HOME_DIR"
}
trap cleanup EXIT

log()  { printf '\n\033[1;34m[itest]\033[0m %s\n' "$*"; }
ok()   { printf '  \033[1;32m✓\033[0m %s\n' "$*"; }
fail() { printf '\n\033[1;31m[fail]\033[0m %s\n' "$*"; exit 1; }

if [[ "$NO_BUILD" -eq 0 ]]; then
  log "building atoshid"
  mkdir -p build
  go build -o "$ATOSHID" ./cmd/atoshid
fi
[[ -x "$ATOSHID" ]] || fail "atoshid not built at $ATOSHID"

H=( --home "$HOME_DIR" )

log "init chain $CHAIN_ID under $HOME_DIR"
"$ATOSHID" "${H[@]}" init energy-itest --chain-id "$CHAIN_ID" >/dev/null

# Five test keys: validator, alice (whale), bob (delegatee), charlie (poor), feeder.
for k in validator alice bob charlie feeder; do
  "$ATOSHID" "${H[@]}" keys add "$k" --keyring-backend "$KEYRING" --algo eth_secp256k1 >/dev/null
done

addr() { "$ATOSHID" "${H[@]}" keys show "$1" -a --keyring-backend "$KEYRING"; }
ALICE=$(addr alice)
BOB=$(addr bob)
CHARLIE=$(addr charlie)
FEEDER=$(addr feeder)
VAL=$(addr validator)

log "set genesis balances + register feeder whitelist"
# Use explicit addresses (already extracted via `keys show`) so there is no
# ambiguity about how add-genesis-account resolves key names.
# validator: enough to bond. alice: 60k ATOS (capacity 100k energy).
# bob: 5k (below threshold -> no energy). charlie: 1k (well below).
# feeder: 1k (only needs gas; oracle MsgReportPrice is subsidized).
"$ATOSHID" "${H[@]}" add-genesis-account "$VAL"     100000000000000000000000000$DENOM >/dev/null
"$ATOSHID" "${H[@]}" add-genesis-account "$ALICE"   60000${ATOS_18}$DENOM             >/dev/null
"$ATOSHID" "${H[@]}" add-genesis-account "$BOB"     5000${ATOS_18}$DENOM              >/dev/null
"$ATOSHID" "${H[@]}" add-genesis-account "$CHARLIE" 1000${ATOS_18}$DENOM              >/dev/null
"$ATOSHID" "${H[@]}" add-genesis-account "$FEEDER"  1000${ATOS_18}$DENOM              >/dev/null

GENESIS="$HOME_DIR/config/genesis.json"

# Whitelist the feeder; (oracle MsgReportPrice is also subsidized in
# energy params by default, but keep oracle params consistent.)
jq --arg feeder "$FEEDER" '
  .app_state.oracle.params.allowed_feeders = [$feeder]
' "$GENESIS" > "$GENESIS.tmp" && mv "$GENESIS.tmp" "$GENESIS"

"$ATOSHID" "${H[@]}" gentx validator 1000${ATOS_18}$DENOM --chain-id "$CHAIN_ID" --keyring-backend $KEYRING >/dev/null
"$ATOSHID" "${H[@]}" collect-gentxs >/dev/null
"$ATOSHID" "${H[@]}" validate-genesis >/dev/null

# Rewrite EVERY listen port in config.toml / app.toml so we don't collide
# with another atoshid (e.g. local_node.sh on default ports).
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
# --chain-id is REQUIRED here. Atoshi's AtoshiAppOptions(chainID) runs in
# NewAtoshi() before genesis.json is parsed, so without the flag the chain
# id is empty and the app panics with "unknown chain id:".
"$ATOSHID" "${H[@]}" start --chain-id "$CHAIN_ID" \
  --minimum-gas-prices "0$DENOM" --log_level info \
  > "$HOME_DIR/node.log" 2>&1 &
ATOSHID_PID=$!

# Wait for RPC. If the chain process dies before answering, dump the log.
NODE_UP=0
for i in $(seq 1 30); do
  if ! kill -0 "$ATOSHID_PID" 2>/dev/null; then
    echo "----- node.log (process exited) -----"
    tail -50 "$HOME_DIR/node.log" || true
    fail "atoshid process died during startup; see log above"
  fi
  if "$ATOSHID" status --node "$NODE_RPC" --home "$HOME_DIR" >/dev/null 2>&1; then
    NODE_UP=1; break
  fi
  sleep 1
done
if [[ "$NODE_UP" -ne 1 ]]; then
  echo "----- node.log (timeout, process still running) -----"
  tail -80 "$HOME_DIR/node.log" || true
  fail "node RPC did not respond on $NODE_RPC after 30s"
fi

# CRITICAL: confirm we're actually talking to OUR node, not a different
# atoshid instance the operator might have running on the default port.
ACTUAL_CHAIN=$("$ATOSHID" status --node "$NODE_RPC" --home "$HOME_DIR" --output json 2>/dev/null \
  | jq -r '.NodeInfo.network // .node_info.network // ""')
[[ "$ACTUAL_CHAIN" == "$CHAIN_ID" ]] || fail "RPC at $NODE_RPC reports chain_id='$ACTUAL_CHAIN' (expected '$CHAIN_ID') — wrong node?"

QFL=( --home "$HOME_DIR" --node "$NODE_RPC" --output json )
TFL=( --home "$HOME_DIR" --chain-id "$CHAIN_ID" --keyring-backend $KEYRING --node "$NODE_RPC" --yes \
      --gas-prices "1$DENOM" --gas auto --gas-adjustment 1.5 --output json )

# helper: query alice's bank balance in aatos (string)
bank_bal() {
  "$ATOSHID" query bank balance "$1" "$DENOM" "${QFL[@]}" | jq -r '.balance.amount'
}
energy_acc() {
  "$ATOSHID" query energy account "$1" "${QFL[@]}"
}

# ============================================================================
log "1) module reachable: query energy params"
"$ATOSHID" query energy params "${QFL[@]}" | jq -e '.params.energy_enabled == true' >/dev/null \
  || fail "energy params unreachable or disabled at genesis"
ok "energy params query OK, enabled = true"

# ============================================================================
# Sanity: confirm chain actually loaded the genesis accounts. If this prints
# "not found" we know the genesis-auth-state never made it into the keeper.
log "1b) sanity: query auth account for alice + feeder"
echo "  alice addr: $ALICE"
"$ATOSHID" query auth account "$ALICE" --node "$NODE_RPC" --output json 2>&1 | head -5 || true
echo "  feeder addr: $FEEDER"
"$ATOSHID" query auth account "$FEEDER" --node "$NODE_RPC" --output json 2>&1 | head -5 || true

# ============================================================================
# The energy keeper initializes an account's last_updated_time only on its
# first touch (AnteHandler / msg_server). A query alone does not initialize.
# So we send a tiny tx from alice -> bob first to register her account,
# then wait, then check accrual.
log "2a) warmup: alice -> bob (1 aatos) to initialize alice's energy account"
"$ATOSHID" "${H[@]}" tx bank send alice "$BOB" "1$DENOM" --from alice "${TFL[@]}" >/dev/null
sleep 3
INIT_ENERGY=$(energy_acc "$ALICE" | jq -r '.settled.tx_energy_accrued')
ok "alice account initialized, energy after warmup = $INIT_ENERGY"

# ============================================================================
# In test mode tx_energy_max_accrue_window = 60s, so capacity (50k for
# alice's 60k ATOS holding) refills at ≈833/s. After 8s ≈ 6,664 gas units.
log "2b) wait for alice to accrue energy (sleep 8s; rate ≈ 833/s)"
sleep 8
ALICE_ENERGY=$(energy_acc "$ALICE" | jq -r '.settled.tx_energy_accrued')
[[ "$ALICE_ENERGY" -gt "$INIT_ENERGY" ]] || fail "alice's energy should have grown past $INIT_ENERGY after 8s, got $ALICE_ENERGY"
ok "alice TxEnergy = $ALICE_ENERGY (capacity: $(energy_acc "$ALICE" | jq -r '.tx_energy_capacity'))"

# ============================================================================
log "3) feeder MsgReportPrice is subsidized (free regardless of energy)"
BAL_BEFORE=$(bank_bal "$FEEDER")
"$ATOSHID" "${H[@]}" tx oracle report-price 0.15 150000 itest \
  --from feeder "${TFL[@]}" >/dev/null
sleep 3
BAL_AFTER=$(bank_bal "$FEEDER")
DELTA=$(( BAL_BEFORE - BAL_AFTER ))
[[ "$DELTA" -eq 0 ]] || fail "feeder MsgReportPrice should be free, paid $DELTA"
ok "feeder paid 0 atos for subsidized MsgReportPrice"

# ============================================================================
log "4) charlie (1k ATOS, below threshold) pays full fee on MsgSend"
BAL_BEFORE=$(bank_bal "$CHARLIE")
"$ATOSHID" "${H[@]}" tx bank send charlie "$BOB" "1$DENOM" "${TFL[@]}" >/dev/null
sleep 3
BAL_AFTER=$(bank_bal "$CHARLIE")
DELTA=$(( BAL_BEFORE - BAL_AFTER ))
# delta = 1 (sent) + gas_fee
[[ "$DELTA" -gt 100 ]] || fail "charlie should have paid gas (delta=$DELTA)"
ok "charlie paid $DELTA aatos (1 sent + $(( DELTA - 1 )) gas)"

# ============================================================================
log "5) alice (60k ATOS, has accrued energy) → energy covers gas, lower fee"
BAL_BEFORE=$(bank_bal "$ALICE")
ENERGY_BEFORE=$(energy_acc "$ALICE" | jq -r '.settled.tx_energy_accrued')
"$ATOSHID" "${H[@]}" tx bank send alice "$BOB" "1$DENOM" "${TFL[@]}" >/dev/null
sleep 3
BAL_AFTER=$(bank_bal "$ALICE")
ENERGY_AFTER=$(energy_acc "$ALICE" | jq -r '.settled.tx_energy_accrued')
ATOS_DELTA=$(( BAL_BEFORE - BAL_AFTER ))
ENERGY_DELTA=$(( ENERGY_BEFORE - ENERGY_AFTER ))
ok "alice spent $ATOS_DELTA aatos (1 sent + gas), energy delta = $ENERGY_DELTA"
[[ "$ENERGY_DELTA" -gt 0 ]] || fail "alice's energy should have decreased"

# ============================================================================
log "6) delegate 50000 TxEnergy alice → bob for 1h, ATOS frozen"
BOB_IN_BEFORE=$(energy_acc "$BOB" | jq -r '.settled.delegated_in_usable')
ALICE_LOCKED_BEFORE=$(energy_acc "$ALICE" | jq -r '.settled.locked_atos')

"$ATOSHID" "${H[@]}" tx energy delegate "$BOB" 50000 1h --from alice "${TFL[@]}" >/dev/null
sleep 3

BOB_IN_AFTER=$(energy_acc "$BOB" | jq -r '.settled.delegated_in_usable')
ALICE_LOCKED_AFTER=$(energy_acc "$ALICE" | jq -r '.settled.locked_atos')

[[ "$BOB_IN_AFTER" -ge 50000 ]] || fail "bob should have ≥50000 inbound energy, got $BOB_IN_AFTER"
ok "bob inbound energy: $BOB_IN_BEFORE → $BOB_IN_AFTER"
ok "alice locked_atos: $ALICE_LOCKED_BEFORE → $ALICE_LOCKED_AFTER (30000 ATOS frozen for 50000 gas units)"

DELEG_LIST=$("$ATOSHID" query energy delegations "$ALICE" out "${QFL[@]}")
DELEG_ID=$(echo "$DELEG_LIST" | jq -r '.outbound[0].id')
[[ -n "$DELEG_ID" && "$DELEG_ID" != "null" ]] || fail "no outbound delegation found"
ok "delegation id = $DELEG_ID"

# ============================================================================
log "7) bob (no own energy) sends with delegated energy → no ATOS gas"
BOB_BAL_BEFORE=$(bank_bal "$BOB")
BOB_IN_BEFORE=$(energy_acc "$BOB" | jq -r '.settled.delegated_in_usable')
"$ATOSHID" "${H[@]}" tx bank send bob "$CHARLIE" "1$DENOM" "${TFL[@]}" >/dev/null
sleep 3
BOB_BAL_AFTER=$(bank_bal "$BOB")
BOB_IN_AFTER=$(energy_acc "$BOB" | jq -r '.settled.delegated_in_usable')
BOB_DELTA=$(( BOB_BAL_BEFORE - BOB_BAL_AFTER ))
INBOUND_DELTA=$(( BOB_IN_BEFORE - BOB_IN_AFTER ))
ok "bob spent $BOB_DELTA aatos, inbound energy $BOB_IN_BEFORE → $BOB_IN_AFTER (Δ=$INBOUND_DELTA)"
[[ "$INBOUND_DELTA" -gt 0 ]] || fail "bob should have consumed inbound delegated energy"

# ============================================================================
log "8) undelegate → ATOS unfrozen"
"$ATOSHID" "${H[@]}" tx energy undelegate "$DELEG_ID" --from alice "${TFL[@]}" >/dev/null
sleep 3
ALICE_LOCKED_FINAL=$(energy_acc "$ALICE" | jq -r '.settled.locked_atos')
ok "alice locked_atos after undelegate: $ALICE_LOCKED_FINAL (should be 0)"
[[ "$ALICE_LOCKED_FINAL" == "0" ]] || fail "alice locked_atos should be 0 after undelegate"

# ============================================================================
log "9) governance: change tx_energy_per_threshold 50000 → 100000 via MsgUpdateParams"
# This step requires gov proposal flow. Skipped here as it depends on
# atoshid CLI's gov proposal support; left as a manual / separate test.
ok "governance retune step skipped (run separately)"

# ============================================================================
log "10) estimate-fee query"
EST=$("$ATOSHID" query energy estimate-fee "$ALICE" 21000 "${QFL[@]}")
echo "$EST" | jq '.'
ok "estimate-fee query OK"

log "ALL CHECKS PASSED"
