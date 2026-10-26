# TESTPLAN: W4 真实双机故障验收（P5 收官批）

> 环境：隔离实测（不影响生产 hub）——hub-w4 @ 腾讯云（systemd swarmmind-hub-test，18443/18444，dev 9cfcd92 构建）+ node mac-mini-test @ Mac mini（tailscale wss 100.64.0.3:18443 连接，真实 agent zcode-dev/GLM）。管理 API 走 SSH 隧道至云上 127.0.0.1:18444。

## 场景 A：杀 Prime 接管续管闭环（RFC 接管语义实机验证）

1. 任期 `prime-t1` takeover → 建 Work（3 步骤线性依赖，zcode-dev 写文件+答复标记）
2. t1 绑定派发步骤 1 → 等完成（真实 GLM 执行）
3. **模拟 Prime 崩溃**：t1 停止一切动作
4. 新任期 `prime-t2` takeover（读档应见：步骤 1 done、2/3 pending）
5. t2 绑定续派步骤 2 → done → 步骤 3 → done
6. t2 collect 收集报告 → Work done

断言：A1 接管 CAS 生效（expected_term 链）；A2 每步骤恰好一次尝试（无额外执行）；A3 接管/派发/终态事件齐备且 principal=服务端主体；A4 续派不受旧任期影响；A5 collect 后 status=done。

## 场景 B：杀 hub 恢复

1. 新 Work，t3 接管，绑定派发步骤 1 → running 中 `systemctl restart swarmmind-hub-test`
2. hub 恢复 + node 自动重连（epoch 递增）→ 步骤 1 结果补送 → 回写 done
3. t3 续派步骤 2 → collect

断言：B1 恢复后 Work/尝试/绑定完整（无丢失）；B2 补送不重复执行；B3 状态机连续。

## 场景 C：断线 + 权限拒绝（Work 绑定形态）

1. 停 node-test → 绑定派发 → 信箱受理（submitted）→ 起 node → 补送完成
2. 绑定派发 env PATH=/forbidden → policy_denied → step failed 回写

断言：C1 离线信箱受理+补送；C2 拒绝在真实入口发生且回写步骤；C3 Work 状态正确反映（blocked 可接管续派）。

## 采证

全部 API 请求/响应 JSON 落 `docs/testing/w4/`，场景执行时间戳落盘，报告 `docs/TESTREPORT-w4-p5.md`。
