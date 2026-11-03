# 阶段 C 独立采证报告（STAGE-C-EVIDENCE，2026-11-03）

> 独立采证会话（fresh context，不共享开发会话结论，测试循环制度②）。基线 feat/product-v3 @ `afb346f`（工作树采证前干净；采证产出=本报告 + docs/tasks/v3/evidence-c/，无源码/测试改动，无 commit/push）。
> 采证对象：**W07 阶段 C（中心协作与远程路由）**——同机两进程（hub 进程 + node 进程，各自临时 data_dir / 回环端口 / 自备令牌）完整实跑。
> **机械采证口径声明**：本报告全部结论为同机两进程机械采证。按任务书 W07-04「AC18 和 SPEC11 必须真实环境，fake/同机两进程不代替」——**本报告不签产品通过**，真实双机面见 §7 DEFERRED 登记。
> 方法：复用阶段 A/B 既定方法（STAGE-A/B-EVIDENCE：go build 强刷二进制 + mtime 核对 + 全新临时实例 + 回环端口 + fake CLI 镜像 zcode 协议），不盲信其结论。

## 0. 总结论：**采证清单 5 项 PASS（1 项含观察）+ AC18 真实双机 DEFERRED 如实登记；零产品 FAIL；观察 3 条（无必须修）**

| 采证项 | 结论 | 证据目录 |
|---|---|---|
| 1. 能力版本门混跑（W07-01）：旧 node 声明缺位 → hub 投影 false + 空间操作 409 spaces_unsupported + 普通任务零牵连 | **PASS** | evidence-c/ev1/ |
| 2. 中心空间全链（W07-02）：建空间→配成员→就绪→启动（引导 drafting）→指名派发→远端执行（node 真实 fake CLI 会话轮）→converged→消息/records/strategy 投影→主人候选定版→讨论收束态（REST+页面双通道） | **PASS** | evidence-c/ev2/、evidence-c/shots/ |
| 3. 守卫与边界：绑定会话普通轮两端拒（hub 409 同步；node 受理+执行口异步拒——观察 O1）/未登记 scope 词形拒绝（单测复跑绿）/PAT 调主人专属 403/撤能力声明后 sweep 零外发 | **PASS（含观察 O1）** | evidence-c/ev3/ |
| 4. 恢复映射：kill -9 node→重启对账（needs_review 留待核对、零重跑实证）+ kill -9 hub→重启（hub_spaces.db 快照 4/4 空间恢复 + 在途意图按实物分类对账） | **PASS** | evidence-c/ev4/ |
| 5. 页面走查（playwright 真实 Chromium）：P03 列表 / P04 详情九分区 / w05 策略区 / P07 中心接入六分区 / 机器页 detect 四层徽标+明细折叠（脱敏：页面与 hub REST 响应零本机路径） | **PASS（17/17 断言）** | evidence-c/shots/（5 图 + results.json）、evidence-c/ev5-detect/ |
| 6. AC18 真实双机（异构双机/断线/权限拒绝/产物交接/外部宿主 MCP） | **DEFERRED**（须真实环境，不硬造） | §7 |

**「真实模型质量不由 fake 通过证明」声明见 §8。**

---

## 1. 实例搭建记录（全程临时目录/回环端口，未碰生产配置）

### 1.1 构建与 embed 强刷核对

- `go build -o /tmp/stagec-bin/hub ./cmd/hub && go build -o /tmp/stagec-bin/node ./cmd/node`（版本 dev，二进制 mtime 2026-11-03 09:23）。
- 强刷核对：`find internal/hub/web internal/node/web internal/cmd cmd internal/hub internal/node -type f -newer <bin>` 采证中与收尾各核一次均无输出——实测对象=最新资产构建（mtime 铁律）。

### 1.2 实例与凭据（均为测试值）

