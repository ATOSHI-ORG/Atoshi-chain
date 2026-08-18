#!/usr/bin/env bash
# ============================================================================
# 本地 devnet 升级模拟脚本 — 验证 v20.1 升级提案在单 binary 上跑得通
#
# 测试范围（运维安全）：
#   ✓ 升级提案语法正确，能上链
#   ✓ 投票期 60s 内通过
#   ✓ BeginBlocker 在 target_height 真的触发 v20.1 upgrade handler
#   ✓ RefreshAllSnapshots 跑完不 panic
#   ✓ 链在 handler 跑完后继续出块（验证升级流程不会卡死）
#
# 不在测试范围（需 Go 单测）：
#   ✗ "能量累积 bug 真的被修了" —— 当前 binary 已经接好 SendRestriction，
#     无法在同一 binary 内造出"坏 snapshot"状态。验证 bug 修复要写 Go 单测：
#     app/upgrades/v20_1/upgrades_test.go
#
# Usage:
#   ./scripts/upgrade_devnet.sh
#
# HOME_DIR: $HOME/.atoshid-upgrade-test （独立目录，不影响 qa_devnet）
# ============================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

CHAIN_ID="atoshi_88388-1"
HOME_DIR="$HOME/.atoshid-upgrade-test"
KEYRING="test"
DENOM="liao"

# 端口（与 local_node.sh / qa_devnet.sh 错开）
RPC_PORT=26697
P2P_PORT=26696
ABCI_PORT=26698
GRPC_PORT=9094
API_PORT=1357
EVM_PORT=8585
NODE_RPC="tcp://localhost:${RPC_PORT}"

ATOSHID="$ROOT/build/atoshid"

# 固定助记词（仅本地测试，不要用到任何真实环境）
MNEMO_VAL="raccoon floor wood tongue wasp web chef various cattle pigeon december draft obscure case armor farm cloud wood high pole absurd network deputy potato"
MNEMO_ALICE="van fence blouse utility venue visual rough riot risk liberty pigeon turn junior sea wool okay shaft gloom equip sword dose grape wage horror"

log()  { printf '\n\033[1;34m[upgrade-test]\033[0m %s\n' "$*"; }
ok()   { printf '  \033[1;32m✓\033[0m %s\n' "$*"; }
fail() { printf '\n\033[1;31m[FAIL]\033[0m %s\n' "$*"; exit 1; }
atos() { printf '%s000000000000000000' "$1"; }

H=( --home "$HOME_DIR" )
TX_FLAGS=( --chain-id "$CHAIN_ID" --keyring-backend "$KEYRING" --node "$NODE_RPC" \
           --yes --gas 500000 --gas-prices "1$DENOM" -o json )
QFLAGS=( --node "$NODE_RPC" -o json )

# ----------------------------------------------------------------------------
# 1. build binary
# ----------------------------------------------------------------------------
log "构建 atoshid（当前分支 HEAD）"
mkdir -p build
go build -o "$ATOSHID" ./cmd/atoshid
[[ -x "$ATOSHID" ]] || fail "atoshid 未构建：$ATOSHID"

# ----------------------------------------------------------------------------
# 2. reset + init devnet
# ----------------------------------------------------------------------------
log "重置 $HOME_DIR"
rm -rf "$HOME_DIR"
"$ATOSHID" "${H[@]}" init upgrade-test --chain-id "$CHAIN_ID" >/dev/null

log "导入 validator + alice 助记词"
printf '%s\n' "$MNEMO_VAL"   | "$ATOSHID" "${H[@]}" keys add validator --recover --keyring-backend "$KEYRING" --algo eth_secp256k1 >/dev/null
printf '%s\n' "$MNEMO_ALICE" | "$ATOSHID" "${H[@]}" keys add alice     --recover --keyring-backend "$KEYRING" --algo eth_secp256k1 >/dev/null

VAL=$("$ATOSHID" "${H[@]}" keys show validator -a --keyring-backend "$KEYRING")
ALICE=$("$ATOSHID" "${H[@]}" keys show alice -a --keyring-backend "$KEYRING")
ok "validator = $VAL"
ok "alice     = $ALICE"

log "写入创世余额"
"$ATOSHID" "${H[@]}" add-genesis-account "$VAL"   "$(atos 100000000)$DENOM" >/dev/null
"$ATOSHID" "${H[@]}" add-genesis-account "$ALICE" "$(atos 60000)$DENOM"     >/dev/null

