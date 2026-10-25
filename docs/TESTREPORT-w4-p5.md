# TESTREPORT: W4 真实双机故障验收（P5 收官）

> 执行：Hana 编排，2026-10-25 23:05-23:15 UTC+8。dev 分支 `9cfcd92`（W1-W3 全量）交叉编译部署。
> 环境（隔离实测，生产 hub 未动）：hub-w4 @ 腾讯云（systemd `swarmmind-hub-test`，tailscale 内网 18443/管理 18444 回环，独立 data_dir /var/lib/swarmmind-test）+ node `mac-mini-test` @ Mac mini（真实 agent zcode-dev/GLM，TOFU 首连）。管理 API 经 SSH 隧道 127.0.0.1:18445。

## 场景 A：杀 Prime 接管续管闭环 ✅

Work `w-c121feb1…`（三步骤产物链，真实 GLM 执行）：
1. `prime-t1` 建单接管 → 绑定派发步骤 1（attempt #a1）→ done，回写正确
2. **模拟 Prime 崩溃**（t1 停止动作）
3. `prime-t2` takeover（expected_term 链生效）→ 读档见全貌 → 此时步骤 2 存在 t1 派发的在途尝试
4. **在途遗产事实回流照收**：t1 派的步骤 2 任务完成 → 正常回写 done，未重复执行
5. t2 仅续派步骤 3（#a3）→ done → collect → **done** ✅

接管后旧任期（prime-t1-active）绑定派发 → **HTTP 409 `work_term_conflict`** ✅（事实回流不拒、编排写入拒，与 RFC 六.2/接管语义一致）。
采证：`a1`-`a13`。

执行注记：编排初版脚本把「旧任期派发测试」放在 t2 接管之前执行（当时 t1-active 仍是现任，成功属正确行为）——时序修正后补测真 409。该失误反证了接管前后行为差异确实存在。

## 场景 B：杀 hub 恢复 ✅

Work `w-43fa1edb…`：步骤 1 running 中 `systemctl restart swarmmind-hub-test`：
- node 自动重连（epoch 递增）→ 步骤 1 结果补送 → 回写 done，**绑定与尝试无丢失**（attempt #a1 完整）
- Work 状态完整（executing/term/attempts/events 4 条）
- 续派步骤 2 → done → collect → **done** ✅

采证：`b1`-`b5`（含杀 hub 前后 UTC 时间戳）。

## 场景 C：断线 + 权限拒绝 + blocked 放弃出口 ✅

Work `w-78bab85…`：
1. 停 node → 绑定派发 → **信箱受理**（attempt #a1 submitted）→ 起 node → 补送 → completed ✅
2. 绑定派发 `PATH=/forbidden` → **failed + policy_denied**（真实入口拒绝）→ step 2 回写 failed → Work blocked ✅
3. **blocked 无死环**：`prime-t5` 接管 → collect 首次被拒并引导（「步骤 #2(failed) 未完成……确认放弃剩余步骤用 outcome=abandoned」）→ 按 abandoned 语义关闭（报告写明处置理由，failed 事实保留不掩盖）→ **closed** ✅

采证：`c1`-`c5`。

## 结论

**W4 通过，P5 四批（W1-W4）全部完成。** RFC-work-resume 两大目标实机验证：
- **执行事实与接管**：Prime 崩溃后新任期读档续管全链闭环（接管 CAS/在途遗产回流/旧任期拒绝/收集关闭）
- **多步骤断点查看**：Work 读档即见全貌（done/running/pending/failed 逐步呈现，事件账本完整）

**核验数字**：三场景 12 次真实任务派发（全部 GLM 真实执行）、接管 5 次、拒绝路径 3 类（work_term_conflict/policy_denied/work_closed+steps_not_done 引导）、重复执行 0 次、丢失绑定 0 次。采证 28 件存 `docs/testing/w4/`。

## 遗留与环境说明

- 测试实例保留：云 `swarmmind-hub-test.service`（可 `systemctl stop` 停用；数据 /var/lib/swarmmind-test）；Mac `p5-w4-test/`（node.log 可查全程）。管理台经 SSH 隧道 `127.0.0.1:18444` 浏览器可看 Work 视图。
- observing：重复收集返回 409 work_closed（原报告不被覆盖，安全侧）——「幂等回执」当前口径为拒绝重复写入而非 200 dup，RFC 口径如需 200+dup 可后续对齐。
- P5 合入 main：等 P4 观察期 Gate + 伦哥点头（版本纪律）。
