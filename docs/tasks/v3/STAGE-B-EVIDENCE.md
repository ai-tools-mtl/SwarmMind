# 阶段 B 独立采证报告（STAGE-B-EVIDENCE，2026-11-02）

> 独立采证会话（fresh context，不共享开发会话结论，测试循环制度②）。基线 feat/product-v3 @ `0cdb202a`（工作树采证前干净；采证产出=本报告 + docs/tasks/v3/evidence-b/，无源码/测试改动，无 commit/push）。
> 采证对象：**本机（node 单实例，无 Hub）完整运行两种策略**（W05 验收节「B必须本机无Hub完整运行两策略」）+ AC11/12/13/14/17 本机可达面 + UI 走查。
> 方法：复用阶段 A 既定方法（STAGE-A-EVIDENCE §1：go build 强刷二进制 + mtime 核对 + 全新 data_dir 临时实例 + 回环端口 + local_token 自备 + fake CLI 镜像 zcode headless 协议），不盲信其结论。

## 0. 总结论：**采证清单全项 PASS（1 项 DEFERRED 如实登记），零产品 FAIL；观察 4 条（无必须修）**

| 采证项 | 结论 | 证据目录 |
|---|---|---|
| AC11 讨论空间实跑（manual 主人→成员→ready→start 引导 drafting→指名派发→FINAL 候选登记→定版→投影→收束态） | **PASS** | evidence-b/ac11/ |
| AC12 非法输入零执行（越权候选 403 / 非法 FINAL 400 / 终态 records 409 / 预算耗尽 409） | **PASS** | evidence-b/ac12/ |
| AC12 fake provider empty/timeout 分支 | **DEFERRED**（本机无 provider 装配面——如实登记归后续，不硬造） | — |
| AC13 写审固定缺陷链（版本→问题→claim_fix→新版本旧审查失效→复审 confirmed_fixed→伪造证据不绿→四层证据→缺口清零→finish；两 ID 稳定；两次未修暂停分支） | **PASS** | evidence-b/ac13/、evidence-b/ac13-wr2/ |
| AC17 在途阻止（finish/finalize 409 execution_in_flight + archive 409 + 无在途后流程正确） | **PASS** | evidence-b/ac17/ |
| AC14 本机可达面（external 领取顺序 W04-N10 + 重启恢复对账 + 循环事实缺席诚实拒绝） | **PASS** | evidence-b/ac14/ |
| UI 走查（w05 区五组真实渲染 + 资源页漂移横幅） | **PASS** | evidence-b/ui/、evidence-b/ui/shots/（8 张截图） |

**「真实模型质量不由 fake 通过证明」声明见 §8。**

---

## 1. 实例搭建记录（全程临时目录/回环端口，未碰生产配置）

### 1.1 构建与 embed 强刷核对

- `go build -o /tmp/stageb-bin/node ./cmd/node`（`--version` = dev，二进制 mtime 2026-11-02 13:39）。
- 强刷核对：`find internal/node/web -type f -newer /tmp/stageb-bin/node` 无输出——实测对象=最新资产构建。

### 1.2 实例与凭据

| 项 | 值 |
|---|---|
| node 启动 | `nohup /tmp/stageb-bin/node -config /tmp/stageb-run/node.yaml`，首启 PID 25331；AC14 重启采证 1 次（优雅 SIGTERM → 同 data_dir 重启 PID 30966） |
| 形态 | standalone（hub_url 空，「本机自治全功能，不连 hub」日志原文在案） |
| local API | 127.0.0.1:28801；local_token=`stageb-local-token-2026`（测试值） |
| data_dir | /tmp/stageb-run/node-data（spaces.json 原子快照/prime_token/mcp_access.json 均落此） |
| 能力注册 | disc-a / disc-b / wr-fake / rev-fake（均 session_adapter: zcode，command=/tmp/stageb-run/fakecli.sh）+ echo-contrast（无适配器对照） |
| prime_token | 测试实例自生成；MCP 编排授权经主人 `PUT /api/v1/local/mcp/access {"enabled":true}` 显式启用（evidence-b/setup/01-01-mcp-enable.json 200）——AC13 编排主体边界与 AC14 领取采证需要 |
| fake CLI | /tmp/stageb-run/fakecli.sh（留档 evidence-b/fakecli.sh.txt）：镜像 zcode 协议（首轮签发持久 sessionId 落状态文件；续轮核验 resume ID，非本脚本签发 exit 5 `no such session`；回显 `history:<累计轮数>` 证明同线程续接；prompt 内嵌 `SLEEP=<sec>` 长跑；`BOOM=1` 失败轮） |
| 成员会话 | 每成员经 POST /api/v1/sessions 登记 + 首轮投递发布（open）后绑入空间（一成员一原生线程） |