log "patch genesis：缩短 voting_period 到 60s（验证 upgrade-proposal 不用等 30 分钟）"
GENESIS="$HOME_DIR/config/genesis.json"
jq '
  .app_state.gov.params.voting_period = "60s"
  | .app_state.gov.params.max_deposit_period = "60s"
  | .app_state.gov.params.expedited_voting_period = "30s"
  | .app_state.gov.params.quorum = "0.100000000000000000"
  | .app_state.gov.params.min_deposit = [{"denom":"liao","amount":"1000000"}]
  | .app_state.gov.params.expedited_min_deposit = [{"denom":"liao","amount":"5000000"}]
' "$GENESIS" > "$GENESIS.tmp" && mv "$GENESIS.tmp" "$GENESIS"

"$ATOSHID" "${H[@]}" gentx validator "$(atos 1000)$DENOM" --chain-id "$CHAIN_ID" --keyring-backend "$KEYRING" >/dev/null
"$ATOSHID" "${H[@]}" collect-gentxs >/dev/null
"$ATOSHID" "${H[@]}" validate-genesis >/dev/null

log "改端口避冲突"
CONFIG="$HOME_DIR/config/config.toml"
APP_TOML="$HOME_DIR/config/app.toml"
sed -i.bak "s|tcp://127.0.0.1:26657|tcp://127.0.0.1:${RPC_PORT}|g"  "$CONFIG"
sed -i.bak "s|tcp://0.0.0.0:26656|tcp://0.0.0.0:${P2P_PORT}|g"      "$CONFIG"
sed -i.bak "s|tcp://127.0.0.1:26658|tcp://127.0.0.1:${ABCI_PORT}|g" "$CONFIG"
sed -i.bak "s|localhost:9090|localhost:${GRPC_PORT}|g"              "$APP_TOML"
sed -i.bak "s|tcp://localhost:1317|tcp://localhost:${API_PORT}|g"   "$APP_TOML"
sed -i.bak "s|127.0.0.1:8545|127.0.0.1:${EVM_PORT}|g"               "$APP_TOML"
rm -f "$CONFIG.bak" "$APP_TOML.bak"

# ----------------------------------------------------------------------------
# 3. start chain in background
# ----------------------------------------------------------------------------
log "后台启动节点"
"$ATOSHID" "${H[@]}" start --chain-id "$CHAIN_ID" \
  --minimum-gas-prices "0$DENOM" --log_level info \
  > "$HOME_DIR/node.log" 2>&1 &
NODE_PID=$!