| 项 | 值 |
|---|---|
| hub 启动 | `nohup /tmp/stagec-bin/hub -config /tmp/stagec-run/hub.yaml`，首启 PID 75095；采证中 kill -9 后重启 1 次（PID 78497） |
| hub 端口 | node 接入 ws://127.0.0.1:28877/ws/node；管理面 http://127.0.0.1:28878（insecure_loopback） |
| hub 凭据 | admin_token=stagec-\*\*\*（测试值，不入档，配置留档已脱敏 tools/hub.yaml.txt）；REST 通道经 POST /api/v1/login 发会话 cookie |
| node 启动 | `nohup /tmp/stagec-bin/node -config /tmp/stagec-run/node.yaml`，首启 PID 75125；采证中 kill -9 后重启 1 次（PID 78225，epoch 1→2），hub 重启后重连（epoch 3） |
| node 端口/令牌 | local API 127.0.0.1:28879；local_token=stagec-\*\*\*（测试值） |
| data_dir | /tmp/stagec-run/hub-data（hub_spaces.db SQLite + hub_state.db）、/tmp/stagec-run/node-data（hub_space_bindings.json 等） |
| 能力注册 | c-node：hub-a / hub-b（均 session_adapter: zcode，command=/tmp/stagec-run/fakecli.sh，权限 workspace-write）+ echo-contrast（无适配器对照）；sim-old-node：old-coder（模拟节点，见 1.3） |
| fake CLI | /tmp/stagec-run/fakecli.sh（留档 tools/fakecli.sh.txt）：zcode 协议镜像（首轮签发持久原生 ID、续轮核验 resume ID 非本脚本签发 exit 5、回显 `history:<累计轮数>`、SLEEP=<sec> 长跑、ECHO-MARK:x 回显） |
| 采证工具 | req.sh 带时间戳 curl 包装（tools/req.sh.txt，每件应答存 JSON 带 `_ts/_method/_url/_status`）；playwright 1.64.1（仓库自带 node_modules，真实 Chromium） |

### 1.3 模拟节点（sim-old-node，旧版词形构造——STAGE-A 落盘 fixture 方法的线上等价形）

自备 Go 客户端（留档 tools/oldnode-main.go.txt，非仓库资产；协议 JSON 帧形按 internal/protocol 内联复制——internal 包不可站外导入）：经 WS 连 hub /ws/node，以独立 register_token 注册能力卡，卡面**有意缺位 Spaces 字段**（= 旧版 node 线上词形；session_adapter: zcode 保留——「有会话能力但 predates 空间特性」的真实混跑形态，与 internal/protocol/testdata/w07_old_node_register.json 同形）。支持：space_bind 应答（代次回带）、task_offer 延时回 task_result（gofile 门控扣住结果做在途窗）、donefile 触发 card_update 撤声明。出站帧携注册 ack 回传 epoch（hub 入站帧 epoch 校验，SPEC 4.4）。

### 1.4 采证工具纪律

- 全部请求经带时间戳留档的 req.sh（120 件 JSON 逐件入档，文件名序号即调用序）。
- 我方客户端错误全部原样留档（零副作用诚实拒绝，且反证各门生效）：①我方版本猜测过期 409 version_conflict ×3（ev1/06-07、ev3/11——乐观锁生效）；②消息漏带 expected_version 400（ev2/15-16——读档乐观锁入口校验）；③查错端点 405（ev4/09）；④helper 首版 bind 应答缺 epoch 被 hub 丢弃 → 503 bind_timeout（ev3/17——「应答验证来源/代次」防线的正面实证，见 O4）。

---

## 2. 采证项 1：能力版本门混跑 —— PASS

复现关键行（H=http://127.0.0.1:28878，证据 ev1/NN-*.json 逐件带时间戳）：