### 1.3 采证工具纪律

- 所有请求经带时间戳留档的 curl 包装（req.sh：body/http_status/应答全文逐件存 JSON，文件名序号即调用序）。
- 采证中我方客户端三次错误（空 session_id 打到 `//turns` 得 301/404；乐观锁版本过期得 409；loop 解析 bug）全部原样留档——均为零副作用诚实拒绝，且 409 version_conflict 正面实证了乐观锁生效（详见 §2/§6 注记）。
- fake CLI 的 BSD sed 不支持 `\+` 导致首条 SLEEP 派发未长跑（工具缺陷即时修复重测，非产品行为，如实登记）。

---

## 2. AC11 讨论空间实跑 —— PASS

复现关键行（B=http://127.0.0.1:28801，T=local_token，证据 ac11/NN-*.json 逐件带时间戳）：

| 步骤 | 请求 | 应答摘录（原文） |
|---|---|---|
| 建成员会话 | POST /api/v1/sessions `{"agent":"disc-a",...}` | 201 `session.id=s-995a962fd158…, adapter=zcode`；首轮后 `status=open, turns=1`（07-07-get-a.json） |
| 创建空间（manual 主人） | POST /api/v1/spaces `{"mode":"manual","members":[disc-a,disc-b 绑 session_id],"policy":{"finish_auth":"human_only"}}` | 201 `sp-0ed7733835d9…, status=pending_config, leader={manual/owner, term:1}, 成员绑定 bound_at=2026-11-02T05:46:03Z`（09） |
| ready | POST /spaces/{id}/control action=ready | 200 `applied=true, status=ready`（10） |
| start | POST control action=start | 200 `applied=true, status=running`（11） |
| **start 后引导 drafting** | GET /spaces/{id}/strategy | 200 `has_loop=true, phase="drafting", phase_valid=true, fact_seq=1`；空间 `phase="drafting", version=4`（12/13——start 本体应答 phase 空、引导落账后同源可见；trail seq 1 `循环初始化 → drafting（引导边）`） |
| 指名派发 disc-a | POST /spaces/{id}/dispatches `{"member":"disc-a","expected_version":4,"note":"…ECHO-MARK:a1"}` | 202 `status=pending, turn_seq=2, term=1, version_ref=5`（14） |
| 成员发言落账（执行链） | GET /dispatches/stageb-ac11-disp-a1 | 200 `status="converged"` + `turn: {state:"finished", status:"completed", final_answer:"ack:stageb history:2 mark:a1）", task_id:"t-c70473ed…"}`（15——history:2=同线程续接实证） |
| 成员发言落账（时间线） | POST /messages `{"member":"disc-a","text":"我倾向方案甲：成本低、落地快…"}` ×2 | 201 `used.messages=1/2`；GET messages → `seq1 member=disc-a by=owner:owner`、`seq2 member=disc-b by=owner:owner`（16/19/20——服务端主体落账，非自报） |
| disc-b 派发同链 | dispatch + query | 202→converged，`final_answer:"ack:stageb history:2 mark:b1）"`（17/18） |
| FINAL 候选登记（records 面） | POST /records `{"kind":"candidate","member":"disc-a","body":"FINAL: 定版方案甲（分阶段吸收乙的扩展性设计）","round":1}`（×2 成员） | 201 `cand-4a480c4a27f9 status=pending proposer=disc-a`、`cand-21313e0e176d status=pending proposer=disc-b`（21/22） |
| 主人候选定版 | POST /candidates/cand-4a480c4a27f9/finalize | 200 `status="finalized", by=owner`（24——服务端派生主体） |
| 主人候选否决 | POST /candidates/cand-21313e0e176d/dismiss | 200 `status="dismissed"`（25） |
| 投影候选 finalized | GET /strategy | `candidates=[(cand-4a480c4, finalized), (cand-21313e0, dismissed)]`；候选缺口从 gaps 消失（23 有 `candidate` 缺口 → 26 无） |
| **讨论收束态如实可见** | GET /records?since=0 | trail seq1-5 全链：`phase_changed(引导) → candidate_registered ×2 → candidate_finalized（by owner）→ candidate_dismissed（by owner）`（27——五值封闭 type 标注 candidate/control 如实） |

