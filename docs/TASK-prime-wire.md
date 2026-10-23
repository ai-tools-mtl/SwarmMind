# 任务书：declare/rotate 统筹自动接线（影子 home 零手贴）

任务 ID：t20261023-62a2
日期：2026-10-23
提出人：伦哥
编排：Hana

## 背景

node 网页钦定统筹载体（`POST /api/v1/local/prime/declare`）后，应答只返回 mcp_config 配置片段 + note 提示「请立即配给统筹的 MCP 连接」，需要本机主人手动把片段粘贴进 agent 宿主的 MCP 配置。但被钦定 agent 的 `config_home` 是 node 托管的影子 home（`ExecProfile.ConfigHome`，留空则兜底 `data_dir/home`），node 对其有完全管辖权——接线应自动完成，纸条不该经过人的手。

安全边界（不可破）：prime_token 绝不经 agent 面（MCP 工具）分发或换发（SPEC 六.六豁免③）；本任务全部动作发生在 declare/rotate 这个主人人工操作的事务内，走的是主人授权，不越线。

## 目标

declare（钦定）与 rotate（轮换）成功后，node 自动把 swarmmind MCP 配置合并写入被钦定 agent 影子 home 内的宿主配置文件，实现「钦定即接线，零手贴」；探测不到已知宿主布局时降级为现有手动片段流程。

## 实现要求

1. 新增 `internal/node/localapi/primewire.go`（函数拆分自定，可并入 prime.go 但倾向独立文件）：
   - `wirePrimeConfig(home string, mcp map[string]any) (wired bool, target string, err error)` 语义：
     - 探测 `<home>/.zcode/cli/setting.json`（注意：文件名是 `setting.json`，不是 settings.json）存在且为合法 JSON → 命中 zcode 布局；
     - 命中后：读入 → 合并顶层 `mcpServers.swarmmind = mcp`（幂等，重复 declare/rotate 覆盖同键，不动其他键）→ 写前备份为 `<file>.bak-prime`（直接覆盖旧备份，不堆积）→ 原子写（临时文件 + rename，权限 0600——文件内含 prime_token 凭证）；
     - 文件不存在、目录不存在、JSON 解析失败、写失败 → `wired=false` 降级，**绝不硬写、绝不创建宿主目录**，err 可为 nil（降级不是错误）或带说明。
   - 布局探测只认 zcode 一种；未知宿主一律降级手动片段。不做泛化抽象。
2. `handlePrimeDeclare` 成功路径：若被钦定 agent 的影子 home（`ConfigHome`，空则 `data_dir/home`）接线成功 → 应答新增 `"wired": true`、`"wired_target": <绝对路径>`，note 改为告知已自动写入的路径（凭证仍仅此一次展示不变）；接线降级 → `"wired": false`，保留现有 `mcp_config` 片段与手动指引 note。
3. `handlePrimeRotate` 成功路径：同样调用 `wirePrimeConfig` 刷新影子 home 内的 `SWARM_NODE_PRIME_TOKEN`（否则旧 token 留在配置里，统筹载体下一请求 401）；应答带 wired 同口径字段。
4. 撤销统筹（如存在对应路径）：**不**清理已写入的配置（token 已即时失效，残留无害），文档说明即可。
5. 撤销/重钦定幂等：同 agent 反复 declare 不产生重复条目、不报错。

## 测试要求（上线纪律：一项不绿不交付）

- 单测覆盖：zcode 布局命中合并；幂等重复写入；备份生成与覆盖；坏 JSON 降级不硬写；目录不存在降级；rotate 后 token 刷新进配置；`ConfigHome` 为空时兜底 `data_dir/home` 路径正确。
- `go build ./...` 与 `go test ./...` 全绿；既有 prime_test.go / server_test.go 不回归。

## 文档同步

1. SPEC.md：declare/rotate 行为描述处（六.四 node 本地 API 与 prime 相关节）补「自动接线」语义与降级口径；SPEC 六.六豁免③ 补一句：影子 home 自动接线发生在主人 declare/rotate 事务内，不构成 agent 面暴露（口径对齐，不改豁免结论）。
2. README/SPEC「客户端统筹载体配置」节（5ca23bd 引入）：改为「钦定即自动接线」为主路径，手动片段降为探测未命中时的兜底说明。
3. docs/TASK-prime-wire.md 末尾追加「完成记录」节（改动文件清单 + 测试结果），供审查与交付核对。

## 已知陷阱