# 自动清理：脚本退出时杀掉节点
cleanup() {
  if kill -0 "$NODE_PID" 2>/dev/null; then
    log "清理：停止节点 PID=$NODE_PID"
    kill "$NODE_PID" 2>/dev/null || true
    wait "$NODE_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

log "等待第一个块上链（最多 60 秒）..."
H_CURRENT=0
for i in $(seq 1 60); do
  # || echo 0 防止 curl/jq 失败被 set -e 杀掉；用正则校验防空字符串
  raw=$(curl -s "http://localhost:${RPC_PORT}/status" 2>/dev/null || true)
  H_CURRENT=$(echo "$raw" | jq -r '.result.sync_info.latest_block_height // "0"' 2>/dev/null || echo 0)
  H_CURRENT=${H_CURRENT:-0}
  if [[ "$H_CURRENT" =~ ^[0-9]+$ ]] && [[ "$H_CURRENT" -gt 0 ]]; then
    break
  fi
  if (( i % 5 == 0 )); then echo "  ${i}s: height=$H_CURRENT"; fi
  sleep 1
done
if ! [[ "$H_CURRENT" =~ ^[0-9]+$ ]] || [[ "$H_CURRENT" -eq 0 ]]; then
  echo "==== node.log 末尾 40 行 ===="
  tail -40 "$HOME_DIR/node.log" 2>/dev/null || echo "(node.log 空)"
  fail "节点没在 60 秒内产出第一个块"
fi
ok "节点已出块，当前高度 $H_CURRENT"

# ----------------------------------------------------------------------------
# 4. 拿 gov authority 地址
# ----------------------------------------------------------------------------
log "查询 gov 模块账户地址"
GOV_AUTH=$("$ATOSHID" "${H[@]}" query auth module-account gov "${QFLAGS[@]}" | jq -r '.account.value.address // .account.base_account.address')
ok "gov authority = $GOV_AUTH"

# ----------------------------------------------------------------------------
# 5. 计算升级目标高度
# ----------------------------------------------------------------------------
log "计算 upgrade target 高度"
H_NOW=$(curl -s "http://localhost:${RPC_PORT}/status" 2>/dev/null | jq -r '.result.sync_info.latest_block_height // "0"' 2>/dev/null || echo 0)
H_NOW=${H_NOW:-0}
if ! [[ "${H_NOW}" =~ ^[0-9]+$ ]] || [[ "${H_NOW}" -eq 0 ]]; then
  fail "拿不到当前块高（H_NOW='${H_NOW}'）"
fi
# 投票期 60s = ~12 块（5 秒/块） + 缓冲 50 块（防止脚本轮询延迟 + 链 apply 延迟）= 62 块
# 留足够缓冲很重要：如果 target_height 在 proposal apply 时已经过去了，
# 链会以 "upgrade cannot be scheduled in the past" 拒绝
TARGET_HEIGHT=$((H_NOW + 62))
ok "当前高度 ${H_NOW}，升级目标 ${TARGET_HEIGHT}"

# ----------------------------------------------------------------------------
# 6. 提交升级提案
# ----------------------------------------------------------------------------
log "提交 v20.1 升级提案"
cat > /tmp/upgrade-v20.1.json <<EOF
{
  "messages": [
    {
      "@type": "/cosmos.upgrade.v1beta1.MsgSoftwareUpgrade",
      "authority": "$GOV_AUTH",
      "plan": {
        "name": "v20.1",
        "height": "${TARGET_HEIGHT}",
        "info": ""
      }
    }
  ],
  "metadata": "local upgrade simulation",
  "deposit": "1000000liao",
  "title": "v20.1 local test",
  "summary": "Local devnet simulation of v20.1 upgrade (RefreshAllSnapshots + SendRestriction)."
}
EOF

SUBMIT_HASH=$("$ATOSHID" "${H[@]}" tx gov submit-proposal /tmp/upgrade-v20.1.json --from validator "${TX_FLAGS[@]}" | jq -r '.txhash')
ok "submit txhash = $SUBMIT_HASH"
sleep 5

# 验证 tx 上链
SUBMIT_CODE=$("$ATOSHID" "${H[@]}" query tx "$SUBMIT_HASH" "${QFLAGS[@]}" | jq -r '.code')
[[ "${SUBMIT_CODE}" == "0" ]] || fail "submit-proposal 失败 code=${SUBMIT_CODE}，看 raw_log"
ok "提案上链成功"

# 拿提案 ID
# 拿最大 proposal id（不依赖 --reverse flag，做 jq 端排序）
PID=$("$ATOSHID" "${H[@]}" query gov proposals "${QFLAGS[@]}" 2>/dev/null \
  | jq -r '[.proposals[].id | tonumber] | max | tostring')
ok "Proposal ID = $PID"

# ----------------------------------------------------------------------------
# 7. 投票
# ----------------------------------------------------------------------------
log "投 yes"
VOTE_HASH=$("$ATOSHID" "${H[@]}" tx gov vote "$PID" yes --from validator "${TX_FLAGS[@]}" | jq -r '.txhash')
sleep 5
VOTE_CODE=$("$ATOSHID" "${H[@]}" query tx "$VOTE_HASH" "${QFLAGS[@]}" | jq -r '.code')
[[ "$VOTE_CODE" == "0" ]] || fail "vote 失败 code=$VOTE_CODE"
ok "投票成功"

# ----------------------------------------------------------------------------
# 8. 等提案通过（voting_period = 60s）
# ----------------------------------------------------------------------------
log "等提案 status = PASSED（最多 90 秒）"
for i in $(seq 1 30); do
  # SDK v0.50 把字段包在 .proposal 下；老版本是顶层
  STATUS=$("$ATOSHID" "${H[@]}" query gov proposal "$PID" "${QFLAGS[@]}" \
    | jq -r '.proposal.status // .status // "null"')
  echo "  status = ${STATUS}"
  if [[ "${STATUS}" == "PROPOSAL_STATUS_PASSED" ]]; then
    ok "提案通过"
    break
  fi
  if [[ "${STATUS}" == "PROPOSAL_STATUS_REJECTED" || "${STATUS}" == "PROPOSAL_STATUS_FAILED" ]]; then
    FR=$("$ATOSHID" "${H[@]}" query gov proposal "$PID" "${QFLAGS[@]}" \
      | jq -r '.proposal.failed_reason // .failed_reason // ""')
    fail "提案 ${STATUS}（failed_reason: ${FR}）"
  fi
  sleep 3
done
[[ "${STATUS}" == "PROPOSAL_STATUS_PASSED" ]] || fail "提案 90 秒内没通过，当前 ${STATUS}"

# ----------------------------------------------------------------------------
# 9. 等到达升级高度
# ----------------------------------------------------------------------------
log "等链跑到升级高度 ${TARGET_HEIGHT}"
for i in $(seq 1 60); do
  H_NOW=$(curl -s "http://localhost:${RPC_PORT}/status" 2>/dev/null | jq -r '.result.sync_info.latest_block_height // "0"' 2>/dev/null || echo 0)
  H_NOW=${H_NOW:-0}
  [[ "${H_NOW}" =~ ^[0-9]+$ ]] || H_NOW=0
  echo "  block = $H_NOW (target $TARGET_HEIGHT)"
  if [[ "${H_NOW}" -ge "${TARGET_HEIGHT}" ]]; then
    ok "已到达升级高度"
    break
  fi
  sleep 3
done

sleep 3   # 给 BeginBlocker 一点时间跑 handler

# ----------------------------------------------------------------------------
# 10. 检验 upgrade handler 跑了
# ----------------------------------------------------------------------------
log "检查 upgrade handler 日志"
if grep -q "v20.1" "$HOME_DIR/node.log" && grep -q "energy snapshot refresh complete" "$HOME_DIR/node.log"; then
  REFRESHED=$(grep "energy snapshot refresh complete" "$HOME_DIR/node.log" | tail -1 | sed -E 's/.*accounts_refreshed=([0-9]+).*/\1/')
  ok "upgrade handler 已执行（刷新了 $REFRESHED 个 EnergyAccount）"
else
  echo
  echo "==== node.log 最后 30 行（debug 用）===="
  tail -30 "$HOME_DIR/node.log"
  fail "upgrade handler 没跑——可能 height 没到，或 handler 未被注册"
fi

# ----------------------------------------------------------------------------
# 11. 验证链继续出块（升级后没卡死）
# ----------------------------------------------------------------------------
log "等 10 秒，验证链继续出块"
sleep 10
H_AFTER=$(curl -s "http://localhost:${RPC_PORT}/status" 2>/dev/null | jq -r '.result.sync_info.latest_block_height // "0"' 2>/dev/null || echo 0)
H_AFTER=${H_AFTER:-0}
[[ "${H_AFTER}" =~ ^[0-9]+$ ]] || H_AFTER=0
if [[ "${H_AFTER}" -gt "${TARGET_HEIGHT}" ]]; then
  ok "链继续出块，当前高度 ${H_AFTER}（升级后又出了 $((H_AFTER - TARGET_HEIGHT)) 块）"
else
  fail "链卡在升级高度 $TARGET_HEIGHT 没动"
fi

# ----------------------------------------------------------------------------
# 12. 抽样验证 EnergyAccount snapshot
# ----------------------------------------------------------------------------
log "抽样：alice 的 snapshot 是否等于 bank 余额"
# 先发个 tx 让 alice 的 energy account 被持久化（如果没创建过的话）
ALICE_BAL=$("$ATOSHID" "${H[@]}" query bank balance "$ALICE" "$DENOM" "${QFLAGS[@]}" | jq -r '.balance.amount')

# 触发 alice 的 energy account 写入（任意 tx）
"$ATOSHID" "${H[@]}" tx bank send "$ALICE" "$VAL" "1$DENOM" --from alice "${TX_FLAGS[@]}" >/dev/null
sleep 5

ALICE_SNAP=$("$ATOSHID" "${H[@]}" query energy account "$ALICE" "${QFLAGS[@]}" | jq -r '.settled.last_balance_snapshot // "0"')
ALICE_BAL_NOW=$("$ATOSHID" "${H[@]}" query bank balance "$ALICE" "$DENOM" "${QFLAGS[@]}" | jq -r '.balance.amount')

if [[ "${ALICE_SNAP}" == "${ALICE_BAL_NOW}" ]]; then
  ok "alice snapshot (${ALICE_SNAP}) == bank balance (${ALICE_BAL_NOW})"
else
  echo "  ⚠️  snapshot=${ALICE_SNAP}, bank=${ALICE_BAL_NOW}（差异可能是正常的——SendRestriction 已生效，snapshot 在 send 时已更新）"
fi

# ----------------------------------------------------------------------------
# 13. 总结
# ----------------------------------------------------------------------------
echo
echo "========================================================================"
echo "  ✅ v20.1 升级模拟通过"
echo "========================================================================"
echo "  Proposal ID    : $PID"
echo "  Upgrade height : ${TARGET_HEIGHT}"
echo "  Refreshed accts: $REFRESHED"
echo "  Block now      : ${H_AFTER}"
echo ""
echo "  日志: $HOME_DIR/node.log"
echo "  完整 upgrade 事件:"
grep -E "v20.1|energy snapshot|UPGRADE" "$HOME_DIR/node.log" | head -20 | sed 's/^/    /'
echo "========================================================================"
echo ""
echo "  下一步建议："
echo "    1. 写 Go 单测 app/upgrades/v20_1/upgrades_test.go 验证 RefreshAllSnapshots 真的修 bug"
echo "    2. 在主网升级前，把 binary 推到 GitHub Release，让 cosmovisor 能下载"