---

## 3. AC12 非法输入零执行 —— PASS

基线留档后逐项攻击，事后复核候选数/fact_seq/版本/在途全部不变（零登记零派发）：

| 攻击 | 应答 | 零副作用复核 |
|---|---|---|
| 越权提出者（非成员 ghost-user）候选 | **403** `{"error":"proposer_not_member","message":"提出者 \"ghost-user\" 不在空间成员内（非法候选拒绝，不静默换人）"}`（02） | 候选仍 2 条、状态不变、`fact_seq=5` 无新增、`in_flight=0`、version 8→8（06/07 vs 01 基线） |
| 非法 FINAL 词形（缺前缀） | **400** `candidate_not_final_form: 缺 "FINAL:" 前缀`（03） | 同上 |
| FINAL 前缀后无内容 | **400** `前缀后无内容`（04） | 同上 |
| 非法 kind=arbitrary_shell | **400** `kind 须为 candidate\|review\|issue\|evidence…数据写入只是事实登记，不等于执行`（05） | 同上 |
| 终态空间 records | 建一次性空间 → finalize(abandoned)（09 200）→ POST records → **409** `illegal_transition: 终态/已归档空间只读：不再收策略循环记录`（10）；终态候选定版同 **409**（11） | — |
| 预算耗尽派发（预算分支本机可达面） | 空间 `max_dispatch_rounds=1`：首派发 202 收敛 → 二派发 **409** `budget_exhausted: dispatch_round 已达上限 1`（21——版本正确形态；17-20 的 409 version_conflict 为我方版本过期，另证乐观锁） | — |
| fake provider empty/timeout 分支 | **DEFERRED**：本机无 provider 装配面（jev 未配置→顺序单棒手选出口是既定诚实行为，W05-02 口径）；empty/timeout 分支归 provider/jev 装配批次（后续/W07 集成面），不硬造 | — |

---

## 4. AC13 写审固定缺陷链 —— PASS（主链 + 伪造不绿 + 两次未修暂停）

### 4.1 主链（空间 sp-55f08562852a…，manual，policy.finish_auth=**verified_write_review** 主人预配置；writer=wr-fake / reviewer=rev-fake）

