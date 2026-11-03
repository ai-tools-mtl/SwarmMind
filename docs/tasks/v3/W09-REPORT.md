# W09 收尾加固批次交付报告（2026-11-03）

> 流程：多子 agent 开发 + 双重审查（集成者把关 + codex 对抗复审 GPT-6.1 SOL 中档固定档位）逐工作块双绿定通过。基线 1aec205（v3 全案收口）→ HEAD d5c5cdc，**9 个本地提交**，未 push（push 须伦哥授权+敏感扫描）。任务书：docs/tasks/v3/W09-hardening.md（FINAL-REPORT §4 登记项的可开发部分全数处理）。集成者门禁：全仓 `go build/vet/test -count=1` **28 包全绿** + race 覆盖涉改包 + 双端 `node --check` + shared block 具名锁 9 块全绿。

## 1. 工作块与提交链（四块全部双绿定通过）

| 块 | 内容 | 提交链 | 双审要点 |
|---|---|---|---|
| A | Engine.RestoreLoop 快照持久化接线（O2 闭环：循环事实跨重启延续） | 8304c77→8972d8f→0d4d1d3 | codex R1 三必修（SaveLoops 分段取照写出内存从未存在过的组合 A₀+B₁／死锁探针构造后装回调假绿／查找拒绝路径建空 loop 而不通知）+一建议（目录 fsync 静默）→ 全收口；R2 **零必修**，三建议（测试合法参数化/一致性压力边界如实声明+回载校验 goroutine/derr 可观测）随批收口 |
| B | 留待核对人工裁决释放入口（O3 闭环：POST intents/{rid}/resolve 主人专属 + ConvergeIntent 复用 + 串行门解锁锚 + MCP 工具 + hub 页面 + SPEC 声明 30） | 78ae65d→992d3e3→0d4d1d3 | codex R1 一必修（**裁决传参冒号截断**：request_id 合法含冒号，verdict:rid 拼接 split 把 dispatch:42 截成 dispatch——Node VM 复现）+两建议（note 审计文案超实现/准入状态表）→ 全收口；R2 **零必修**（dispatch:42 全链 Node VM 八组对照闭合），余建议（事件文案二轮对齐真实留痕形态/陈旧注释）随批收口 |
| C | 绑定族细粒度并发加固（SPEC 声明 27 降范围面收窄） | 41a4b70→d5c5cdc | 三场景确定性交错矩阵（13 cell，3+4+6，bindFrameGate 帧级钉序零 sleep）。R1（集成者管道截断丢失结论头）→ R2 重审独立分析出**三必修**，其中 S3.1 为**真实生产窗**：补偿 delete-claim 删缓存后迟到低代 bind ack 把旧代写成 {live}，与 node 新代墓碑形成分裂面（hub live 旧代/node 墓碑新代 → ensureBound 缓存命中跳过补绑定 → 后续派发 node 执行口全拒、会话派发失效）；S3.2 证明墓碑低水位推进分支非防御性不可达。生产修复一处（noteBound 咨询 genSeq 分配高水位——缓存保空→新代自愈）+ S3.1b/S3.2b 两确定性 cell + S1.3 hub 缓存面断言；R3 **零必修**（三必修确认闭合；等代写/借用路径/重订阅闭包三面已核未破），两建议（cell 计数口径/M1 变异基线注记）随批收口 |
| D | O1 两端口径书面评估 + 裁决落地 | 3ecb669 + d037a72 | 评估文档 W09-O1-assessment.md（差异表/影响面/方案/裁决）；落地：hubBind 登记会话普通轮两提交面（主人 local/agent）同步拒 space_dispatch_required（与 hub 面 409 同形同码），执行口守卫保留权威兜底（登记迟到窗仍由执行口拒收——两层防线语义与统一前等价） |

## 2. 变异验证纪律（集成者亲验制）

本批次起变异咬合声明一律由集成者**亲自重放**（备份→摘除→断言精确 FAIL→还原→零多余 diff）：

- C 块 M1-M4：M1 摘 noteBound 条目高水位（基线 41a4b70）、M2 摘学得代次回写身份校验、M3 摘 genSeq 咨询→S3.1b 精确 FAIL `{gen:1}` 复活、M4 摘 compensate gen 判据→S1.3 缓存断言精确 FAIL——全部亲验成立（M1 基线注记如实入档：genSeq 门引入后单摘条目门不再触发 S3.1）。
- **报告失实第二例**（A 块）：agent 声称「变异 M2 被死锁探针捕获」，集成者读回发现探针假绿（回调在 NewEngine 构造后才装入 Config——引擎按值拷贝配置致回调恒 nil），变异声明整体失实。处置：修复探针（构造前装入+「回调确已触发」双向断言），教训入记忆——**变异/测试有效性的声明必须核测试接线本身；Go 构造按值拷贝 Config 的纪律进审查清单**。

## 3. 批次级验证汇总

- 全仓 28 包 `go test ./... -count=1` 全绿（codex 沙箱 listen 权限受限致其复跑部分包红——同套命令本机全绿，差异为环境非产品）。
- race：strategy/hub spaces/hub api/swarmmind-mcp/node service 全绿；矩阵 race 5 连 + 全包 race 多轮 0 flake。
- 双端 `node --check`；shared block 具名锁 9 块双端逐字节绿；SPEC 六.五白名单/六.六映射/声明 30 登记。
- 变异咬合亲验 6 处（A 块 3 处由 agent 自验+集成者复核接线真实性；C 块 4 处集成者亲验）。

## 4. 遗留/登记（如实）

- C 块矩阵覆盖边界：unbindGenOnce 墓碑回写「高水位推进」分支（`e.revoked && e.gen < *gen`）已由 S3.2b 闭环（R2 论断「防御性不可达」被 codex 反例推翻后补 cell）；探针学得当代次后解除属接口语义边界（生产 REST 对代次未知诚实跳过，无生产调用方直触）。
- codex 复审环境限制：其沙箱对 listen/bind 操作全面禁止——凡涉端口的产品级验收（回环/重启/重连）其「未独立验收」声明属环境边界，非产品缺陷；对应证据沿用 STAGE-C 独立采证与本机门禁。
- FINAL-REPORT §4 登记项对应关系：§4.2（绑定族）→ 本批次 C 块收口；§4.3（RestoreLoop 接线）→ A 块收口；§4.4（needs_review 释放入口）→ B 块收口；§4.5 中 O1 → D 块收口。剩余用户决策项不变：push 授权+敏感扫描、AC18 真双机环境、服务端自动取证/Hub P08 provider 配置面（既登记后续批次）。
