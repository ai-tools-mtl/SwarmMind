# TASK-notify-unify-20261026 · 通知整合与双端一致性（伦哥 2026-10-26 午反馈五项）

> 来源：伦哥部署后实测截图两张 + 五条反馈。基线：dev/p5-work-resume @ 48b6a4a（已部署生产双端）。
> 总原则（伦哥钦点）：相关功能开发必须中心端和客户端保持一致——本轮全部改动按 SPEC 六.五 双端契约执行。

## 背景（侦查结论）

1. **顶栏排版错乱根因**：`ensureFreshnessEl()`（hub app.js ~583 行）用 `right.prepend(sp)` 把「数据更新于」span 插到 .top-right 最前——即铃铛左边。top-right 渲染顺序成 [数据更新于][🔔][会话剩余][退出登录]，「数据更新于」与「会话剩余」同属状态信息却被铃铛隔开。SVG 铃铛本身无问题（伦哥确认）。
2. **feishu 与 webhook 重叠**：internal/notify/dispatch.go deliver 里 TypeFeishu=POST feishuMessage（msg_type text），TypeWebhook=POST terminalPayloadFor（通用 JSON）。底层同是 postHTTP，差异只在消息体。Channel struct：{ID,Type,Name,Enabled,Events,Secrets,CreatedAt}（internal/notify/channel.go:51）。
3. **node 通知现状**：渠道 CRUD+测试端点齐全（localapi/notify.go，/api/v1/local/notify/channels 五端点），UI notifyCard 就绪（node web ~945 行「standalone 补盲区」家族），TerminalNotifier 挂钩完备（service/notify.go：notifyTerminal 挂 MarkFinished 首次终态 + NotifyRecoveredTerminal 恢复旁路，去重锚点在账本）。**唯一堵点**：cmd/node/main.go `notifyDispatcherFor` 仅 standalone 装配（networked/setup 返回 nil）→ notifyReady 503 → notify_available=false → UI 隐藏。伦哥生产 node 是 networked，所以什么都看不到。
4. **node 任务指令**：Ledger Entry.Task 本就存完整任务输入（ledger.go:97「持久化完整任务」），详情单查 GET /api/v1/tasks/{id} 走 ledger.TaskView 含 Input——node 前端 refreshDrawer（~1489 行）已拿全量数据，纯前端点亮。上轮 hub 独有的 promptZoneHtml 应提升进 shared block 双端同文。
5. **settings 通用存储已有**：internal/settings（hub/node 通用，原子写+互斥+运行时可改）可承载「同时通知」开关。

## 改动一：顶栏状态信息成组

文件：internal/hub/web/app.js（ensureFreshnessEl）。

- 插入位置从 `right.prepend(sp)` 改为插在 #session-left 之前（notify-bell 之后）：`right.insertBefore(sp, document.getElementById('session-left'))`；session-left 缺失时退回 prepend（防御）。
- 效果：[数据更新于][会话剩余][🔔][退出登录]——状态成组、通知入口靠操作区。
- .top-right 已有 gap:12px，无需动间距。

## 改动二：feishu 渠道并入 webhook + 消息格式维度

文件：internal/notify/channel.go、dispatch.go、hub web app.js、node web app.js（双端同改）。

- Channel 加字段 `Format string \`json:"format,omitempty"\``（"generic"|"feishu"，空=generic）。
- **读取迁移**（OpenChannelStore 载入路径）：Type=="feishu" 的存量条目 → Type="webhook"、Format="feishu"、Secrets["url"]=Secrets["webhook_url"]（删 webhook_url 键）；迁移在内存态生效，写回时落新形态。保证伦哥生产 hub 已配的飞书渠道升级后无缝继续工作（这是硬要求）。
- ChannelTypes 移除 "feishu"（新渠道不可再建独立 feishu 类型）；TypeFeishu 常量保留仅作迁移识别。
- ValidateSecrets：webhook 类型只认 url（webhook_url 不再是合法键；迁移已归一）。sms/email/inbox 口径不变。
- deliver：TypeWebhook 分支按 Format 构建消息体——generic=terminalPayloadFor+自定义 headers；feishu=feishuMessage(msg_type text)+无自定义头（飞书机器人不支持自定义头）。
- TestChannel 同步按 Format 发测试消息。
- UI（hub 与 node 的 NOTIFY_TYPES 家族双端同改）：删除「飞书机器人」类型项；Webhook 表单加「消息格式」select（通用 JSON / 飞书机器人）；选飞书时地址字段 label 变「飞书机器人 Webhook 地址」、hint 更新为飞书路径文案（沿用本轮修正的桌面端路径+堵岔路口径）；编辑旧渠道（已迁移 format=feishu）回填正确格式。
- node 端 NOTIFY_TYPES 副本（node web ~271 行起）同改同验（node 无 inbox 项的差异保留）。