| 步骤 | 应答摘录 |
|---|---|
| 创建→ready→start→编写轮派发 | start 200；派发 wr-fake 202→converged `final_answer:"ack:stageb history:2 mark:wr1）"`（10/11）；`08` 首发我方版本过期 409（乐观锁零副作用，如实留档） |
| 版本登记 v1 | POST /records `{"kind":"review","op":"revision","baseline":"base-demo-1","content_sha256":H1,"patch_sha256":P1,"author":"wr-fake"}` → 201 `revision.id=rev-3fccc547ed88`（12） |
| **版本 ID 稳定（幂等）** | 同内容同补丁重放 → **200 `created=false`，同一 rev-3fccc547ed88**（13） |
| drafting→frozen→review | set_phase ×2 均 200 applied=true（15/16） |
| **悬空锚 404（发现版本须已登记）** | issue `found_in_version=HD（未登记 hash）` → **404** `revision_not_found: 发现版本 a799fb1f52f6… 未登记（问题须锚定本空间已登记版本）`（14） |
| issue I1 登记锚定 v1 | 201 `iss-214d518a5ea2`（17） |
| **问题 ID 稳定（重报同 ID）** | 同 (reviewer,file,line,title) 重报 → 201 同 `iss-214d518a5ea2`、occurrences=2（18；trail seq6 `issue_rereported 第 2 次报告`） |
| review→rework + claim_fix | 200 / 201 `{"op":"claim_fix","recorded":true}`（19/20） |
| 新版本 v2 → **旧 review 失效留痕** | revision v2 201 `rev-d65983efc7d0`（21）；trail seq10 **`review_invalidated: 版本变更 f1ac7703804e… → c04d7627dde6…：旧复审结论失效`**（23）；strategy gaps 出现 `版本不匹配：最近复审针对 f1ac7703804e…，当前版本 c04d7627dde6…（代码改变旧 review 失效）`（22） |
| rework→rereview + 复审 confirmed_fixed | set_phase 200；rereview `against=H2 result=confirmed_fixed` 201（29/30） |
| **伪造证据不绿（三连拒）** | ① owner+`source=agent_self_report, verified=true` → **400** `agent 自报不得标记已证实（须回执/服务端事实/有权人工核对）`（31）；② 编排主体（prime_token）`verified=true` → **403** `已证实标记（verified=true）只能由本机主人登记（人工核对证据）：编排主体不得自证…非自报来源标已证实同样拒绝`（32）；③ 未知来源 `magic_oracle` → **400** `未知证据来源`（33） |
| 四层证据补齐（owner 人工/回执锚） | cli_exit=executor_receipt / protocol_parse=protocol_receipt / round_complete=host_observation / work_accept=human_confirmation，均 `verified=true` 201（36-39；投影四格 ✓已证实，45） |
| **判据缺口清零** | GET /strategy → `gaps: 无`；`last_reviewed_hash == current_revision`（40） |
| 完成前拒绝面 | 编排主体 finish → **403** `主体 orchestrate:prime 非该动作有权主体（当前任期 1、负责人 manual/owner）`（26）；owner 无 term finish → **403**（27，驱动者形态须携当前任期——授权契约如实）；owner term=1 判据未满足时 finish → **409** `completion_not_satisfied（4 项缺口：phase,issue,evidence,version）` 逐条人话（28） |
| **finish 完成** | control action=finish `{"expected_version":9,"term":1}` → **200 applied=true, status=completed, final_outcome=done**，`final_reason="完成判据满足：验收条件在位、候选清零/定版、必须修清零、四层测试证据证实、版本匹配（c04d7627dde6…）、无在途"`、`final_by=owner:owner`（44） |
| 终态只读 + 归档 | set_phase→drafting 409（表外路径）；records 409 终态只读（47/48）；archive 200 archived=true（49）；重复归档 applied=false 幂等（51） |
| **两 ID 稳定断言** | 终态投影仍含 `rev-3fccc547ed88`/`rev-d65983efc7d0`/`iss-214d518a5ea2`——问题 ID/版本 ID 跨轮到完成不漂移（45） |

### 4.2 连续两次未修暂停分支（本机链路可达——实跑，空间 sp-ae6060b21632…，ac13-wr2/）

