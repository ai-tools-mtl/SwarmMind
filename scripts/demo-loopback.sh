#!/usr/bin/env bash
# SwarmMind V0 回环演示（SPEC 七 DoD 3-10 逐条验证）。
#
# 覆盖：DoD 3 全链 echo、4 超时+进程组整组终止、5 离线信箱、6 重连韧性(epoch 递增)、
# 7 hub 重启恢复、8 幂等、9 白名单、10 needs_review；另含步骤 0（补充，非 DoD 项）
# TLS TOFU 首连记录。DoD 1/2 由 go build/test 验证；DoD 11 双机属 P2（SPEC 已声明）。
#
# 脚本要求（SPEC 七）：bash set -euo pipefail、trap 清理、mktemp 隔离（hub/node 各自
# 独立 data_dir）、有上限的状态轮询（无固定 sleep 等待）、JSON 解析用 python3、
# 随机空闲端口。超时验收检查直接子进程消失（pgrep 进程组幸存），不只看错误字符串。
#
# 局限声明（SPEC 七）：DoD 6 的「旧连接残余帧被 hub 忽略」无法在本脚本注入，由单测
# 覆盖（internal/hub/core L1 代次复核、internal/hub/session epoch 残余帧注入）；
# DoD 8 的「重复 offer 只执行一次」由单测覆盖（ledger.ErrDuplicate 幂等分支 +
# fullchain 重复 offer 用例）。断线一律用进程 kill 模拟。

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/swarmmind-demo.XXXXXX")"
mkdir -p "$TMP/bin" "$TMP/logs"

ADMIN_TOKEN="demo-admin-token-0123456789"
NODEA_TOKEN="demo-token-node-a"
NODEB_TOKEN="demo-token-node-b"
LOCAL_A_TOKEN="demo-local-token-a"
LOCAL_B_TOKEN="demo-local-token-b"

HUB_PID=""
NODEA_PID=""
NODEB_PID=""
HUB_TLS_PID=""
NODE_TLS_PID=""
API_PORT=""      # 当前 api() 指向的管理面端口（TLS 段会切换）
COOKIES="$TMP/cookies"  # 会话 jar（TLS 段换独立 jar：cookie 域不含端口，共用会互踩）

cleanup() {
  local rc=$?
  for pid in "$NODEB_PID" "$NODEA_PID" "$NODE_TLS_PID" "$HUB_TLS_PID" "$HUB_PID"; do
    if [ -n "$pid" ]; then
      kill "$pid" 2>/dev/null || true
    fi
  done
  # 进程组幸存兜底清理（demo 专用参数，避免误伤他进程）
  pkill -f 'sleep 41$' 2>/dev/null || true
  pkill -f 'sleep 44$' 2>/dev/null || true
  if [ "$rc" -ne 0 ]; then
    echo
    echo "演示失败：现场保留在 ${TMP} （日志在 ${TMP}/logs/）"
  else
    rm -rf "$TMP"
  fi
}
trap cleanup EXIT

fail() { echo "FAIL: $*"; exit 1; }
pass() { echo "  PASS: $*"; }