- zcode 配置文件名是 `setting.json`（单数）。
- 合并 JSON 用标准库，保持顶层其他键原样；不要引入第三方 JSON 库（依赖白名单哲学）。
- 影子 home 是 node 托管，但探测降级要克制：宁可 `wired=false` 让人手动贴，不可猜布局乱写。
- 禁止在 git 提交信息/注释/文档外泄任何真实 token 值（测试用假 token）。

## 完成记录（2026-10-23，zcode 实现）

### 改动文件

| 文件 | 改动 |
|---|---|
| `internal/node/localapi/primewire.go` | 新增：`wirePrimeConfig`（zcode 布局探测 `<home>/.zcode/cli/setting.json` → UseNumber 解码 → 合并 `mcpServers.swarmmind` → 写前备份 `.bak-prime`（覆盖旧备份）→ 临时文件+rename 原子写 0600；文件/目录不存在、JSON 非法、顶层或 mcpServers 非对象、写失败一律 `wired=false` 降级不硬写不建目录）；`atomicWriteFile0600`；`Server.wirePrime`/`agentShadowHome`（ConfigHome，空则 `data_dir/home`，与 runner HOME 同口径）；`applyWiredFields`（应答 wired/wired_target 字段与 note 口径） |
| `internal/node/localapi/prime.go` | `handlePrimeDeclare` 成功路径在 muPrime 段内（writeConfigMu 释放后）调 `wirePrime`，应答新增 `wired`/`wired_target`，note 按接线/降级双口径（`mcp_config` 片段两口径均保留）；`handlePrimeRotate` 同口径接线（影子 home 内刷新 `SWARM_NODE_PRIME_TOKEN`，杜绝旧 token 残留 401），轮换+接线同互斥段串行防「旧 token 后落盘盖新 token」交错；包注释更新 |
| `internal/node/localapi/server.go` | `Config` 新增 `DataDir`（影子 home 兜底 `data_dir/home` 数据源；空则该 agent 接线降级） |
| `cmd/node/main.go` | localapi 装配接线 `DataDir: cfg.DataDir` |
| `internal/node/web/app.js` | 钦定完成视图/轮换提示随 `wired` 分支：命中明示写入路径与备份文件（零手贴）、凭证展示改留档口径；降级保留手动粘贴指引；轮换 confirm 文案同步 |
| `internal/node/localapi/primewire_test.go` | 新增单测（见下） |
| `SPEC.md` | 六 node 本地 API 节补「统筹凭证与钦定·自动接线」段（语义+降级口径+rotate 刷新+撤销不清理）；六.六豁免③补自动接线不构成 agent 面暴露的口径句（豁免结论不变） |
| `deploy/prime/PRIME.md` | 「客户端统筹载体配置」三步接入改为「钦定即自动接线」主路径（预置空 setting.json → 网页钦定 → `wired:true` 即成），手动片段降为 `wired:false` 探测未命中时的兜底；验证收尾与 403 排查口径同步 |

撤销统筹不清理已写入配置（token 已即时失效，残留无害）——SPEC 与代码注释均已说明。

### 测试结果（任务书「测试要求」逐项对应）

| 要求 | 测试 | 结果 |
|---|---|---|
| zcode 布局命中合并 | `TestWirePrimeConfigZcodeMerge`（合并 swarmmind、其他键/其他 MCP 条目原样、无关键大整数字面量不经 float 换算、0600、target 绝对路径）+ `TestWirePrimeConfigMergeNoServersKey`（mcpServers 键不存在新建） | PASS |
| 幂等重复写入 | `TestWirePrimeConfigIdempotent`（三次写入同键覆盖、恰两键不堆条目）+ `TestPrimeDeclareRepeatNoDup`（反复 declare 全 200、单条目、最新凭证） | PASS |
| 备份生成与覆盖 | `TestWirePrimeConfigBackup`（首备份=原文、二次覆盖为上次写前内容、备份文件恰一个） | PASS |
| 坏 JSON 降级不硬写 | `TestWirePrimeConfigBadJSONDegrade`（原文件逐字节不动、不生成备份）+ `TestWirePrimeConfigNonObjectDegrade`（顶层/mcpServers 非对象） | PASS |
| 目录不存在降级 | `TestWirePrimeConfigMissingHomeDegrade`（err=nil 静默降级、不创建宿主目录；空 home 带说明降级）+ `TestPrimeDeclareDegradeManualFlow`（端到端：目录不存在/无布局 → wired=false、保留 mcp_config 与手动 note、.zcode 与 home 目录不被创建） | PASS |
| rotate 后 token 刷新进配置 | `TestPrimeRotateRefreshesWiredToken`（declare→rotate，配置内 token=tok-wire-2、旧 token 不残留、rotate 应答 wired 同口径） | PASS |
| ConfigHome 空兜底 data_dir/home | `TestPrimeDeclareConfigHomeFallback`（端到端落 `data_dir/home/.zcode/cli/setting.json`）+ `TestAgentShadowHome`（ConfigHome 优先/兜底/不在表/双空四分支直测） | PASS |
| 应答 wired 口径 | `TestPrimeDeclareWiresShadowHome`（wired=true + wired_target 绝对路径 + note 告知路径 + mcp_config 保留 + 文件 token 与应答一致） | PASS |