事实链经 records 面登记：v1→issue J1 `iss-b3df82355072`→claim_fix#1→v2→复审 **still_open**（failures=1，stop 仍 false，18）→claim_fix#2→v3→复审 **still_open**（failures=2）→ **GET /strategy → `stop.should_pause=true, triggers=[{kind:"fix_rounds_unresolved", detail:"同一问题修复 2 次未过复审达阈值（上报裁决）：iss-b3df82355072（2 次修复未过复审，demo/x.go:10）"}]`**（24）。落账归操作区：set_phase rereview→**paused**（200，`paused_from=rereview`）+ control pause（200，space status=paused, `paused_reason="停止建议触发：同一问题修复2次未过复审"`）（33-36）。**两 ID 稳定**：J1 与 rev-68ce3ba31963/rev-07b076c384aa/rev-5779e0234d10 全程不变（35）。暂停恢复约束如实：暂停中 set_phase 他阶段 409 `暂停恢复须回到暂停前阶段`（10-12/16/19/22 的 drafting 表外拒绝亦同链留证）。

---

## 5. AC17 在途阻止 —— PASS

长跑轮（fake CLI `SLEEP=60`）制造确定性在途窗口（`in_flight=1` 前后取证，43/48）：

| 动作（在途窗口内） | 应答 |
|---|---|
| POST /archive | **409** `illegal_transition: archive：仅已完成/已放弃空间可归档（现 running）`（44——状态门前置，409 如实） |
| POST /finalize done | **409** `execution_in_flight: 空间存在未收束执行（先等待收束或核对）`（45） |
| POST /finalize abandoned | **409** `execution_in_flight`（46） |
| POST control finish | **409** `execution_in_flight`（47） |
| 在途成员移除（configure_members） | 409 `member_in_flight` 族拒绝（ac12 空间同面；ac17 快窗 15 亦 409） |
| **无在途后流程正确** | 轮收敛（`intent=converged, turn=completed, final_answer="ack:stageb history:2"`，49）→ finalize(abandoned) **200**（51）→ archive **200 archived=true**（52） |

附带诚实面：AC17-2 空间（human_only 默认）finish → **403 `完成授权未开启（默认 human_only）：等待主人定版；开启仅主人管理面`**（20）。时间线上一次早期尝试（34，SLEEP=30 窗口）finalize(abandoned) 200——应答体 `in_flight:0` 证明服务端判定时在途已收束（我方客户端慢于轮终态），非放行在途；随后以 SLEEP=60 确定性窗口重证如上。

---

## 6. AC14 本机可达面 —— PASS

### 6.1 外部空间管家领取初始化顺序（W04-N10）

| 步骤 | 应答 |
|---|---|
| 编排主体建 manual 空间 | **403** `编排主体仅可创建 external 待领取空间（得 manual）`（01） |
| 编排主体建 external 自授 verified_write_review | **403** `完成授权仅主人可授（agent 新建空间不能自授 verified_write_review）`（02） |
| 主人建 external（成员空） | 201 `status=pending_leader, leader.term=0, claim_principals=[prime]`（03） |
| 未领取 configure_members | 编排主体 → **403** `主体 orchestrate:prime 非该动作有权主体（当前任期 0、负责人 /）`（46/51——任务书 403 口径成立）；主人 → **409** `illegal_transition: configure_members：仅待配置/暂停态可变更成员（现 pending_leader）`（04——主人过授权门后被状态门拒，两门各司其职） |
| 未领取 dispatch | 编排主体 → **403**（47/52） |
| 合法领取 | prime takeover `expected_term=0, expected_version=1` → **200** `leader=external/prime term=1 lease=…（租约至 07:16 UTC）, status=pending_config`（08） |
| 重复领取 | prime 再 takeover → **403**（09——已建任期后 claimable 不再成立，不顶替有效任期） |
| 主人显式 CAS 接管 | owner takeover `expected_term=1, takeover_kind=manual` → 200 `leader=manual/owner term=2`（10——主人任选 kind 契约） |
| 领取后管家配置成员→启动→派发 | ext2 空间全链：claim 200 → prime configure_members 200（`term:1` 驱动者形态）→ **启动前 dispatch 409** `仅运行态可派发（现 pending_config；先显式启动）`（21）→ pending_config 直接 start 409 `先 ready 就绪`（22）→ ready 200 → start 200 → dispatch 202 → **converged `final_answer:"ack:stageb history:2 mark:ext2"`**（25-28——先领取任期→再选人→显式启动，全顺序成立） |
| 会话唯一绑定 | configure 携他空间已绑会话 → **409** `session_bound_elsewhere: 会话已绑定其他协作空间（一原生线程唯一归属空间）`（13） |