| 步骤 | 请求 | 应答摘录（原文） |
|---|---|---|
| hub 投影（新 node） | GET /api/v1/capabilities | 200 `"spaces":{"hub":true,"nodes":{"c-node":true,"sim-old-node":false},"private_history_readable":false}`（01——spaces.nodes 按卡声明如实投影；隐私边界恒 false） |
| hub 目录（卡面对照） | GET /api/v1/agents | 200 c-node 卡带 `"session_adapter":"zcode","spaces":"v1"`；sim-old-node 卡 `session_adapter` 在而 **spaces 字段缺位**（02/03——落盘 fixture 线上词形） |
| 旧 node 建会话（既有链零门） | POST /api/v1/sessions `{node_id:"sim-old-node",agent:"old-coder",…}` | 201 `session.id=s-9cccb370…, status=active`（04——普通会话链不经过版本门，零牵连） |
| 混跑空间 | POST /api/v1/spaces（成员=sim-old-node/old-coder） | 201 `sp-fe65fa69…, pending_config, leader=manual/admin term 1`（05） |
| **空间派发 → 版本门拒绝** | POST /spaces/{id}/dispatches `{member:"sim-old-node/old-coder",…}` | **409** `{"error":"spaces_unsupported","message":"目标 node 未声明空间协作能力或版本不受支持：请核对双方版本（升级 node 或 hub）后重试"}`（08；重试同 409 确定性——13） |
| 被拒派发零副作用 | GET /spaces/{id} | 200 `version=1, status=pending_config, in_flight=0`（09——意图零登记） |
| ready→start 照常（同空间） | POST control ×2 | 200 `ready` → 200 `running`（10/11——空间自身操作不受成员缺声明牵连） |
| **普通任务零牵连** | POST /api/v1/tasks `{target_node:"sim-old-node",…}` | **202** `{task_id:"t-96f68736…"}`；GET 任务 `status=submitted`（12/14——普通任务不经过版本门，受理照常排队；采证后取消清理 15） |

「声明版本三分话术」（未声明/合法词形非受支持版本/词形非法）后两支 live 不可达（受支持集合= v1，模拟卡为缺位词形），由单测锁（`internal/hub/core/spacesgate.go` 话术 + w07_spacesgate_test.go）——复跑绿见 §4.3。

---

## 3. 采证项 2：中心空间全链 —— PASS（REST+页面双通道）

空间 sp-e1d7d872780c5809a4717bbb45a8ba51（manual 主人=admin，成员 c-node/hub-a + c-node/hub-b，各绑 hub 注册表会话）。证据 ev2/NN-*.json：

| 步骤 | 应答摘录 |
|---|---|
| 建成员会话 ×2 | 201 `s-1f3aa204…(hub-a)`、`s-723c0596…(hub-b)`，绑定快照 `adapter=zcode, perm=workspace-write`（01/02） |
| 建空间（成员=开放目录词形 nodeID/agentName + session_id） | 201 `sp-e1d7d872…, pending_config`，双成员 `bound_at` 落账（03） |
| ready → start | 200 `ready` → 200 `running`（04/05） |
| **start 引导 drafting** | GET /strategy：`has_loop=true, phase="drafting", fact_seq=1`，gaps 三项如实（acceptance/phase/version）；空间 `version=4, phase="drafting"`（06/07——`循环初始化 → drafting（引导边）` trail seq1） |
| **指名派发 → 远端执行** | POST dispatches `{member:"c-node/hub-a","expected_version":4}` → 202 `status=accepted, task=t-9bb2910a…, turn_seq=1`（08）；node 日志同刻：`中心空间绑定已登记 session=s-1f3a… scope=hub:sp-e1d7… gen=1 from=hub` + `会话首轮建立 … native=fakesess-1793669500-75781-6727 adapter=zcode`；node 落盘 `hub_space_bindings.json`：`{"s-1f3a…":{"space":"hub:sp-e1d7…","generation":1}}`；GET 意图 → `status="converged"` + 轮行（09——**远端真实执行**，fake CLI 子进程跑在 node 侧） |
| 同线程续接 | hub-a 第二派发 → 轮 2 `response:"ack:stagec history:2 mark:c3"`（14——fake CLI 状态文件同 native 计数 2，**同原生线程续接实证**）；hub-b 首轮 `history:1 mark:c2`（10/11） |
| 消息落账（服务端主体） | POST messages ×2 → 201；GET → `seq1 member=c-node/hub-a by=owner:admin`、`seq2 c-node/hub-b by=owner:admin`（19-21——我方首试漏 expected_version 400 如实留档 15/16） |
| FINAL 候选登记（records 面） | POST records ×2 → 201 `cand-d208c414…(hub-a) status=pending`、`cand-3522a499…(hub-b) status=pending`（23/24） |
| strategy 投影可见 | GET /strategy：candidates 双条 pending；gaps 含 `candidate`（25）；定版/否决后 → `gaps` 无 candidate 项，candidates=`[(cand-d208…, finalized),(cand-3522…, dismissed)]`（28——**候选缺口从 gaps 消失**） |
| 主人候选定版/否决 | POST candidates/{cid}/finalize → 200 `status="finalized"`；dismiss → 200 `status="dismissed"`（26/27） |
| **讨论收束态如实可见** | GET /records?since=0：trail seq1-5 全链 `phase_changed(引导) → candidate_registered ×2 → candidate_finalized（by admin）→ candidate_dismissed（by admin）`（29——type 封闭标注 control/candidate 如实） |
| 页面双通道采样 | 见 §6：P04 九分区渲染同空间真实数据（九分区 head/goal/members/sched/msgs/compose/w05/events/actions 全 1 且内容与 REST 同源） |

