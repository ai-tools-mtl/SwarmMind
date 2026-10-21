#!/bin/bash
# StartLux-Decision 云端验证脚本（bamboonode.dev 托管 27B AutoThink）
# 用法：BAMBOO_API_KEY=bk-xxx ./tests/startlux-verify.sh
# 验证内容：真实 SwarmMind 场景——「检查磁盘」子任务该派给哪个节点？

set -e
KEY="${BAMBOO_API_KEY:?请先注册 bamboonode.dev 获取 BAMBOO_API_KEY}"

echo "=== StartLux-Decision 云端验证 ==="
echo "场景：磁盘巡检子任务的目标节点分配决策"
echo

RESP=$(curl -s --max-time 30 'https://api.bamboonode.dev/v1/decisions' \
  --header "Authorization: Bearer $KEY" \
  --header 'Content-Type: application/json' \
  --data '{
  "model": "startlux-decision-27b-autothink",
  "state": "SwarmMind 协作网络当前状态 JSON：{\"nodes\":[{\"node_id\":\"mac-mini\",\"type\":\"macbook\",\"online\":true,\"inflight\":0,\"capabilities\":[\"diskcheck\",\"zcode-dev\",\"echo-demo\"]},{\"node_id\":\"baidu-node\",\"type\":\"cloud-vps\",\"online\":true,\"inflight\":0,\"capabilities\":[\"diskcheck\",\"echo-demo\",\"sleeper\",\"writer\"]}],\"subtask\":{\"description\":\"检查磁盘占用情况并回报\",\"kind\":\"inspection\",\"estimated_output\":\"1KB 文本报告\"}}",
  "questions": {
    "target": {
      "type": "choice",
      "instructions": "这个磁盘巡检子任务应该派给哪个节点执行？考虑：节点在线、具备 diskcheck 能力、当前负载、任务性质（本机磁盘巡检在哪台机器跑就检查哪台——但本次巡检对象是 baidu-node 云服务器的磁盘）",
      "criteria": {
        "mac-mini": "本地 MacBook 节点",
        "baidu-node": "腾讯云 VPS 节点"
      }
    },
    "confident": {
      "type": "noul",
      "instructions": "是否对这个分配决策有高置信度？"
    }
  },
  "think_mode": "auto"
}')

echo "$RESP" | python3 -c "
import sys, json
d = json.load(sys.stdin)
print('原始响应结构 keys:', list(d.keys()))
print()
print(json.dumps(d, ensure_ascii=False, indent=1)[:2000])
"

echo
echo "=== 判定 ==="
echo "$RESP" | python3 -c "
import sys, json
d = json.load(sys.stdin)
# 结构探测（以实际响应为准）
target = d.get('answers', d).get('target', d)
print('target 决策:', json.dumps(target, ensure_ascii=False)[:300])
" || echo "解析失败——保存原始响应到 /tmp/startlux-resp.json 人工检查"
echo "$RESP" > /tmp/startlux-resp.json