### 6.2 管家断线恢复的本机替代采证（重启 node 实例）

重启前快照：11 空间（状态各异）、`spaces.json` `spaces:11, intents:9`、ac14w 空间 `has_loop=true`（rev-94fe335f88a5）、fake CLI 状态文件基线。`kill -TERM`（优雅退出，日志无异常）→ 同 data_dir 重启（standalone 完成日志在案）。

| 核验 | 结果 |
|---|---|
| **空间状态从 spaces.json 恢复** | GET /spaces：11/11 空间 ID、status、archived 全部一致（40——逐空间对照表全 OK，含 running/paused/abandoned/completed+archived/pending_config） |
| **意图账本对账（accepted/converged 不被取消不重跑）** | ext2 `ac14-ext2-disp1` 与 ac11 `stageb-ac11-disp-a1` 重启后查询均 `status=converged, turn=completed`，final_answer 原样（44/45）；fake CLI 状态文件与重启前逐字节 diff 为空——**零重跑实证** |
| 绑定守卫恢复 | 空间绑定会话普通续轮（session_send 绕过）→ **拒绝** `space_dispatch_required: 该会话已绑定协作空间，请经空间派发（space dispatch）推进，普通续轮被拒绝`（46——重启后 SessionOwners 恢复生效；HTTP 状态映射见观察 O1） |
| **策略循环事实缺席时完成判据如实拒绝** | 重启前 has_loop=true 的 ac14w 空间 → 重启后 GET /strategy `has_loop=false`（如实空态，39）；finish → **409** `completion_not_satisfied（2 项缺口：phase,version）：…无登记版本：编写产出未固定（版本/证据/审查均无从谈起）`（43——缺口清单指路，宁拒不猜，**诚实行为锚不算 FAIL**；cmd/node/main.go:306-316 明注循环事实存引擎内存、重启缺席如实拒绝为既定交接口径） |
| Hub 侧面 | **DEFERRED 归 C**（本机无 Hub；W07 交付后重验） |

---

## 7. UI 走查（轻量）—— PASS

Playwright（仓库自带 node_modules/playwright，真实 Chromium）；首屏等启动轮询尘埃落定（nav 出现 + 2.5s）；断言不用容器祖先 locator 形态（w05 区按 `[data-zone="w05"]` 直取）。截图 8 张存 evidence-b/ui/shots/。

| 断言 | 结果 | 证据 |
|---|---|---|
| 空间详情 w05 区真实渲染（重建链空间 sp-86992f…） | **PASS**：阶段行（`阶段 rereview · 当前版本 cb1732a3060b… · 最近复审 …`）/候选表（`定版交付说明…` 提出者 wr-fake 状态**已定版** 1 次）/版本表（rev-94fe335f88a5 四格缺失 vs **rev-3468743ba6a1（当前）** 四格 `✓已证实（执行回执/协议回执/服务端事实/人工核对）`）/问题表（iss-e11ab5a51e99 必须修 demo/post.go:7 **已修复确认** 1/1）/`完成判据缺口：无（判据面全满足；完成仍须显式操作）` | shots/space-wr3-w05-full.png + ui-probe2 复读（首轮脚本断言 `已修复确认` 未命中为我方读时序早于重绘——复读 PASS，非产品问题） |
| 展示红线 | **PASS**：`登记≠交付`、`证据四层分离逐版展示：agent 自报不算已证实`、`缺口清零也不自动完成——完成授权归操作区` 注记全部在页 | 同上截图 |
| 重启后循环事实缺席的诚实空态 | **PASS**：重启前有候选事实的 AC11 空间 w05 区如实显示 `本空间暂无策略循环数据：候选/版本/问题均未登记…`（不编造） | shots/space-ac11-w05-empty-after-restart.png |
| 资源页漂移横幅 | **PASS**：登记命令文件删除 → 服务页 disc-a 卡片出横幅 `检测结果已过期（基础事实与上次检测不一致）：路径已不存在（上次检测时存在）：/tmp/stageb-run/fakecli.sh——需重新检测。四层状态仍为上次检测值；请点「能力配置 → 发现本机 agent」按当前登记面重新检测。`——四层状态未被注记回写（安装 ✓ 仍在）；API 面 `detect.drift={"drifted":true,"notes":[…]}`（ui/03）；文件还原后注记随下次轮询消失（路径事实漂移为逐次便宜复核语义，如实登记） | shots/services-drift-banner.png |
| 协作空间列表页 | PASS（列表渲染 + 截图存档） | shots/spaces-list.png |