---

## 4. 采证项 3：守卫与边界 —— PASS（含观察 O1）

### 4.1 绑定会话普通轮拒绝（两端）

| 端 | 请求 | 应答 |
|---|---|---|
| hub | POST /api/v1/sessions/s-1f3a…/turns（该会话绑定 hub:sp-e1d7…） | **409 同步** `{"error":"space_dispatch_required","message":"core: 该会话已绑定协作空间，请经空间派发（space dispatch）推进，普通续轮被拒绝"}`（ev3/01） |
| node 本地 | POST http://127.0.0.1:28879/api/v1/sessions/s-1f3a…/turns | 202 受理 → 任务落 **failed**，`error="space_dispatch_required: 该会话已绑定协作空间：普通会话轮不得绕过空间调度推进成员线程，请经空间派发（space dispatch）"`（ev3/02+04——执行口守卫拒；**内核零触碰**：会话 turn_count 不变、CLI 未 spawn）；重启后同验（ev4/08+26） |

**观察 O1（登记，非必须修）**：hub 侧为同步 409；node 本地面为「受理 202 + 执行口异步拒」。机制：提交侧同步守卫只查**本机 collab 绑定**（SubmitSessionTurnLocal），hubBind 登记核对在收件门/执行口（spaceTurnAllowed，internal/node/service/spacedispatch.go:605-636 注释明示该分工）；拒绝语义与错误码两端同规、零执行零触碰，唯 HTTP 同步形态不同。建议修方向：提交侧同步补查 hubBind（是否对齐由集成者裁决；node web 面不受影响——本地空间绑定路径的同步 409 已有单测锁 sessions_local_test.go TestWriteLocalSessionErr…409，STAGE-B O1 已收口）。

### 4.2 PAT 调主人专属动作 403

admin 会话建 PAT（ev3/06，明文已脱敏不入档）后：

| 动作 | 应答 |
|---|---|
| POST /spaces/{id}/finalize | **403** `该动作为主人专属（预算/策略/定版/归档/候选定版否决不开放编排面——agent 不能代调或伪造人已授权）：请由主人登录 hub 控制台执行`（07） |
| POST /spaces/{id}/archive | **403** 同话术（08） |
| POST /spaces/{id}/candidates/{cid}/finalize | **403** 同话术（09） |
| 对照：GET /spaces/{id}（读面） | 200（10——PAT 读面放行） |
| 对照：POST dispatches（编排面） | 过授权层入业务层（我方故意携过期版本 → 409 version_conflict，11——**身份成立、授权成立、业务校验照走**） |

### 4.3 未登记 scope 词形拒绝 + 撤能力声明 sweep 零外发

