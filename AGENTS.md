# SwarmMind 错误模式清单（codex 自动加载，与 CLAUDE.md 同步维护）

> 条目格式：规则式一句话 + 根因 + 自查命令。禁止空话。

## 审查守则

- 只读审查，禁止修改文件；结论按「必须修 / 建议修 / 可不修」分级，每条给文件:行号
- 对照 SPEC.md 第六节 DoD 逐条核实；zcode 声明的测试结果必须复跑验证（go build/test 可实际执行，GOCACHE/GOMODCACHE 已全局可用）
- 返工上限：同一条意见修两次不过 → 停止，上报 Hana 裁决

## Git 远程操作（2026-10-21 新增，两文件同步）

- **push 前必须先查 remote**：`git remote -v` 确认目标仓库是伦哥指定的，禁止凭记忆猜测用户名/仓库名自建 remote（2026-10-21 SwarmMind 首提误建 mtl802/SwarmMind，正确为 ai-tools-mtl/SwarmMind）。
- 公开仓库 push 前必须扫描敏感信息（真实 token/IP/密钥）：`grep -rn '<敏感前缀>' --include='*'`，无输出才推。
- 两文件（CLAUDE.md/AGENTS.md）内容需保持同步，修改一份必须同步另一份。
