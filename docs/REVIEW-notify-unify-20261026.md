# REVIEW-notify-unify-20261026 · 通知整合与双端一致性审查记录

> 任务书：docs/TASK-notify-unify-20261026.md（伦哥 2026-10-26 午五条反馈）
> 流水线：zcode 编写（权限已恢复）→ codex 审查 → 意见回流 zcode 修复 → Hana playwright 三层走查
> 基线：48b6a4a（当前生产版本）

## 改动面

22 文件 +849/-344：notify 包（channel/dispatch/store/notify/settings 新文件）、cmd/node 装配与测试、hub api+web、node localapi+service+web、双端测试同步。

## 五项落地

1. **顶栏排版**：ensureFreshnessEl 插入位置修正——[数据更新于][会话剩余][🔔][退出登录]，状态成组、铃铛靠操作区。
2. **feishu 并入 webhook**：Channel 加 Format 维度（generic/feishu）；migrateLegacyFeishu 读取时幂等迁移（Type/键归一）；deliver/TestChannel 按 Format 分流；双端 NOTIFY_TYPES 删独立飞书项、Webhook 表单加「消息格式」。**存量渠道迁移已实测**：构造旧形态 JSON → hub 启动 → 落盘归一 webhook+feishu+url。
3. **networked 通知 + 同时通知开关**：notifyDispatcherFor 去 standalone 门控（三模式装配）；开关存 data_dir/notify_settings.json（AlsoNotify *bool 指针形态承载默认 true，读失败回退 true 不吞通知）；serviceNotifier 门控接线；localapi GET/PUT /api/v1/local/notify/settings；node 控制台通知卡 networked 可见 + toggle 声明式渲染（仅 networked 显示，standalone 语义不存在）。
4. **node 任务指令**：promptZoneHtml 提升进 shared block 双端逐字节同文；node drawerZones 点亮 prompt 分区（数据源 TaskView 本含 Input，纯前端）；SPEC 六.五口径同步。
5. **双端一致**：shared block 锁测试双绿；NOTIFY_TYPES 家族双端同改（node 无 inbox 差异保留）。

## codex 审查意见与裁决（第 80 轮）

| # | 级别 | 意见 | 裁决 |
|---|------|------|------|
| 1 | 必须修 | settings.go 每次 Open 新 Store，互斥锁跨请求不生效+固定 .tmp 并发截断破坏原子写 | **采纳**：per-path writeLocks（map[路径]*sync.Mutex）串行化，保留每次读盘设计 |
| 2 | 必须修 | node 开关保存回调操作旧 checkbox 节点，轮询重建后显示与实际值脱节（codex 模拟复现） | **采纳**：声明式重写——S.notifyAlso/Saving/Pending 唯一真相，paintNotifyAlso 局部重绘，sessionEpoch 会话代次防迟到应答，失败回滚 |
| 3 | 必须修 | 合法 url+旧 webhook_url 双键并存被静默持久化 | **采纳**：webhook 类型独立拒绝 webhook_url 键（键出现即拒，无论 url 合法与否）+双键用例 |
| 4 | 建议修 | gate 语义测试空转（只测读写没测投递门控） | **采纳**：补 networked 关闭不发/开启即发/standalone 忽略开关值/损坏文件回退 true 四断言 |

四条全采纳，零驳回。修复后独立复跑：build/vet 绿、21 包测试全绿、TestWebMdRender|TestWebDrawer 双绿。

## 三层走查（17/17 + 截图人眼核查）

- 功能断言 17/17：顶栏顺序/迁移渠道在列/编辑回填（类型归一+格式+地址 legacy URL）/新建 Webhook 格式下拉/无独立飞书项/node networked 通知卡/同时通知开关显示+默认勾选+关闭持久化（API 复核）+UI 回显+重开持久化/node 抽屉任务指令 md 渲染
- 截图人眼核查：顶栏成组形态、编辑弹层完整回填（含飞书路径新文案）、node 通知卡（toggle 醒目+家族文案）、node 抽屉（任务指令 md 与 hub 同构）
- 真实语义验证：临时 hub 挂旧形态渠道文件启动，落盘归一无损

## 走查注记

- 走查脚本两处断言初版写错（顶栏顺序公式、地址字段选择器），功能本身正确——截图人眼核查裁决，断言修正后 17/17。教训：断言公式先对齐任务书目标形态再写。
- 登录节流偶发 flaky（SPA 启动重绘窗口），加响应日志后复跑通过，非产品代码问题。

## 遗留

- 生产部署时伦哥 hub 的存量飞书渠道将自动迁移（已实测验证），升级后建议在管理台点一次「发测试」确认。
- node 控制台通知渠道与 hub 渠道是两套独立配置（本机渠道 vs 中心渠道），语义不同，未合并——符合「同时通知」的双路设计。