- **未登记 scope 词形**（node 收件门）：live 触发需伪造 hub 帧词形（Session.Space 非法词形/未登记 hub:<id>），REST/MCP 面均不开放该输入——按单测锁采证，本轮复跑：`go test ./internal/node/service/ -run TestW07SpaceTurnGate -count=1 -v` → `--- PASS: TestW07SpaceTurnGateWordformAndBinding`（词形非法/绑定不符分话术断言）；`./internal/hub/core/ ./internal/hub/spaces/ -run 'TestW07|TestCSweepGateAfterWithdraw'` 双包 ok。live 结构面由 §4.4 出口门同链佐证（同一 RequireSpacesCapable 单源）。
- **撤能力声明后 sweep 零外发（W07-02 必修C 口径，live 实证）**：模拟节点 sim-old-node 先带 `spaces:"v1"` 注册（投影翻 true，ev3/12）→ 新空间 sp-101276fd… 配成员→ready→start → 派发 d1（seq1，202 accepted——task_offer 被 helper gofile 门控扣住在途）→ 派发 d2（seq2，202 **pending**，ev3/37/39）→ **card_update 撤声明**（投影翻 false，ev3/40；hub 日志 `能力表更新 node=sim-old-node`）→ 释放 d1 结果（已接受轮按原契约收束：d1 → `converged`，ev3/41）→ sweep 兜底推进 d2 → **出口版本门取消收束**：d2 → `cancelled`，注记全文 `版本门拒绝（目标 node 未声明空间协作能力（spaces_unsupported）：node sim-old-node 能力 "old-coder" 未声明空间特性（spaces），中心空间操作不下发；请升级该 node 至装配空间协作的版本后重试）：意图取消收束`（ev3/42）；hub 日志三行同刻（tools/hub-run.log 09:51:31-32）。**零外发核对**：d2 无 task_id（从未提交）；会话轮数=1（仅撤声明前投出的 seq1），撤声明后零新任务（ev3/43）；重声明后投影即翻 true（再次 card_update 词形验证，ev3/31）。

---

## 5. 采证项 4：恢复映射 —— PASS

### 5.1 派发在途 kill node → 重启对账（零重跑实证）

长跑轮制造确定性在途（ev2 空间 hub-b，SLEEP=60）：dispatch → `accepted, task=t-9b924bef…, seq=2`（ev4/03）。`kill -9` node（PID 75125）→ 同 data_dir 重启（epoch=2）：

| 核验 | 结果 |
|---|---|
| node 重启对账诚实化 | node 日志：`重发未确认结果 task=t-9b924bef…`——在途轮按 `needs_review` 收口（err=`node_restarted_midway`），绝不虚报 |
| hub 收敛语义如实 | hub 日志原文：`中心空间轮终不确定，留待核对（不收敛不重跑不转投）… status=needs_review err="node_restarted_midway"`；意图保持 `accepted`（ev4/06），任务终态 needs_review（ev4/07） |
| **零重跑实证** | fake CLI 状态文件重启前后逐字节 diff 为空（ev4/04 基线 vs 重启后仍 `1`）——成员原生线程零重跑 |
| 绑定守卫重启后仍生效 | node 本地普通轮 → 受理后 failed `space_dispatch_required`（ev4/08+26——hub_space_bindings.json 落盘恢复生效） |
| 重启后新派发受理 | dispatch seq=3 → 202 accepted（ev4/11——不撞 seq2；滞留 pending 见 O3） |

### 5.2 kill hub → 重启快照恢复 + 在途意图对账

重启前快照：4 空间全 running（sp-fe65fa… v4 / sp-e1d7… v11 / sp-0373… v6 / sp-1012… v6），在途意图 2 条（accepted needs_review 1 条 + pending 1 条）。`kill -9` hub（PID 75095）→ 同 data_dir 重启：