- `go build ./...` ✅；`go vet`（localapi/cmd/node）✅
- `go test ./... -count=1` ✅ 15 包全绿、0 FAIL（含既有 prime_test.go / server_test.go 全部不回归）

### 修复轮（2026-10-23，codex 审查 R1-R5，Hana 裁决全采纳）

| 条 | 修复 | 涉及文件 |
|---|---|---|
| R1 | `wirePrimeConfig` Decode 后校验 doc 非 nil：文件内容 `null` 时 Decode 成功但 doc 置 nil map，随后赋值 panic（此时凭证已换发——旧凭证失效、新凭证未正常返回）→ 降级 wired=false 不硬写 | primewire.go |
| R2 | Decode 后验证剩余仅空白到 EOF（`dec.Token()` 须 io.EOF）：`{} garbage` / `{} {}` 这类先解出对象再带尾巴的文件不再被备份覆盖 | primewire.go |
| R3 | rotate 应答与 declare 复用同一份 `mcp_config`（同一构造、两口径一致），降级时主人拿得到手贴片段，与 SPEC 六.四 / PRIME.md 承诺对齐 | prime.go |
| R4 | 占位符 command（`mcpCommand` 探测未命中）一律降级 wired=false、不落盘占位配置（Hana 裁决：占位配置无任何用处）；占位符提为包级常量 `mcpCommandPlaceholder`；新增 `Server.mcpCommandOverride` 同包测试注桩（生产不设，不构成公共配置面） | prime.go / primewire.go / server.go |

R5（建议，全采纳）测试加强明细：

- 数字精度用例改 `9007199254740993`（2^53+1，超 float64 安全整数——丢 UseNumber 走 float64 往返即换算成 9007199254740992，双向断言；原 `3000000000` 验证不了精度）
- 非对象用例补 `null`（`TestWirePrimeConfigNonObjectDegrade/顶层null`）
- 新增 `TestWirePrimeConfigTrailingGarbageDegrade`：`{} garbage` / `{} {}` / `{"a":1}\n2` 降级不动原文不生备份 + 尾部仅空白对照组（不被 R2 误伤）
- 新增 `TestPrimeRotateDegradeManualFlow`：rotate 降级应答带 mcp_config（三 env 键与新凭证同值）+ 手动指引 note（R3 断言）
- `TestPrimeDeclareRotateConcurrent` 补命中布局：echo 影子 home 预置 zcode 布局，并发收尾后真实 setting.json 内 token == 唯一存活凭证、n-1 张作废凭证零残留、无关条目原样
- 新增 `TestWirePrimeConfigWriteFailureDegrade`：目录不可写（备份 CreateTemp 失败）与 `.bak-prime` 被目录占位（rename 必失败）两分支——wired=false、err 指明环节、主文件逐字节不动、无临时文件残留
- 新增 `TestPrimeDeclarePlaceholderCommandDegrade`：布局命中但 command 占位 → wired=false、不落盘不生备份、mcp_config（占位 command）与手动指引保留
- `TestPrimeDeclareWiresShadowHome` 补落盘 command == 探测命中路径断言；`newWireServer` / `newDeclareServer` 注桩确定 command（R4 后既有 e2e 需探测命中才接线）

文档同步：SPEC 六.四命中条件明确为全文恰一个 JSON 对象（`null` / 尾部多余内容不命中）、降级清单补占位符不落盘、declare/rotate 应答两口径一致均带 mcp_config；PRIME.md 降级兜底清单同步。app.js 与 cmd/node/main.go 本轮无改动（前端只按 `wired` 字段分流，后端降级后自动跟随）。

修复轮测试结果：`go build ./...` ✅；`go vet`（localapi/cmd/node）✅；`go test ./... -count=1` ✅ 15 包全绿、0 FAIL（新增 8 个用例/子用例组全 PASS，既有 prime_test.go / server_test.go 不回归）。