---

## 8. 如实声明

1. **真实模型质量不由 fake 通过证明**：本报告全部执行链经 fake CLI（zcode 协议镜像）完成。它证明的是**平台机制**（派发路由、意图持久与收敛、候选/版本/问题/证据的事实登记与校验、完成判据门、授权与边界拒绝、持久化与恢复对账、UI 投影），**不证明任何真实模型的行为质量**（回答质量、真实 CLI 的协议边界、额度/超时形态）。真实 CLI 面以阶段 A 的 AC04 采证为准（zcode PASS / codex DEFERRED）。
2. **采证会话独立性与局限**：本会话不共享开发上下文；结论只覆盖任务书列明的本机可达面。Hub 侧面（AC14 管家断线恢复的 Hub 语义、中心空间后端）归 C/W07。
3. **观察 4 条（无必须修；均不阻塞验收）**：
   - **O1（建议修）**：空间绑定会话的普通续轮拒绝（`space_dispatch_required`）在本地会话轮路径投 HTTP **500**（evidence-b/ac14/44-46-plain-turn-spacebound.json）——拒绝语义与人话正确、绑定守卫可靠，但状态码映射缺项：`internal/node/localapi/sessions.go:155-176`（writeLocalSessionErr 的 switch 未含 space_dispatch_required，落默认 500）；空间派发路径同码映射 409（spaces.go writeSpaceDispatchErr 有该项）。建议对齐 409。
   - **O2（登记）**：W04-N10 文面「未领取 configure_members 返回 403」在**编排主体**上成立（403 forbidden，evidence-b/ac14/46-51）；**主人主体**同动作经授权门后被状态机拒为 409 illegal_transition（04）。两门语义各自正确，登记口径差异供集成者核对测试断言主体。
   - **O3（登记，既定行为）**：策略循环事实（候选/版本/问题/证据）存引擎内存，重启后 w05 区如实空态、完成判据如实拒绝——cmd/node/main.go:306-316 既定交接口径，本轮实证其行为与文档一致（诚实行为锚）。
   - **O4（工具，非产品）**：fake CLI SLEEP 解析 BSD sed 兼容性修复一次；我方三次客户端错误（301/404、409 版本过期）均零副作用留档，反成乐观锁/幂等门的正面证据。

## 9. 清场核对

- 自启进程：node PID 25331（首启）与 30966（重启后）均 SIGTERM 优雅退出；28801 端口释放（清场后 `lsof` 无监听）。
- 临时目录：/tmp/stageb-run、/tmp/stageb-bin 用完删除（fake CLI 脚本与 node.yaml 已留档 evidence-b/）。
- 仓库：`git status --porcelain` 仅新增 docs/tasks/v3/STAGE-B-EVIDENCE.md 与 docs/tasks/v3/evidence-b/（采证产出）；无源码/测试改动；无 commit/push。
- 令牌扫描：evidence-b/ 全目录 grep 无 local_token/prime_token 明文（local_token 为测试值亦不入档）。