| 核验 | 结果 |
|---|---|
| **spaces 快照恢复（hub_spaces.db）** | GET /spaces：4/4 空间 ID、status、version 逐一对照 **全 MATCH**（ev4/16 vs 17）；成员/会话绑定/消息（2 条）/phase 全在（ev4/23/24） |
| **在途意图按实物分类（accepted）** | needs_review 任务 → 重启对账日志：`轮终不确定（needs_review），意图留待核对 … via=accepted`——意图保持 accepted，不收敛不重跑不转投（ev4/18） |
| **在途意图按实物分类（pending 未投递）** | 无轮行实物 pending → `未投递意图已取消收束`，注记 `中心恢复对账：未投递意图不自动重跑（恢复后显式重新派发）`（ev4/19） |
| 双 node 重连 | node epoch=3、sim-old-node 重注册，能力表投影恢复同重启前（`{"c-node":true,"sim-old-node":false}`——撤声明态如实，ev4/20） |
| 空间操作恢复 | GET /strategy 200（见 O2——has_loop=false 如实空态） |

---

## 6. 采证项 5：页面走查（playwright）—— PASS（17/17）

真实 Chromium（playwright 1.64.1）；登录走「用管理令牌登录（恢复通道）」表单；首屏等启动轮询尘埃落定（`#logout` visible + 3s）；断言不用容器祖先 locator 形态（分区按 `#space-detail [data-zone=…]` 直取）。脚本留档 tools/e2e-stagec.mjs.txt，逐条结果 shots/results.json。

| 断言组 | 结果 | 摘录 |
|---|---|---|
| P03 协作空间列表 | PASS | 列表含空间条目（shots/p03-spaces-list.png） |
| P04 详情九分区 | PASS ×9 | head（`运行中 sp-e1d7… 范围 hub:sp-e1d7…（中心空间固定形态…）`）/goal/members（`会话线程（hub 注册表）`双成员）/sched（`手动 / admin · 任期 1`）/msgs（`讨论消息（2）… owner:admin`）/compose（`编排主体（PAT）不能冒充主人`注记）/w05/events（全链意图审计 `intent_accepted/converged/cancelled system:system`）/actions——九分区全 1 且与 REST 同源（shots/p04-space-detail.png） |
| w05 策略区 | PASS | 候选表（`定版方案甲…已定版`、`备选乙案…已否决`）+ `登记≠交付` + `完成判据缺口 4 项（如实全列；缺口清零也不自动完成…）`（shots/w05-strategy-zone.png） |
| 机器页 detect 四层徽标+明细折叠 | PASS | hub-a 卡 `安装✓ 配置✓ 就绪✗ 调用✓`；明细展开含 drift 横幅 + 逐层徽标（`执行就绪 未通过：…含本机路径的逐层明细留该机器 node 控制台检测面`——固定人话脱敏）+ `本页为中心只读汇聚投影…中心不远程扫描、不代写`（shots/p-nodes-detect.png） |
| P07 中心接入（设置页 #sec-mcp） | PASS | 六分区（scope/env/auth/gen/verify/maint）+ `外部调用机`词面（shots/p07-hub-integration.png） |
| **脱敏红线（hub 面）** | PASS | 三个页面可见文本 + hub /agents 响应（detect 全量字段级核对）**零 `/tmp/stagec` 本机路径**；config 层 pass 无 reason、execution_ready fail 换固定人话、drift note 无路径（ev5-detect/04——W07-03 隐私投影脱敏收口实证；node 本机检测面路径按设计保留） |

---

## 7. AC18 真实双机项 —— DEFERRED（如实登记，不硬造）

按任务书 W07-04「AC18 和 SPEC11 必须真实环境，fake/同机两进程不代替」，以下面本轮未采证，逐项列明所缺环境：

