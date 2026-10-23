# 审查意见：declare/rotate 统筹自动接线（t20261023-62a2）

审查人：codex（只读审查 + 实测构建/测试，2026-10-23）
审查范围：primewire.go / primewire_test.go（新增），prime.go / server.go / cmd/node/main.go / web/app.js / SPEC.md / PRIME.md（修改）

## 必须修（4 条）

### R1. 顶层 `null` 触发 panic（primewire.go:52）
`json.Decoder.Decode(&doc)` 对文件内容 `null` 返回成功且 doc 置 nil，随后 63 行赋值 panic。此时 declare/rotate 已换发凭证 → 旧凭证失效、新凭证未正常返回。
修法：解码后校验 doc 非 nil（且为对象），否则走降级 wired=false。补 `null` 降级测试。

### R2. 尾部垃圾未验证即覆盖（primewire.go:50）
只 Decode 一次：`{} garbage`、`{} {}` 会先成功解析第一个对象 → 备份并覆盖原文件，违反「非法 JSON 降级、不硬写」。
修法：保留 UseNumber，Decode 后继续读取剩余内容，验证仅空白字符直到 EOF，否则降级。补 `{} garbage` / `{} {}` 用例。

### R3. rotate 降级应答缺 mcp_config（prime.go:62）
rotate 生成 MCP 配置用于接线，但应答没有返回该片段；接线失败时主人拿不到配置，与 SPEC.md:440、PRIME.md:44 承诺不一致。
修法：rotate 应答复用同一份 mcp_config（declare/rotate 两口径一致），补 rotate 降级分支断言。

### R4. 占位符 command 也宣告 wired:true（prime.go:243 / primewire.go:62 / app.js:344）
同目录找不到 swarmmind-mcp 时 command 是 `<swarmmind-mcp 绝对路径>` 占位符；现逻辑照样落盘并 wired:true，前端提示「重连后即可本机派单」但实际 MCP 无法启动。
修法：接线前校验 command 非占位符（mcpCommand 探测命中），占位符时降级 wired=false 并保留手动指引文案。

## 建议（采纳）

### R5. 测试加强（primewire_test.go:94/243/407、prime_test.go:513）
- 数字精度用例改用 `9007199254740993`（超 float64 安全整数），当前 `3000000000` 验证不了精度
- 非对象用例补 `null`
- rotate 测试补降级片段断言
- 并发测试（prime_test.go:513）补命中布局后真实 setting.json 最终凭证断言 + 备份/rename 失败用例

## 审查认可项（不需动）

- 合并/备份/原子写基本实现正确（幂等、UseNumber、0600、Sync、rename、不建宿主目录）
- declare/rotate 互斥范围覆盖接线，无「旧 token 后落盘盖新 token」交错
- DataDir 与 runner HOME 兜底口径一致（ConfigHome 优先，否则 data_dir/home）
- 未新增 agent 面凭证授予入口，两端点仍仅 local_token；token 无日志泄漏；测试全假值

## 裁决记录（Hana）

- R1-R4 全部成立，必须修；R4 修法裁决：占位符一律降级（不写入占位配置，占位配置无任何用处）
- R5 采纳（成本低，覆盖本次真实风险）
- codex 附注的 DoD 十一条未全跑：本轮改动限于 node 本地 API 接线，DoD③-⑩ 与本改动无交集，交付口径为本任务书测试要求全绿 + 既有 15 包回归绿；DoD 全项验收留给版本发布时统一跑