free_port() {
  python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

# jgetkey <file> <key>：单键取值（键含 . 时不能用 jget 的点路径）
jgetkey() {
  python3 - "$1" "$2" <<'PY'
import json, sys
print(json.load(open(sys.argv[1]))[sys.argv[2]])
PY
}

# jget <file> <dotted.path>
jget() {
  python3 - "$1" "$2" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
cur = d
for k in sys.argv[2].split("."):
    if k:
        cur = cur[k]
print(cur)
PY
}

# node_field <file> <node_id> <key>
node_field() {
  python3 - "$1" "$2" "$3" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
for n in d.get("nodes", []):
    if n["node_id"] == sys.argv[2]:
        print(n[sys.argv[3]])
        break
PY
}

# api <method> <path> [body]：CODE=HTTP 状态码，响应体写 $TMP/resp.json
api() {
  local method=$1 path=$2 body=${3:-}
  local args=(-sS -o "$TMP/resp.json" -w '%{http_code}' -X "$method"
              -b "$COOKIES" -c "$COOKIES" -H 'Content-Type: application/json')
  if [ -n "$body" ]; then
    args+=(-d "$body")
  fi
  CODE=$(curl "${args[@]}" "http://127.0.0.1:${API_PORT}${path}")
}

# wait_api <api_port> <admin_token> <cookie_jar> <timeout_s>：登录就绪（有上限轮询）
wait_api() {
  local port=$1 tok=$2 jar=$3 limit=$4 t0=$SECONDS
  while :; do
    code=$(curl -sS -o "$TMP/resp.json" -w '%{http_code}' -X POST \
      -b "$jar" -c "$jar" -H 'Content-Type: application/json' \
      -d "{\"token\":\"$tok\"}" "http://127.0.0.1:${port}/api/v1/login" 2>/dev/null || true)
    if [ "$code" = "200" ]; then return 0; fi
    if (( SECONDS - t0 > limit )); then return 1; fi
    sleep 0.2
  done
}

# wait_node_online <node_id> <timeout_s>
wait_node_online() {
  local id=$1 limit=$2 t0=$SECONDS
  while :; do
    api GET /api/v1/nodes
    if [ "$CODE" = "200" ] && [ "$(node_field "$TMP/resp.json" "$id" online)" = "True" ]; then
      return 0
    fi
    if (( SECONDS - t0 > limit )); then return 1; fi
    sleep 0.2
  done
}

# wait_status <task_id> <正则> <timeout_s>：STATUS 收敛到目标
wait_status() {
  local id=$1 want=$2 limit=$3 t0=$SECONDS
  while :; do
    api GET "/api/v1/tasks/$id"
    if [ "$CODE" != "200" ]; then fail "查询任务 $id 得 HTTP $CODE"; fi
    STATUS=$(jget "$TMP/resp.json" status)
    if [[ "$STATUS" =~ $want ]]; then return 0; fi
    if (( SECONDS - t0 > limit )); then
      fail "任务 $id 在 ${limit}s 内未到达 /$want/（当前 $STATUS）"
    fi
    sleep 0.1
  done
}

echo "== SwarmMind V0 回环演示（DoD 3-10）=="
echo "工作目录: $TMP"

# ---------- 构建（DoD 1 另行由 go build ./... 验证） ----------
echo "── 构建 hub / node / swarmmindctl …"
( cd "$ROOT" && go build -o "$TMP/bin/" ./cmd/hub ./cmd/node ./cmd/swarmmindctl )

# ---------- 配置与端口 ----------
HUB_NODE_PORT=$(free_port)
API_PORT=$(free_port)
LOCAL_A_PORT=$(free_port)
LOCAL_B_PORT=$(free_port)

cat > "$TMP/hub.yaml" <<EOF
node_listen: 127.0.0.1:${HUB_NODE_PORT}
api_listen: 127.0.0.1:${API_PORT}
tls:
  insecure_loopback: true
nodes:
  nodeA: ${NODEA_TOKEN}
  nodeB: ${NODEB_TOKEN}
admin_token: ${ADMIN_TOKEN}
data_dir: ${TMP}/data-hub
EOF

cat > "$TMP/nodeA.yaml" <<EOF
node_id: nodeA
hub_url: ws://127.0.0.1:${HUB_NODE_PORT}/ws/node
register_token: ${NODEA_TOKEN}
hub_pubkey_fp: ""
local_listen: 127.0.0.1:${LOCAL_A_PORT}
local_token: ${LOCAL_A_TOKEN}
concurrency: 2
data_dir: ${TMP}/data-a
base_env:
  PATH: /usr/bin:/bin
  TMPDIR: /tmp
agents:
  - name: echo-demo
    kind: cli
    command: /bin/echo
    workdir: /tmp
    timeout_sec: 10
    allow_args: true
    allow_env: ["TMPDIR"]
    description: 回声测试
    permission_mode: no-shell
  - name: sleeper
    kind: cli
    command: /bin/sleep
    workdir: /tmp
    timeout_sec: 1
    allow_args: true
    allow_env: []
    description: 超时测试
    permission_mode: no-shell
  - name: policy-demo
    kind: cli
    command: /bin/echo
    workdir: /tmp
    timeout_sec: 5
    allow_args: true
    allow_env: ["TMPDIR"]
    description: 白名单拒绝测试（PATH 未列入 allow_env）
    permission_mode: no-shell
EOF

cat > "$TMP/nodeB.yaml" <<EOF
node_id: nodeB
hub_url: ws://127.0.0.1:${HUB_NODE_PORT}/ws/node
register_token: ${NODEB_TOKEN}
hub_pubkey_fp: ""
local_listen: 127.0.0.1:${LOCAL_B_PORT}
local_token: ${LOCAL_B_TOKEN}
concurrency: 2
data_dir: ${TMP}/data-b
base_env:
  PATH: /usr/bin:/bin
  TMPDIR: /tmp
agents:
  - name: echo-demo
    kind: cli
    command: /bin/echo
    workdir: /tmp
    timeout_sec: 10
    allow_args: true
    allow_env: ["TMPDIR"]
    description: 回声测试（nodeB）
    permission_mode: no-shell
  - name: sleeper-long
    kind: cli
    command: /bin/sleep
    workdir: /tmp
    timeout_sec: 20
    allow_args: true
    allow_env: []
    description: needs_review 演示（长任务）
    permission_mode: no-shell
EOF

start_hub()   { "$TMP/bin/hub"  -config "$TMP/hub.yaml"   >>"$TMP/logs/hub.log"  2>&1 & HUB_PID=$!; }
start_nodeA() { "$TMP/bin/node" -config "$TMP/nodeA.yaml" >>"$TMP/logs/nodeA.log" 2>&1 & NODEA_PID=$!; }
start_nodeB() { "$TMP/bin/node" -config "$TMP/nodeB.yaml" >>"$TMP/logs/nodeB.log" 2>&1 & NODEB_PID=$!; }

# ---------- 启动 ----------
echo "── 启动 hub + nodeA + nodeB（回环 ws，随机端口）"
start_hub
start_nodeA
start_nodeB

wait_api "$API_PORT" "$ADMIN_TOKEN" "$COOKIES" 15 || fail "hub 管理面未就绪"
pass "hub 管理面就绪（登录成功）"
wait_node_online nodeA 15 || fail "nodeA 未上线"
wait_node_online nodeB 15 || fail "nodeB 未上线"
pass "nodeA / nodeB 注册上线"

# ---------- 步骤 0（补充，非 DoD 项）：TLS + TOFU 首连记录 ----------
echo
echo "──── [补充] TLS TOFU：wss 自签证书 + trusted_hubs.json 首连记录 ────"
TLS_PORT=$(free_port)
TLS_API_PORT=$(free_port)
mkdir -p "$TMP/data-hub-tls" "$TMP/data-node-tls"
cat > "$TMP/hub-tls.yaml" <<EOF
node_listen: 127.0.0.1:${TLS_PORT}
api_listen: 127.0.0.1:${TLS_API_PORT}
tls:
  insecure_loopback: false
nodes:
  tlsnode: tok-tls
admin_token: ${ADMIN_TOKEN}
data_dir: ${TMP}/data-hub-tls
EOF
cat > "$TMP/node-tls.yaml" <<EOF
node_id: tlsnode
hub_url: wss://127.0.0.1:${TLS_PORT}/ws/node
register_token: tok-tls
hub_pubkey_fp: ""
local_listen: 127.0.0.1:$(free_port)
local_token: tok-local-tls
data_dir: ${TMP}/data-node-tls
base_env:
  PATH: /usr/bin:/bin
agents: []
EOF
"$TMP/bin/hub" -config "$TMP/hub-tls.yaml" >>"$TMP/logs/hub-tls.log" 2>&1 & HUB_TLS_PID=$!
"$TMP/bin/node" -config "$TMP/node-tls.yaml" >>"$TMP/logs/node-tls.log" 2>&1 & NODE_TLS_PID=$!
TLS_COOKIES="$TMP/cookies-tls"
wait_api "$TLS_API_PORT" "$ADMIN_TOKEN" "$TLS_COOKIES" 15 || fail "TLS hub 未就绪"
for i in $(seq 1 50); do
  if [ -s "$TMP/data-node-tls/trusted_hubs.json" ]; then break; fi
  if [ "$i" = 50 ]; then fail "TOFU 记录未生成"; fi
  sleep 0.2
done
TLS_FP=$(jgetkey "$TMP/data-node-tls/trusted_hubs.json" "127.0.0.1:${TLS_PORT}")
if [ -z "$TLS_FP" ]; then fail "TOFU 记录为空"; fi
pass "TOFU 首连已记录 127.0.0.1:${TLS_PORT} → ${TLS_FP:0:12}…"
API_PORT_SAVED=$API_PORT
API_PORT=$TLS_API_PORT
COOKIES_SAVED=$COOKIES
COOKIES=$TLS_COOKIES
api GET /api/v1/nodes
if [ "$(node_field "$TMP/resp.json" tlsnode online)" != "True" ]; then
  fail "tlsnode 未上线（wss + TOFU 模式）"
fi
pass "tlsnode 经 wss + TOFU 注册上线"
API_PORT=$API_PORT_SAVED
COOKIES=$COOKIES_SAVED
kill "$NODE_TLS_PID" 2>/dev/null || true; NODE_TLS_PID=""
kill "$HUB_TLS_PID" 2>/dev/null || true; HUB_TLS_PID=""

# ---------- DoD 3：回环全链 echo（5s 内 completed，FinalAnswer 正确） ----------
echo
echo "──── [DoD 3] 回环全链：CLI 提交 echo → 5s 内 completed 且 FinalAnswer 正确 ────"
CLI=("$TMP/bin/swarmmindctl" --hub "http://127.0.0.1:${API_PORT}" --token "$ADMIN_TOKEN")
T0=$(python3 -c 'import time; print(time.time())')
SUBMIT_OUT=$("${CLI[@]}" submit --node nodeA --agent echo-demo --arg hello --arg swarmmind --by demo)
echo "$SUBMIT_OUT" | sed 's/^/  /'
TASK3=$(printf '%s' "$SUBMIT_OUT" | python3 -c 'import sys,re; m=re.search(r"t-[0-9a-f]{32}", sys.stdin.read()); print(m.group(0) if m else "")')
if [ -z "$TASK3" ]; then fail "CLI submit 未返回 task_id"; fi
wait_status "$TASK3" 'completed' 5
api GET "/api/v1/tasks/$TASK3"
FINAL=$(jget "$TMP/resp.json" result.final_answer)
if [ "${FINAL%$'\n'}" != "hello swarmmind" ]; then fail "FinalAnswer 不符: $FINAL"; fi
ELAPSED=$(python3 -c "import time,sys; print(round(time.time()-float(sys.argv[1]),2))" "$T0")
if ! python3 -c "import sys; sys.exit(0 if float(sys.argv[1]) <= 5.0 else 1)" "$ELAPSED"; then
  fail "耗时 ${ELAPSED}s 超过 5s"
fi
pass "全链 completed（${ELAPSED}s ≤ 5s），FinalAnswer=hello swarmmind"

# node 本地 API 提交（SPEC 六：本机进程 → 本机 node → hub）
LOCAL_OUT=$(curl -sS -X POST -H 'Authorization: Bearer '"$LOCAL_A_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"target_node":"nodeA","target_agent":"echo-demo","input":{"args":["via-local-api"]},"submitted_by":"local-proc"}' \
  "http://127.0.0.1:${LOCAL_A_PORT}/api/v1/tasks")
LOCAL_TASK=$(printf '%s' "$LOCAL_OUT" | python3 -c 'import json,sys; print(json.load(sys.stdin)["task_id"])')
wait_status "$LOCAL_TASK" 'completed' 10
pass "node 本地 API 提交 → completed（ ${LOCAL_TASK} ）"
# 本地能力目录（边界方案 v2：node 无全网视图——只有本机目录，无 network 键）
curl -sS -H 'Authorization: Bearer '"$LOCAL_B_TOKEN" \
  "http://127.0.0.1:${LOCAL_B_PORT}/api/v1/agents" > "$TMP/resp.json"
python3 - "$TMP/resp.json" <<'PY' || fail "本地目录应为纯本机视图（含本机 echo-demo、无 network 键）"
import json, sys
d = json.load(open(sys.argv[1]))
assert "network" not in d, d
assert all(a["node_id"] == "nodeB" for a in d.get("local", [])), d
assert any(a["card"]["name"] == "echo-demo" for a in d.get("local", [])), d
PY
pass "node 本地目录仅本机 agent（无全网代理）"

# ---------- DoD 4：超时 + 进程组整组终止 ----------
echo
echo "──── [DoD 4] sleeper 超时：failed + error 含 timeout + 进程组无幸存 ────"
api POST /api/v1/tasks '{"target_node":"nodeA","target_agent":"sleeper","input":{"args":["41"]},"submitted_by":"demo","idem_key":"d4-timeout"}'
if [ "$CODE" != "202" ]; then fail "提交返回 $CODE"; fi
TASK4=$(jget "$TMP/resp.json" task_id)
wait_status "$TASK4" 'failed' 15
api GET "/api/v1/tasks/$TASK4"
ERR4=$(jget "$TMP/resp.json" result.error)
if [[ "$ERR4" != *timeout* ]]; then fail "error 不含 timeout: $ERR4"; fi
pass "任务 failed，error=$ERR4"
for i in $(seq 1 30); do
  if ! pgrep -f 'sleep 41$' >/dev/null 2>&1; then break; fi
  if [ "$i" = 30 ]; then fail "进程组存在幸存者（pgrep sleep 41 命中）"; fi
  sleep 0.1
done
if pgrep -f 'sleep 41$' >/dev/null 2>&1; then fail "进程组存在幸存者"; fi
pass "进程组整组终止（pgrep sleep 41 无幸存）"

# ---------- DoD 5：离线信箱 ----------
echo
echo "──── [DoD 5] node 断开 → 提交入信箱 → 重连补推 → 完成 ────"
kill "$NODEB_PID" 2>/dev/null || true
wait "$NODEB_PID" 2>/dev/null || true
NODEB_PID=""
for i in $(seq 1 50); do
  api GET /api/v1/nodes
  if [ "$(node_field "$TMP/resp.json" nodeB online)" = "False" ]; then break; fi
  if [ "$i" = 50 ]; then fail "nodeB 未离线"; fi
  sleep 0.2
done
pass "nodeB 已离线"
api POST /api/v1/tasks '{"target_node":"nodeB","target_agent":"echo-demo","input":{"args":["offline-mailbox"]},"submitted_by":"demo","idem_key":"d5-mailbox"}'
if [ "$CODE" != "202" ]; then fail "离线提交返回 $CODE"; fi
TASK5=$(jget "$TMP/resp.json" task_id)
api GET "/api/v1/tasks/$TASK5"
if [ "$(jget "$TMP/resp.json" status)" != "submitted" ]; then fail "离线任务未入信箱（非 submitted）"; fi
pass "离线提交入信箱（submitted，202）"
start_nodeB
wait_node_online nodeB 20 || fail "nodeB 重启未上线"
wait_status "$TASK5" 'completed' 15
api GET "/api/v1/tasks/$TASK5"
FINAL=$(jget "$TMP/resp.json" result.final_answer)
if [ "${FINAL%$'\n'}" != "offline-mailbox" ]; then fail "补推结果不符: $FINAL"; fi
pass "重连补推完成，FinalAnswer=offline-mailbox"

# ---------- DoD 6：重连韧性（epoch 递增） ----------
echo
echo "──── [DoD 6] kill -9 nodeB → 重启 → epoch 递增（残余帧忽略由单测覆盖） ────"
api GET /api/v1/nodes
E1=$(node_field "$TMP/resp.json" nodeB epoch)
kill -9 "$NODEB_PID" 2>/dev/null || true
wait "$NODEB_PID" 2>/dev/null || true
NODEB_PID=""
start_nodeB
wait_node_online nodeB 20 || fail "nodeB 重启未上线"
api GET /api/v1/nodes
E2=$(node_field "$TMP/resp.json" nodeB epoch)
if [ "$E2" -le "$E1" ]; then fail "epoch 未递增: $E1 → $E2"; fi
pass "epoch 递增 $E1 → ${E2} （退避重连 + 重新注册）"

# ---------- DoD 7：hub 重启恢复 ----------
echo
echo "──── [DoD 7] hub 重启：能力表/任务/epoch/信箱随快照恢复 + node 自动重连 ────"
kill "$HUB_PID" 2>/dev/null || true
wait "$HUB_PID" 2>/dev/null || true
HUB_PID=""
start_hub
wait_api "$API_PORT" "$ADMIN_TOKEN" "$COOKIES" 15 || fail "hub 重启后未就绪"
wait_node_online nodeA 30 || fail "nodeA 未自动重连"
wait_node_online nodeB 30 || fail "nodeB 未自动重连"
pass "nodeA/nodeB 自动重连"
api GET /api/v1/agents
python3 - "$TMP/resp.json" <<'PY' || fail "能力表未随快照恢复"
import json, sys
d = json.load(open(sys.argv[1]))
names = {(a["node_id"], a["card"]["name"]) for a in d["agents"]}
assert ("nodeA", "echo-demo") in names and ("nodeB", "sleeper-long") in names, names
PY
pass "能力表随快照恢复（agents 目录完整）"
api POST /api/v1/tasks '{"target_node":"nodeA","target_agent":"echo-demo","input":{"args":["after-hub-restart"]},"submitted_by":"demo","idem_key":"d7-after"}'
if [ "$CODE" != "202" ]; then fail "重启后提交返回 $CODE"; fi
TASK7=$(jget "$TMP/resp.json" task_id)
wait_status "$TASK7" 'completed' 15
pass "重启后新任务正常完成"

# ---------- DoD 8：幂等 ----------
echo
echo "──── [DoD 8] 幂等：同键同内容同 task_id / 同键不同内容 409 ────"
# 「重复 offer 只执行一次」由单测覆盖（ledger.ErrDuplicate + fullchain 重复 offer 用例）
BODY8='{"target_node":"nodeA","target_agent":"echo-demo","input":{"args":["idem-x"]},"submitted_by":"demo","idem_key":"d8-idem"}'
api POST /api/v1/tasks "$BODY8"
if [ "$CODE" != "202" ]; then fail "首次提交返回 $CODE"; fi
T8A=$(jget "$TMP/resp.json" task_id)
api POST /api/v1/tasks "$BODY8"
if [ "$CODE" != "200" ]; then fail "幂等命中应 200, got $CODE"; fi
if [ "$(jget "$TMP/resp.json" dup)" != "True" ]; then fail "dup 应为 true"; fi
T8B=$(jget "$TMP/resp.json" task_id)
if [ "$T8A" != "$T8B" ]; then fail "同键两次 task_id 不同: $T8A vs $T8B"; fi
pass "同键同内容 → 同 task_id（200 dup=true）"
api POST /api/v1/tasks '{"target_node":"nodeA","target_agent":"echo-demo","input":{"args":["DIFFERENT"]},"submitted_by":"demo","idem_key":"d8-idem"}'
if [ "$CODE" != "409" ]; then fail "同键不同内容应 409, got $CODE"; fi
pass "同键不同内容 → 409 conflict"

# ---------- DoD 9：白名单 ----------
echo
echo "──── [DoD 9] 白名单：allow_env 外覆盖 PATH → failed + policy_denied ────"
api POST /api/v1/tasks '{"target_node":"nodeA","target_agent":"policy-demo","input":{"args":["hello"],"env":{"PATH":"/nonexistent"}},"submitted_by":"demo","idem_key":"d9-policy"}'
if [ "$CODE" != "202" ]; then fail "提交返回 $CODE"; fi
TASK9=$(jget "$TMP/resp.json" task_id)
wait_status "$TASK9" 'failed' 15
api GET "/api/v1/tasks/$TASK9"
ERR9=$(jget "$TMP/resp.json" result.error)
EXIT9=$(jget "$TMP/resp.json" result.exit_code)
if [[ "$ERR9" != *policy_denied* ]]; then fail "error 不含 policy_denied: $ERR9"; fi
if [ "$EXIT9" != "-1" ]; then fail "未运行的拒绝应 exit_code=-1, got $EXIT9"; fi
pass "越界 env 拒绝执行（failed + policy_denied，exit_code=-1）"

# ---------- DoD 10：needs_review ----------
echo
echo "──── [DoD 10] started 后 kill nodeB → 重启 → needs_review（非 failed、不自动重跑） ────"
api POST /api/v1/tasks '{"target_node":"nodeB","target_agent":"sleeper-long","input":{"args":["44"]},"submitted_by":"demo","idem_key":"d10-review"}'
if [ "$CODE" != "202" ]; then fail "提交返回 $CODE"; fi
TASK10=$(jget "$TMP/resp.json" task_id)
wait_status "$TASK10" '^running$' 15
pass "任务进入 running"
kill -9 "$NODEB_PID" 2>/dev/null || true
wait "$NODEB_PID" 2>/dev/null || true
NODEB_PID=""
start_nodeB
wait_node_online nodeB 20 || fail "nodeB 重启未上线"
wait_status "$TASK10" 'needs_review' 20
api GET "/api/v1/tasks/$TASK10"
ERR10=$(jget "$TMP/resp.json" result.error)
if [ "$ERR10" != "node_restarted_midway" ]; then fail "复核原因不符: $ERR10"; fi
pass "终态 needs_review（error=node_restarted_midway，非 failed，未重跑）"
REVIEW_OUT=$("${CLI[@]}" review --id "$TASK10")
echo "$REVIEW_OUT" | sed 's/^/  /' | head -8
if [[ "$REVIEW_OUT" != *"请勿重新提交"* ]]; then fail "review 未输出人话第一句"; fi
pass "swarmmindctl review 展示待核对上下文与处理指引（V0 只读）"

# CLI query 人话输出抽验
QUERY_OUT=$("${CLI[@]}" query --id "$TASK3")
if [[ "$QUERY_OUT" != *"已完成 · completed"* ]]; then fail "query 未按双语格式输出"; fi
pass "swarmmindctl query 技术态配人话（已完成 · completed）"

echo
echo "════ 全部通过：DoD 3/4/5/6/7/8/9/10 PASS（DoD 1/2 见 go build/test，DoD 11 属 P2） ════"