1. **真异构双机**（第二台机器/异构 OS/真实网络分区）：本会话仅一台宿主机——W07-02 的「远程」为同机双进程回环；跨机时延、网络抖动、断线重连补传（接受前离线持久排队→重连补投）、keepalive 失联语义均未在真实网络面验证。
2. **外部宿主 MCP（AC06/07 的 C 段）**：另一机器启动 stdio 桥接（swarmmind-mcp 连中心）+ tools/list 实发现 + 无副作用查询；主机无 CLI 依赖形态；「目录为空」与「连不上」的区分断言——缺外部调用机环境。
3. **AC18 固定线程原生 ID 全链**：双机各一条流程（先网页+规则/jev、后外部 MCP），固定原生 ID 跨机核验、同版代码审查（显式仓库基线/patch hash/依赖范围）、断线与权限拒绝各有证据——上两项环境在位后方可执行。
4. **旧版本混跑回归（真实旧二进制）**：本轮旧 node 为模拟客户端（卡面缺位 Spaces 字段的线上词形等价），未以真实旧版本 hub/node 二进制对跑（「两模拟 node 只能用于单测不签产品通过」——本报告同口径）。

---

## 8. 如实声明

1. **真实模型质量不由 fake 通过证明**：全部执行链经 fake CLI（zcode 协议镜像）完成，证明的是**平台机制**（能力版本门、中心空间状态机、远端派发路由、绑定代次协议、意图 outbox 与按实物对账、候选/定版事实登记、投影、双端页面、恢复语义、脱敏投影），不证明任何真实模型行为质量。
2. **机械采证不签产品通过**：同机两进程口径按 W07-04 明确不代替真实环境；AC18/SPEC11 须真实双机（§7）。
3. **观察 3 条（无必须修）**：
   - **O1（登记，建议方向待裁决）**：node 本地面绑定会话普通轮为「202 受理 + 执行口异步拒」（错误码 space_dispatch_required 两端同规、零执行零触碰），hub 面为同步 409——提交侧同步守卫只查本机 collab 绑定，hubBind 核对在执行口（设计注记如此分工，internal/node/service/spacedispatch.go:605-611）。语义等价，HTTP 同步形态不同。
   - **O2（登记，既定边界——同 STAGE-B O3 家族）**：策略循环事实（候选/版本/问题/证据/trail）hub 侧与 node 侧同为引擎内存——hub kill -9 重启后 `has_loop=false`、records trail 如实归零（strategy.go/discussion.go 源码注记「跨重启边界（如实声明，不冒认）」+ `TestNextSpeakerFinalizedNotCarriedAcrossRestart` 锚定；RestoreLoop 无生产调用点——快照接线归 W04-03/W08 交接面）。空间快照/成员/消息/意图账本在 hub_spaces.db 全量恢复不受影响。hub 面完成判据在循环事实缺席时同样如实拒绝（与 node 面同一「宁拒不猜」语义）。
   - **O3（登记）**：意图落 needs_review 留待核对后，同空间后续 pending 意图被串行门无限期滞留（RecordReview 纯登记不改状态不触发收敛——V0 无「核对后释放」入口）。对齐任务书「未知 needs_review 不自动重发」的诚实阻塞，但驱动者需知：留待核对态的空间不再推进，待核对/释放入口归后续批次。
   - **O4（工具，非产品）**：模拟节点 helper 三轮迭代（internal 包不可站外导入→帧形内联；出站帧缺 epoch 被 hub 丢弃→503 bind_timeout，反证帧来源校验防线；慢结果门控）全程留档；我方客户端错误（版本过期 409×3、消息缺 expected_version 400、错端点 405）均零副作用如实入档，反证乐观锁/校验门生效。

## 9. 清场核对

- 自启进程：hub（75095/78497）、node（75125/78225）、模拟节点 helper 各代（至 77650）全部终止；`ps` 无 stagec-bin/oldnode-bin 残留；28877/28878/28879 零监听。
- 临时目录：/tmp/stagec-run、/tmp/stagec-bin、/tmp/stagec-ev、/tmp/stagec-tools 用完删除——采证证据已全部落 docs/tasks/v3/evidence-c/（不依赖 /tmp 残留）。
- 仓库：`git status --porcelain` 仅 `docs/tasks/v3/evidence-c/`（采证产出）；无源码/测试改动；无 commit/push。
- 令牌扫描：evidence-c/ 全目录无 admin_token/local_token/register_token/PAT 明文（PAT 前缀为产品设计展示形，非凭据；配置留档已脱敏）。