## 改动三：networked 模式 node 通知 + 「同时通知」开关

文件：cmd/node/main.go、internal/node/localapi/notify.go + server.go、internal/notify（如需）、node web app.js。

- notifyDispatcherFor 去掉 standalone 门控：三模式（standalone/networked/setup）都装配渠道存储+分发器（setup 无 data_dir 可用性不受影响，路径照开；存储打开失败仍按启动失败处理）。注释同步改写（「通知归 hub」的旧裁决由本轮取代，注明伦哥 2026-10-26 裁决与「同时通知」语义）。
- **「同时通知」开关**：internal/settings Store 承载，存储文件 data_dir/notify_settings.json，结构 {also_notify: bool}，**默认 true**（默认跟发；渠道未配置自然静默，零副作用）。
  - 语义：networked 模式下 hub 通知照常（hub 渠道），node 在本机任务终态（notifyTerminal + NotifyRecoveredTerminal 两路，去重锚点不变）时若 also_notify=true 则本地渠道也发一份；false 则 node 静默。standalone 本机通知即唯一通知，开关不参与门控（存储仍在，值忽略）。
  - 实现：serviceNotifier 包装处或 Dispatcher 前置判断读 settings（每次 TaskFinal 读一次，KB 级文件读取可接受；不做内存缓存——开关要运行时即时生效，读盘是最简单正确解）。
  - API：GET /api/v1/local/notify/settings → {also_notify}；PUT /api/v1/local/notify/settings {also_notify: bool}（校验布尔，写 settings）。
- notify_available 判定：随 Notify 非 nil 三模式自然放开（核对 local status 投影字段来源，改判定而非硬编码）。
- node web UI：notifyCard 在 networked 也渲染（沿用现有家族结构）；卡片头部加「同时通知」toggle（说明文案：「开启后，本机执行的任务终态除中心端渠道外，本机渠道也同步通知」）；standalone 模式隐藏 toggle（语义不存在）。toggle 走 PUT settings + D2 局部刷新纪律。
- hub 端无需改动（node 自主通知，hub 无感）。

## 改动四：node 任务指令分区（shared block 提升）

文件：internal/hub/web/app.js、internal/node/web/app.js（shared block 双端同文）、SPEC.md 六.五。

- promptZoneHtml（上轮 hub 独有实现）迁入「抽屉答复与日志 shared block」，双端逐字节同文；node drawerZones 加 `prompt: promptZoneHtml(t)` 键。
- node 数据源已就绪（TaskView 含 Input），无后端改动。
- hub 端 drawerZones 注释里「任务指令 hub 独有」的口径更新。
- SPEC.md 六.五白名单核对：「任务输入」相关差异条目更新（输入展示双端同构后，白名单中 hub 独有项按现实删改）；P4 通知盲区条目若提及「networked 不装配」同步更新。
- docs/CLAUDE.md 与 AGENTS.md 若有相应旧口径条目同步。

## 已知陷阱

1. 双端 shared block 逐字节锁：promptZoneHtml 迁移后 `go test ./internal/node/localapi/ -run 'TestWebMdRender|TestWebDrawer'` 必须双绿；hub/node app.js 的 shared block 标记间内容 diff 为空。
2. 存量 feishu 渠道迁移是无声硬要求：迁移逻辑加单测（构造旧形态 JSON → Load → 断言新形态+字段归一+行为不变）；升级生产时渠道静默可用。
3. 局部刷新禁整层重建（D22）；开关 toggle 走局部更新。
4. settings 读盘在 TaskFinal 热路径：单次小文件读取可接受，禁止加锁缓存引入失效复杂度（简单正确优先）。
5. 0 值退灰、hash 锚点禁用、CJK 窄列竖排——沿用既有前端纪律。
6. 工人不执行 git commit。

## DoD 验证矩阵

- [ ] go build ./... + go vet ./... + go test ./... 全绿（重点 notify、localapi、hub/web、node/web）
- [ ] feishu→webhook 迁移单测绿（旧 JSON → 新形态 + deliver 消息体正确）
- [ ] TestWebMdRender|TestWebDrawer 双绿 + shared block 双端 diff 为空
- [ ] playwright 走查：顶栏 [数据更新于][会话剩余][🔔][退出] 顺序截图
- [ ] playwright 走查：hub 设置页 webhook 格式下拉 + 飞书格式回填（迁移态）
- [ ] playwright 走查：node（networked）通知卡片可见 + 同时通知开关 + node 抽屉任务指令
- [ ] 真实环境部署后冒烟：生产 hub 存量渠道迁移后测试发送成功；Mac node networked 通知卡片可见
- [ ] 改动文件清单 + 逐条 DoD 结果输出
