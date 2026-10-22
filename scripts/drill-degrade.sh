#!/bin/bash
# SwarmMind 降级演练（P4 Gate 证据）：三类故障注入与恢复验证
# 用法：./scripts/drill-degrade.sh（在 Mac 上运行，操作腾讯云 hub 与百度云 node）
set -e
# 凭据一律环境变量注入，严禁硬编码（2026-10-22 P0 教训：admin_token 曾入 public 历史）
HUB_HOST="${SWARM_HUB_HOST:?需 export SWARM_HUB_HOST=ubuntu@host}"
HUB_KEY="${SWARM_HUB_KEY:?需 export SWARM_HUB_KEY=/path/to/key}"
ADMIN_TOKEN="${SWARM_ADMIN_TOKEN:?需 export SWARM_ADMIN_TOKEN=...}"
HUB_SSH="ssh -i $HUB_KEY $HUB_HOST"
NODE_HOST="${SWARM_NODE_HOST:?需 export SWARM_NODE_HOST=root@host}"
NODE_KEY="${SWARM_NODE_KEY:?需 export SWARM_NODE_KEY=/path/to/key}"
NODE_SSH="ssh -i $NODE_KEY $NODE_HOST"
PASS=0; FAIL=0
check() { if [ "$2" = "$3" ]; then echo "✅ $1"; PASS=$((PASS+1)); else echo "❌ $1（期望 $3 实得 $2）"; FAIL=$((FAIL+1)); fi; }
# 注意：所有断言一律用 check 函数，禁止裸 && || 链（shell 优先级陷阱：成功分支也会执行 FAIL++）

echo "== 演练 1：hub 进程杀死 → systemd 自愈 → 节点自动重连 =="
$HUB_SSH 'sudo systemctl kill -s SIGKILL swarmmind-hub'
sleep 8
STATE=$($HUB_SSH 'systemctl is-active swarmmind-hub')
check "hub 自愈重启" "$STATE" "active"
RECONN=$($NODE_SSH 'sleep 5; tail -2 /var/log/swarmmind-node.log | grep -c "注册成功"')
if [ "$RECONN" -ge 1 ]; then echo "✅ node 自动重连"; PASS=$((PASS+1)); else echo "❌ node 未重连"; FAIL=$((FAIL+1)); fi

echo "== 演练 2：node 执行中杀死 → 恢复 → needs_review 语义 =="
$HUB_SSH 'curl -s -X POST http://127.0.0.1:8444/api/v1/login -H "Content-Type: application/json" -d "{\"token\":\"4380b93e2cb636f200cbcd758924ed62\"}" -c /tmp/hc -o /dev/null'
TID=$($HUB_SSH 'curl -s -b /tmp/hc -X POST http://127.0.0.1:8444/api/v1/tasks -H "Content-Type: application/json" -d "{\"target_node\":\"baidu-node\",\"target_agent\":\"sleeper\",\"input\":{\"args\":[\"90\"]},\"submitted_by\":\"drill\"}"' | python3 -c "import sys,json;print(json.load(sys.stdin)['task_id'])")
sleep 3
$NODE_SSH 'pkill -x swarmmind-node'
sleep 2
$NODE_SSH 'nohup /opt/swarmmind-node --config /opt/swarmmind/node.yaml >> /var/log/swarmmind-node.log 2>&1 &'
sleep 8
NR=$($HUB_SSH "curl -s -b /tmp/hc http://127.0.0.1:8444/api/v1/tasks/$TID" | python3 -c "import sys,json;t=json.load(sys.stdin);print(t['status'],(t.get('result') or {}).get('error',''))")
check "needs_review 语义（node_restarted_midway）" "$NR" "needs_review node_restarted_midway"

echo "== 演练 3：取消原语（running 任务即时取消）=="
TID2=$($HUB_SSH 'curl -s -b /tmp/hc -X POST http://127.0.0.1:8444/api/v1/tasks -H "Content-Type: application/json" -d "{\"target_node\":\"baidu-node\",\"target_agent\":\"sleeper\",\"input\":{\"args\":[\"90\"]},\"submitted_by\":\"drill\"}"' | python3 -c "import sys,json;print(json.load(sys.stdin)['task_id'])")
sleep 3
CODE=$($HUB_SSH "curl -s -b /tmp/hc -X POST http://127.0.0.1:8444/api/v1/tasks/$TID2/cancel -H 'Content-Type: application/json' -d '{\"reason\":\"演练\"}' -o /dev/null -w '%{http_code}'")
check "取消端点 202" "$CODE" "202"
sleep 4
ST=$($HUB_SSH "curl -s -b /tmp/hc http://127.0.0.1:8444/api/v1/tasks/$TID2" | python3 -c "import sys,json;t=json.load(sys.stdin);print(t['status'],(t.get('result') or {}).get('error',''))")
check "cancelled(user_cancelled) 终态" "$ST" "cancelled user_cancelled"

echo "== 演练 4：重复取消 → 幂等 200（客户端重试友好，设计如此）=="
CODE2=$($HUB_SSH "curl -s -b /tmp/hc -X POST http://127.0.0.1:8444/api/v1/tasks/$TID2/cancel -H 'Content-Type: application/json' -d '{}' -o /dev/null -w '%{http_code}'")
check "已终态取消幂等 200" "$CODE2" "200"
ST2=$($HUB_SSH "curl -s -b /tmp/hc http://127.0.0.1:8444/api/v1/tasks/$TID2" | python3 -c "import sys,json;t=json.load(sys.stdin);print(t['status'])")
check "重复取消后状态仍 cancelled" "$ST2" "cancelled"

echo
echo "===== 演练汇总：$PASS 通过 / $FAIL 失败 ====="
[ $FAIL -eq 0 ] && echo "P4 Gate 降级演练：通过" || echo "P4 Gate 降级演练：存在失败项"
