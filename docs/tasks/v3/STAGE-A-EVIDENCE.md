# 阶段 A 独立采证报告（STAGE-A-EVIDENCE，2026-10-31）

---

# 第十一轮采证（2026-11-01，基线 84c90f6）：SN07 裁决断言采证——**node 套件 13/13 全 PASS，阶段 A 采证收敛**

> 独立采证会话第十一次进场。84c90f6 仅改 tests/e2e-console.mjs（裁决实施：SN07 scroll 断言改测产品真实语义「轮询不把用户滚动重置到顶」——settle 后 scrollY>40 即通过、实测 scrollY 进 record 留痕、真重置到顶则诚实 FAIL）。二进制沿用核对通过（internal/cmd 无文件晚于第四轮二进制）。全新 data_dir 临时实例；10s 轮延迟夹具沿用。证据：e2e-node-sessions-r11.log、shots-r11/。

## L1. 本轮套件官方记录

**node 模式**（e2e-node-sessions-r11.log）：**13 PASS / 0 FAIL / 0 SKIP——全绿**。
- **SN07 PASS（裁决断言采证达成）**：十三子断言全真——相位A identityKept/drawerKept=true；焦点窗三真（identityKept/focusKept/caretKept）；blur 面 identityKept=true + **scrollKept=true（scrollY 实测=89，>40 未被重置到顶，实测值入 record 留痕）**；豁免窗装载不截断（到达 A=true B=true）；关闭徽标翻转（A=true B=true）。实测 89 亦印证 r10 裁决根因分析：scroll anchoring 保持视觉位置而绝对值平移（scratch 行视口上方异步插入），非产品缺陷。
- 其余 12 条 PASS 维持：SN08a/SN08/SN08b/SN01（含 c2 禁选注明+发布推进）/SN05/SN02/SN09/SN04/SN03/SN10a/**SN10（首轮发布→轮 2 取消 executed→已关闭双刷新 true/true）**/SN06。

**hub 模式**：本轮未复跑（最近记录 r9：15/15 全绿；r4-r9 六轮连证在案）。

## L2. 第十一轮总结论——**「阶段A采证全绿（codex CLI 采证 DEFERRED 除外，已登记）」**

node 套件 13/13 全 PASS，hub 套件 15/15（六轮连证记录）。十轮采证累计结论汇总：

| 采证项 | 结论 |
|---|---|
| E2E sessions hub 套件 S01-S13 | 全绿（r4/r5/r6/r7/r9 五轮 15/15 连证） |
| E2E sessions node 套件 SN01-SN10a | **全绿（本轮 13/13）** |
| AC01/AC02（fake CLI 首轮/续轮/同线程续接，套件+冒烟） | PASS |
| AC03 产品级重启恢复（同原生 ID/对话衔接/无不可恢复） | PASS |
| AC15 取消矩阵（executed/queued/幂等重放/409 先到保留/lost-race 诚实收敛） | PASS |
| AC19 双端同组件成对截图（列表五视口/详情/抽屉/modal 四组逐对核对） | PASS |
| P5/P6/P7 与 F1/F2/F3/T1/T2/T3/T4/T5b/T5c/T5d 各修复复验 | 全部 PASS |
| AC04 真实 CLI | zcode PASS（4 轮全链+受限操作拒止+额度留痕）；**codex DEFERRED**（环境凭据 403，双环境直跑复现，非产品问题，已登记） |
| P4 首轮取消会话可见性缺口 | 已登记（裁决选服务端受理登记路线，SN10 新流程已实证解耦，随批次收口） |

**阶段 A 采证定稿**：产品验收实质全绿；登记项两项（P4 批次、codex DEFERRED）均为非代码缺陷类。第十轮 scrollY 现场数据（r10 章节）与本轮实测 89 留痕共同闭环 scroll anchoring 根因链。

---

---

# 第十轮采证（2026-11-01，基线 ffc12e6）：scrollKept 时序修正采证——按返工上限停止，现场数据呈报

> 独立采证会话第十次进场。ffc12e6 仅改 tests/e2e-console.mjs（滚动标记移到双行落定后、跨轮询节拍断言）——二进制沿用核对通过。全新 data_dir 临时实例；10s 轮延迟夹具沿用。证据：e2e-*-sessions-r10.log、shots-r10/、probe-r10-scroll.mjs/json、probe-r10-exact.mjs/json（/tmp/stagea-run/）。

## K1. 本轮套件官方记录

**node 模式**（e2e-node-sessions-r10.log）：**12 PASS / 1 FAIL / 0 中断——13 条全记录**。SN10 PASS 维持（首轮发布→轮 2 取消 executed→已关闭双刷新 true/true）；SN08 系/SN01（含 c2）/SN05/SN02/SN09/SN04/SN03/SN10a/SN06 全部 PASS 维持；**唯一 FAIL=SN07，且唯一为假子断言仍=`blur 面scrollKept=false`**（identityKept=true、焦点窗三真、到达 A=true B=true、关闭翻转 A=true B=true——其余十二项全真）。

**hub 模式**：本轮未复跑（上两轮 15/15；第四轮起 r4/r5/r6/r7 四轮连证记录在案）。

## K2. scrollKept 现场数据（返工上限呈报，交由裁决）

套件内该子断言 r6/r7/r8/r10 **四轮同值确定性为假**；本轮两种独立实测均无法在套件外复现：

1. **稳态序列**（probe-r10-scroll.mjs）：列表页 scrollTo(0,160) 后逐秒采样 8s——**scrollY 恒 160**（rows=7/docH=1122 稳定），scroll 事件仅一条（=本 scrollTo），跨全部轮询节拍无位移；GET /api/v1/sessions 每节拍照常。
2. **逐字复刻套件相位B 序列**（probe-r10-exact.mjs：过滤+持焦→清过滤→等行就位→scrollTo(0,160)→scratchB 创建→+1.5s/+3.5s/+6.5s 读数）：**scrollY 序列=0→0→160→160→160→160→160，|scrollY-160|=0**——行就位后滚动可达且跨三个轮询节拍稳定。

即：**同一实例、同一序列，采证探针驱动下 scrollKept 恒真；套件上下文内四轮恒假**。差异必然在套件上下文的残余状态（SN07 相位A 抽屉/Esc、scratchA 已在列、或 nodeSessFlow 先前用例遗留的应用状态）与本探针干净状态之间——套件未输出现场 scrollY 序列（其 record 仅记布尔），而向套件内加插桩属测试文件改动，超出本采证会话权限（纪律：不修码）。**按返工上限（SN07 已历 r6→r7→r8→r10 四次收口迭代）停止迭代**，现场数据如上呈报。

## K3. 第十轮总结论

**node 套件 12/13：除 SN07 scrollKept 一个子断言外全部 PASS；产品侧零待修项**（稳态+复刻双实测证明产品轮询不重置滚动位置）。hub 15/15（四轮连证）。SN07 的 12/13 子断言为真——唯一为假项在套件上下文内四轮稳定复现、探针不可复现，需在套件内加现场 scrollY 插桩（或由裁决改判该断言的测量方式/容差）后定位，属**用例层**工作。

**「阶段A采证全绿（codex CLI 采证 DEFERRED 除外，已登记）」按套件字面（node 全 PASS）未达成——差 SN07 一条 scrollKept 子断言**，现场数据已按上限呈报待裁决。其余全部结论维持：产品九轮采证实质全证（hub 15/15；AC01-03/AC03/AC15/AC19/AC04-zcode PASS；P5/P6/P7、F1/F2/F3/T1/T2/T3/T4/T5b/T5d/T5c 主干各修复复验 PASS；取消三态/关闭收尾/豁免语义/双端同构官方 PASS；P4 维持登记且 SN10 新流程实证解耦；codex 维持 DEFERRED）。

---

---

# 第九轮采证（2026-11-01，基线 6585fbc）：T5c-2 分窗重写采证

> 独立采证会话第九次进场。6585fbc 仅改 tests/e2e-console.mjs（SN07 分窗重写）——二进制沿用核对通过。全新 data_dir 临时实例；10s 轮延迟夹具沿用。证据：e2e-*-sessions-r9.log、e2e-node-sessions-r9b.log、shots-r9/。

## J1. 本轮套件官方记录

**node 模式**（e2e-node-sessions-r9b.log；r9 首跑在 SN01 create 处遇一次服务端瞬时无响应 15s 超时（node 日志无任何该请求记录），重跑即复现正常——记为环境瞬时，非产品）：**12 PASS / 1 FAIL / 0 中断——13 条全记录**。
- **SN07 分窗重写 12/13 子断言为真**：相位A identityKept/drawerKept=true；**焦点窗三真**（identityKept/focusKept/caretKept）；豁免窗装载不截断（**到达 A=true B=true**）；关闭徽标翻转（**A=true B=true**）；**唯 blur 面 `scrollKept=false`**——blur 后滚动标记 160 在 rowInList 观察窗内漂移超 40px 容差（scratch 行入列/重绘窗口内的 scrollY 漂移；r8 对照实验已证产品滚动行为本身正常——无焦点时 scrollY 稳定 160）。
- 其余 12 条 PASS：SN08a/SN08/SN08b/SN01（含 c2+发布推进）/SN05/SN02/SN09/SN04/**SN03/SN10a/SN10（P4 解耦新流程+P6 executed 语义维持）**/SN06。

**hub 模式**（e2e-hub-sessions-r9.log）：**15 PASS / 0 FAIL——全绿第五轮连证**（r4-r9）。

## J2. SN07 唯一残余子断言定性

`blur 面 scrollKept=false`：产品行为（身份/焦点/光标保持、豁免窗装载不截断、行到达与徽标翻转）全部为真；唯滚动位置在长观察窗内漂移超 40px 容差——漂移发生在 scratch 行入列/列表重绘窗口（浏览器滚动锚定或重绘时序），属**用例容差/观察方式**问题（如容差放宽、改测列表分区相对滚动、或行到达后再取滚动标记），非产品缺陷。r8 对照实验已证：无焦点（非豁免）态下同序列 scrollY 稳定 160。

## J3. 第九轮总结论

**产品侧零待修项。** T5c-2 分窗重写复验：SN07 十三项子断言十二真，唯一残余为 blur 面滚动容差（用例层）。hub 15/15 五轮连证；node 12/13。

**「阶段A采证全绿（codex CLI 采证 DEFERRED 除外，已登记）」按套件字面（node 全 PASS）尚差 SN07 一条 blur 面滚动容差断言**——该断言的满足只需用例层调整（容差或观察方式），不涉及任何产品代码；其余 hub+node 共 28 条官方记录全部 PASS。产品验收实质（AC01-AC03、AC15 取消矩阵、P5/P6/P7 修复、双端同构、豁免语义、关闭/取消全语义）已经九轮采证全部证实。

---

---

# 第八轮采证（2026-11-01，基线 c170b83）：SN07 scrollKept 收口采证

> 独立采证会话第八次进场。c170b83 仅改 tests/e2e-console.mjs（+5 行：清过滤后条件等待 tr[data-sess] 就位再 scrollTo+markB）——二进制沿用核对通过。全新 data_dir 临时实例；10s 轮延迟夹具沿用。证据：e2e-node-sessions-r8.log、shots-r8/、逐步 scrollY 诊断 probe-r8-scroll.mjs（/tmp/stagea-run/）。

## I1. 本轮套件官方记录

**node 模式**（e2e-node-sessions-r8.log）：**12 PASS / 1 FAIL / 0 中断——13 条全记录**。
- **SN10 PASS 维持**（首轮发布→轮 2 取消 executed→已关闭双刷新 true/true）；SN08a/SN08/SN08b/SN01（含 c2+发布推进）/SN05/SN02/SN09/SN04/SN03/SN10a/SN06 全部 PASS 维持。
- **SN07 FAIL——`scrollKept=false` 唯一残余，其余九项子断言全真**（identityKept/drawerKept/焦点取毕+终验/光标取毕/到达 A/B/关闭翻转 A/B）。

## I2. scrollKept 残余根因（本轮完全定位——R1 必修10 豁免设计 vs 用例观察点冲突）

c170b83 的条件等待已在 evaluate 外、但 **15s 超时被 .catch 吞掉（行始终未出现在 DOM）**——逐步 scrollY 诊断（probe-r8-scroll.mjs，无焦点对照）：无豁免态下清过滤→行就位→scrollTo(0,160)→scrollY 稳定 160（产品行为正常）。套件失败机理：**搜索框持焦期间会话列表按豁免设计暂停绘制**（R1 必修10：豁免只决定绘制不截断加载）→ ①条件等待 15s 超时（DOM 无行，异常被吞）②scrollTo(0,160) 落在空态 DOM 被钳制（scrollY≈0）③scratchB 行永不上屏（rowInList 300s 超时——r5/r6/r7/r8 四轮「入列表=false」同源）。**scrollKept 按现行写法不可满足：焦点持有时 DOM 冻结，滚动/行观察必须移出豁免焦点窗**（blur 后观察，或豁免窗只断言身份/焦点/光标）。定性：用例重写（T5c-2），非产品缺陷——豁免暂停绘制正是 R1 必修10 的设计行为，产品无异常。

## I3. 第八轮总结论

**node 套件 12/13 官方 PASS（SN10/SN03/SN06/SN04 全部维持），产品侧零待修项。** 唯一 FAIL 为 SN07 `scrollKept` 子断言——根因已完全定位为用例观察点与 R1 必修10 豁免设计的结构性冲突（I2），修复=用例重写（焦点窗内只断言身份/焦点/光标，滚动与行观察移到 blur 后），重跑预期 13/13。

**「阶段A采证全绿」尚未按字面达成**（node 12/13）。其余全部结论维持：hub 15/15（r4-r7 状态未复跑本轮，第四轮起四轮连证记录在案）；SN10 新流程 PASS 维持；P5/P6/P7/T2/T3/T4/T5b/T5d/T5c 主干、F1/F2/F3/T1 各轮 PASS；AC01-03/AC03/AC15/AC19/AC04-zcode PASS；P4 维持登记（SN10 新流程已实证解耦）；codex 维持 DEFERRED。

---

---

# 第七轮采证（2026-11-01，基线 ef115ac）：SN07/SN10 收口采证

> 独立采证会话第七次进场。ef115ac 仅改 tests/e2e-console.mjs（SN07 相位B 次序调整：焦点/光标先行取毕→清过滤→scrollTo+markB；SN10 改投非首轮后取消解耦 P4）——二进制沿用核对通过（internal/cmd 无文件晚于第四轮二进制）。全新 data_dir 临时实例；10s 轮延迟夹具沿用。证据：e2e-*-sessions-r7.log、shots-r7/。

## H1. 本轮套件官方记录

**hub 模式**（e2e-hub-sessions-r7.log）：**15 PASS / 0 FAIL——全绿第四轮连证**（r4/r5/r6/r7）。

**node 模式**（e2e-node-sessions-r7.log）：**12 PASS / 1 FAIL / 0 中断——13 条全记录**。
- **SN10 PASS（收口验证）**：新流程全链走通——首轮正常完成发布（证据位「首轮发布=true」）→ 第二轮长延迟投递 → UI 关闭 → close_in_flight 409 拒绝（SN10a PASS）→ 取消轮 2 →「已终止执行中的轮次：进程组已整组回收」+ 轮行 **cancelled** → 关闭最终成立 → 头部/列表「已关闭」**双刷新 true/true**。P6 的 executed 语义与 P4 解耦后关闭收尾全部实证。
- 其余 11 条 PASS：SN08a/SN08/SN08b/SN01（含 c2+发布推进）/SN05/SN02/SN09/SN04/SN03/SN10a/SN06（维持）。
- **SN07 FAIL——仅剩 `scrollKept=false` 一个子断言，其余九个子断言全真**（identityKept/drawerKept/焦点取毕+终验/光标取毕/到达 A/B/关闭翻转 A/B）。SN10 与 SN04 的收口判据全部达成。

## H2. SN07 scrollKept 残余根因（用例内部绘制时序，非产品缺陷）

ef115ac 次序调整后：清过滤（input 事件）→ `scrollTo(0,160)`+markB **立即执行**，但全量列表的重绘落在下一个 5s 轮询节拍（paintSessionsView 由 pollTick 驱动）——**scrollTo 时 DOM 仍是过滤空态（内容矮，滚动被钳制到 <160），mark 后全量列表重绘、scrollY 停在钳制值** → `|scrollY-160|<40` 确定性为假（r6/r7 两轮同值复现）。产品豁免语义（identity/焦点/光标保持、数据装载不截断）不受影响——同用例其余九项全真。修法=清过滤后先 `waitForSelector('#sess-list-zone tr[data-sess]')`（等全量列表重绘落定）再 scrollTo+markB（一行级）。

## H3. 第七轮总结论

**SN10 收口验证 PASS**（P4 解耦新流程全链：首轮发布→轮 2 取消三态→已关闭双刷新）；hub 套件全绿第四轮连证；node 套件 **12/13**——唯一 FAIL 为 SN07 的 scrollKept 子断言（用例内部绘制时序，修复方法已定位如 H2）。产品侧零待修项。

**「阶段A采证全绿」尚未按字面达成**（node 12/13）：余 SN07 scrollKept 一件用例内部时序修正，重跑预期 13/13 后即可写「阶段A采证全绿（codex CLI 采证 DEFERRED 除外，已登记）」定稿。其余全部结论维持：hub 15/15；SN10 新流程 PASS；P5/P6/P7/T2/T3/T4/T5b/T5d/T5c 主干、F1/F2/F3/T1 各轮 PASS；AC01-03/AC03/AC15/AC19/AC04-zcode PASS；P4 维持登记（本轮 SN10 新流程已实证解耦可行）；codex 维持 DEFERRED。

---

---

# 第六轮采证（2026-11-01，基线 a6a1d94）：T5d/T5c 收口采证——node 套件 13 条全记录

> 独立采证会话第六次进场。a6a1d94 仅改 tests/e2e-console.mjs（T5d 前缀匹配×3+返焦 startsWith；T5c SN07 观察点按现 DOM 重写+过滤词清理解耦）——二进制沿用核对通过（internal/cmd 无文件晚于第四轮二进制）。全新 data_dir 临时实例；10s 轮延迟夹具沿用。证据：e2e-*-sessions-r6.log、shots-r6/、probe-r6-close.json、probe-r6c.json。

## G1. 本轮套件官方记录

**node 模式**（e2e-node-sessions-r6.log）：**13/13 全记录产出（零中断），11 PASS / 2 FAIL**。
- PASS（11）：SN08a/SN08/SN08b/SN01（含 c2+发布推进）/SN05/SN02/SN09/SN04/**SN03（首次官方 PASS：确认层深层态全真）**/SN10a（在途关闭拒绝）/**SN06（首次官方 PASS：字号地板+reduced-motion）**。
- FAIL（2）：**SN07**（仅剩 `scrollKept=false` 一个子断言——T5c 重写后 identityKept/drawerKept/焦点/光标/到达 A/B/关闭翻转 A/B 全部为真）；**SN10**（取消三态语义全过——入口/「已终止执行中的轮次：进程组已整组回收」toast/轮行形态=cancelled——唯「关闭收尾」head/list 已关闭未翻转）。

**hub 模式**（e2e-hub-sessions-r6.log）：**15 PASS / 0 FAIL——全绿第三轮连证**（r4/r5/r6）。

## G2. 两条 FAIL 的根因定性（判别实验证据）

### SN07 `scrollKept=false` —— 用例内部几何缺陷（T5c 残余，非产品）
相位B 时序：`fill('#sess-query','E2E 豁免锚')`（不匹配任何会话→列表进过滤空态）→ `scrollTo(0,160)` → 立即读 `scrollKept: |scrollY-160|<40`。过滤空态内容高度不足 160px+视口，浏览器钳制滚动 → scrollKept 确定性为假。用例自身构造的过滤空态使其滚动标记点不可达；产品豁免行为（identity/focus/caret 保持 + 数据装载不截断）已由同用例其余七个子断言全部为真证实。修法=滚动标记改可达偏移或按可滚动范围断言。

### SN10 收尾 head/list 未翻转 —— 已登记 P4 可见性缺口的确定性下游（非新缺陷）
判别实验（probe-r6c.json）：复刻 SN10 序列（agent 面建会话→投首轮→取消 executed）→ 服务端会话 **status=pending、turns=[(1,cancelled)]**——首轮被取消即永不完成 → 内核映射不发布（createWithID 首轮完成才发布语义）→ ①列表行自始不出现（P4 缺口本体）②详情头停留受理态「进行中」、已关闭徽标无从翻转 ③对未发布态的关闭动作不生效。SN10 的取消三态子断言全过（executed 语义真实），唯收尾断言踩在 P4 缺口上。**P4 已于第二轮采证登记并选定修复路线（服务端受理登记批次）**——SN10 收尾断言属 P4 批次的验收范围，非本轮新回归。

## G3. 第六轮总结论

**T5d/T5c 收口复验：SN03/SN06/SN10a/SN10a-取消三态/SN07-七子断言全部官方记录产出；node 套件 13 条全记录、11 PASS / 2 FAIL，零中断**；hub 套件 15/15 全绿（三轮连证）。

**「阶段A采证全绿」按字面尚未达成，但余项已全部定性且无新增产品缺陷**：
1. **SN07 `scrollKept`**：用例内部几何（T5c 残余，滚动标记在其过滤空态下不可达）——一行断言修正；
2. **SN10 收尾**：已登记 **P4** 可见性缺口的确定性下游（首轮取消→未发布）——随 P4 服务端批次收口，或用例收尾改投非首轮后取消。

除上述两条用例级余项与已登记的 P4/codex-DEFERRED 外，阶段 A 产品采证实质全绿：hub 15/15（三轮连证）；node 13 条记录 11 PASS，SN07/SN10 的 9 个产品语义子断言（豁免身份/焦点/光标/装载不截断/取消 executed/轮行如实）全数为真。前几轮全部结论维持不变。

---

---

# 第五轮采证（2026-11-01，基线 173fc4a）：T5b 修复采证 + node 套件完整走查

> 独立采证会话第五次进场。173fc4a 仅改 tests/e2e-console.mjs（4 行守卫）——核对 internal/ 与 cmd/ 无任何文件晚于第四轮二进制，**二进制沿用**（embed 纪律以「无资产/Go 改动晚于二进制」口径核对通过）。全新 data_dir 临时实例同程序同端口；10s 轮延迟夹具沿用。证据：e2e-*-sessions-r5.log、shots-r5/。

## F1. T5b 复验 + node 套件官方记录（本轮 9 条）

| 用例 | 结果 |
|---|---|
| SN08a/SN08/SN08b | PASS（维持） |
| SN01 | **PASS**（全组件含 c2 禁选注明；reqId/受理回执/登记中徽标/**发布推进** 全真——P5 修复持续有效） |
| SN05 | PASS（五视口 图高=文档高、无溢出） |
| SN02 | PASS（unverified 严格呈现/实键选中/返回） |
| SN09 | PASS（抽屉三分区/焦点陷阱/Esc 返焦） |
| SN04 | PASS（删目录可控续轮 failed：轮1=ok→轮2=failed 徽标+错误条；SN04 代码位于 SN07 后、本轮首次可达并官方 PASS） |
| SN07 | **FAIL（T5c 用例缺陷，见 F2）** |
| SN10/SN03 | **未产出（T5d 用例选择器缺陷，见 F3）**——SN10/SN03 段在「点击关闭按钮」处 15s 超时崩出，SN03/SN06/SN10 无官方记录 |

**hub 模式顺带重跑**（e2e-hub-sessions-r5.log）：**15 PASS / 0 FAIL——全绿维持**（与第四轮一致，两轮连证）。

## F2. 本轮采证发现（两项用例缺陷，非产品缺陷）

### T5c：SN07 用例断言面停留在 W01-05 改版前 DOM，按现结构不可满足（官方 FAIL）
- 相位A 在**详情页**标记并轮询 `#sess-list-zone`（identityKept 四元素之一、入列表/关闭翻转的全部观察点）——独立详情页（W01-05）路由渲染后列表分区不在 DOM，`identityKept=false` 确定性成立、入列/翻转 300s 等待不可满足 → SN07 官方 FAIL。走查原文：「抽屉开放态 节点身份=false … 入列表=false 关闭徽标翻转=false」。
- 相位B（T5b 守卫后确在列表页）identity/focus/caret 全真，但 **scrollKept=false 且 入列表=false**：phase B 聚焦搜索框期间会话列表按 R1 必修10 豁免语义**暂停绘制**（豁免只决定绘制不截断加载）——用例「临时会话入列表在豁免态下可见」的期望与豁免设计本身相悖；滚动漂移与豁免窗内数据装载一致。产品侧列表活性已由 P5 专项抽验（15s 自动推进）与 SN01 发布推进官方 PASS 双证。
- 定性：**用例重写**（观察点改为：详情页只断言抽屉/详情身份保持；回列表后再断言入列/翻转；豁免窗内断言「数据装载、绘制暂停、焦点不丢」，非「可见」）。非产品缺陷。

### T5d：SN10/SN03 关闭按钮选择器在 node DOM 上永不匹配（确定性崩出）
node 详情页关闭按钮渲染为 `data-act="sess-close:<会话ID>"`（node app.js:1174，id 内嵌冒号形式；node 动作分发按前缀匹配），套件点击 `[data-act="sess-close"]` 精确属性匹配 → **在 node 页永不命中**（该裸形式仅存在于 hub，hub app.js:4562 id 走 data-id）→ SN10/SN03 段首次点击即 15s 超时崩出，SN03/SN06/SN10 无官方记录。修法=选择器改 `[data-act^="sess-close:"]`（一行）。非产品缺陷（node 关闭功能本身存在，hub 侧同语义用例 S13/S06 全 PASS）。

## F3. 第五轮总结论

**T5b 复验 PASS**（SN07 双相位均可执行并产出官方记录、不再崩套件）；**node 套件 9 条官方记录 8 PASS**（新增 SN04），SN07 FAIL 与 SN03/SN06/SN10 缺记录均归因用例缺陷（T5c/T5d，证据如上，非产品行为）；**hub 套件两轮连证 15/15 全绿**。

**「阶段A采证全绿」仍未达成**——余 T5c/T5d 两件**纯用例缺陷**（SN07 重写观察点；SN10/SN03 选择器一行修），产品侧无待修项。此二件修复后 node 套件全量重跑（预期 12/12），若全 PASS 即可写「阶段A采证全绿（codex CLI 采证 DEFERRED 除外，已登记）」定稿。

前几轮全部结论维持：P5/P6/P7/T2/T3/T4/T5b PASS；F1/F2/F3/T1 PASS；AC01-03（fake CLI 面）/AC03/AC15/AC19/AC04-zcode PASS；P4 维持登记；codex 维持 DEFERRED。

---

---

# 第四轮采证（2026-11-01，基线 9ae8f56）：P6/P7/T5 修复采证

> 独立采证会话第四次进场。双二进制强刷（hub core 本轮有改动：dispatch.go/workdir.go 四面对称豁免；node web 下拉同构），mtime 核对通过；全新 data_dir 临时实例同程序同端口；10s 轮延迟夹具沿用（S13 时延语义，同第三轮）。证据：e2e-*-sessions-r4.log、shots-r4/、probe-r4-p6.mjs/json。

## E1. 三项修复复验结论（9ae8f56）

| 修复 | 复验方式 | 结论 |
|---|---|---|
| P6 hub 派发取消自引用死锁 | hub 套件 S13b 官方实跑 + P6 专项抽验（进程终止时刻计量） | **PASS**。①官方 S13b **PASS**：接受取消（「取消任务已投递」）⇒ 轮行落 **cancelled** 形态。②专项抽验：hub REST 取消执行中轮（SLEEP=30 长跑）→ 回执 **0.01s** 受理（携 cancel_task_id）→ **1.02s** 轮行收敛 `cancelled / turn_cancelled_executed: 本轮执行已被取消（进程组已整组终止并回收，无孤儿）`；node 日志「会话轮取消任务收口 outcome=executed 记账=turn_cancelled_executed」；取消任务 created 01:49:55 → 收口 01:49:56（**约 1s，非被动等待**；修复前 6.4-16s）。根因（dispatch 出队门控 workdirFree 同键自锁）与三轮采证时间线吻合 |
| P7 node 下拉同构禁选 | node 套件 SN01 官方实跑 | **PASS**。SN01 全组件含 c2 禁选注明官方 PASS（全部本机能力在列、无适配器者禁选+「（未声明会话适配器）」注记） |
| T5 SN07 回列表守卫 | node 套件实跑 | **部分 PASS（相位A 守卫生效；相位B 前置仍缺——T5b）**。相位A 正常执行（回列表→点行→抽屉开放→豁免断言），但抽屉 Esc 后页面驻留**详情页**，相位B `#sess-query`（列表搜索框）15s 超时崩出 → SN07 本身与 SN03/SN04/SN06/SN10 仍无官方记录 |

## E2. 本轮套件官方记录

**hub 模式**（e2e-hub-sessions-r4.log）：**15 PASS / 0 FAIL / 0 SKIP——全绿**。含 S13b 首次官方 PASS（接受取消 ⇒ 轮行 cancelled）、S13a/S06/S09 维持 PASS。

**node 模式**（e2e-node-sessions-r4.log）：**7 PASS / 0 FAIL / 5 未执行（T5b）**。PASS：SN08a/SN08/SN08b/**SN01（含 c2）**/SN05/SN02/SN09。未执行：SN07（相位B 前置缺失中断）与被其阻断的 SN03/SN04/SN06/SN10。

## E3. 本轮发现

### T5b（采证工具缺陷，T5 残余）：SN07 相位B 前缺回列表守卫
9ae8f56 的 T5 修复加了相位A 前回列表守卫，但相位A 本身点行进入详情页（抽屉断言面），Esc 关抽屉后驻留详情；相位B 点击列表页搜索框 `#sess-query` 超时崩出。修复=相位B 前补 `[data-act="sess-back"]`/回列表守卫（一行，与相位A 守卫同式）。证据：e2e-node-sessions-r4.log 尾部（URL=#/sessions/{id}，waiting for `#sess-query`）。

## E4. 第四轮总结论

**P6/P7 修复复验 PASS，T5 部分生效（T5b 残余）**：
- **hub 套件全绿（15/15）**，S13b 的「接受取消 ⇒ 秒级 cancelled + executed 语义」经官方与专项双证；
- node 套件已记录的 7 条全部 PASS（SN01 含 c2 禁选注明恢复、发布推进维持）；
- **未达「阶段A采证全绿」**：余 **T5b** 一件（工具，一行守卫）——修复后 node 套件走完 SN07/SN03/SN04/SN06/SN10 五条，若全 PASS 即可定稿。

前几轮各项结论维持：P5/T2/T3/T4 PASS 不变（本轮 SN01/SN08/SN08b 官方再证）；F1/F2/F3/T1 PASS 不变（本轮 S10/S12/S04/S05/S13a 官方再证）；AC03/AC15/AC04-zcode/AC19 结论不变；P4 维持登记；codex 维持 DEFERRED。

---

---

# 第三轮采证（2026-11-01，基线 28e720a）：P5/T2/T3/T4 修复采证

> 独立采证会话第三次进场。程序同前两轮（embed 强刷重建并核对 mtime、全新 data_dir 临时实例 hub 28443/28444 + node 28545）。**夹具变更（如实登记）**：假 CLI 启用 10s 全局轮延迟（控制文件 /tmp/stagea-run/fake-turn-delay，脚本已存 evidence/instance/fakecli.sh）——hub 套件 S13 序列（在途关闭拒绝→取消→收尾）按设计假设的真实 CLI 秒级时延运行；首轮/二轮的快速夹具（0.4s/轮）使 S13a 关闭永远晚于轮终态，属夹具时延错配而非产品行为。证据：e2e-*-sessions-r3*.log、shots-r3/、probe-r3-*.mjs/json（/tmp/stagea-run/）。

## D1. 四项修复复验结论（28e720a）

| 修复 | 复验方式 | 结论 |
|---|---|---|
| P5 会话列表轮询冻结 | node 套件 SN01 官方记录 + 专项 30s 抽验 | **PASS**。根因（paintSessionsView 调用已改名的 sessChipsZone → ReferenceError 逃逸外层 catch）与二轮采证诊断完全吻合。官方 SN01「发布推进」=true（提交→受理回执→登记中徽标→发布推进全链走通）；专项抽验：modal 提交后不刷新，列表 **15s 内自动从「登记中」推进**（≤30s 判据，probe-r3-p5.json），轮询活性恢复 |
| T2 postDataJSON 误用 | node 套件实跑 | **PASS**。SN01 越过原崩点（create/turn 应答被套件正常消费），SN01-SN10a 不再因此中断 |
| T3 hub 套件 S13a 后崩 | hub 套件实跑 | **PASS（套件全量走完不崩）**。15 条全部产出官方记录：14 PASS / 1 FAIL（S13b，见 D2）；S13b/S06/S09 官方记录首次产出 |
| T4 SN08/SN08b/SN01 条件等待 | node 套件实跑 | **PASS**。SN08 **PASS**（列序在数据落定后读取：标题\|agent\|机器\|工作区\|状态\|最近活动（UTC）\|操作）、SN08b **PASS**（选中过滤/再点复位全真）、SN01 组件等待稳定 |

## D2. 本轮套件官方记录

**hub 模式**（e2e-hub-sessions-r3b.log）：**14 PASS / 1 FAIL，全量走完不中断**。PASS：S01/S02/S02b/S03/S11/S10/S07/S12/S08/S04/S05/**S13a**（在途关闭拒绝：toast「会话仍有在途轮次，已拒绝关闭——请先取消或等待在途轮次完成」）/**S06**/**S09**。FAIL：**S13b**（见 P6）。

**node 模式**（e2e-node-sessions-r3.log）：**6 PASS / 1 FAIL / 5 未执行（T5 崩溃）**。PASS：SN08a/**SN08**/**SN08b**/**SN05**/SN02/**SN09**。FAIL：**SN01**（仅 c2 一个组件，见 P7；其余组件全真——reqId=true/受理回执「已受理」/登记中徽标=true/**发布推进=true**）。未执行：SN03/SN04/SN06/SN07/SN10——SN09 尾态在详情页，SN07 相位A 直点列表行超时崩出（**T5**，与 hub T1 同族：node 侧守卫漏配）。

## D3. 本轮采证发现

### P6（产品 FAIL）：hub 面异步取消对执行中轮不生效（未触发进程终止，只能等自然终态后先到保留）
样本（hub 套件 S13b，10s 夹具）：轮执行中（T+3s）点取消 → 回执「取消任务已投递：结果随详情自动刷新收敛」（异步受理，诚实）→ 账本：取消任务 created_at=17:09:49.788、concluded=17:09:56.202（**6.4s 被动等待**，期间进程未被终止）→ 轮于 17:09:56 自然完成 → 记账 turn_already_finished（先到保留）→ 轮行自然终态 unverified。套件判据「接受取消 ⇒ 轮行必须呈现 cancelled/待核对」→ FAIL。二轮探针样本同现象（延迟 14-16s）；而 **node 本地面取消**即时终止进程（首轮 AC15 executed PASS）——缺陷限于 hub 下发取消路径：node 侧等了 6.4s 却未触发执行中轮的 ctx 终止（疑似 hub 下发轮的取消句柄在执行侧未绑定，退化为被动等待兜底）。回执链全程诚实、不虚报；但取消语义对 hub 面执行中轮落空。复现：任意实例，hub 建会话投长轮（>10s）后经 UI 取消。日志：node-r3b.log（s-cf94d334：01:09:56 三行同秒——取消 op、轮自然完成、先到保留拒绝）。

### P7（产品，双端形态漂移·轻微，建议修）：node 会话新建下拉直接省略无适配器能力，hub 为「禁选+注明」
node `sessAgentOptionsNode()` 仅列声明 session_adapter 的能力（echo-contrast 整项不出现，无「（未声明会话适配器）」禁选项）；hub 列全部能力并对无适配器者禁选+注明（hub app.js:3907）。套件 SN01 组件 c2 断言 node 侧同构禁选注明 → 永假 → SN01 FAIL（其余六组件全 PASS）。两种收敛路径待裁决：①node 补禁选+注明（与 hub 同构）；②SPEC 六.五白名单登记「node 本机单级目标只列可会话能力」+ 套件改断言。节点侧无适配器能力本就不可选，功能无损，故分级建议修。

### T5（采证工具缺陷）：node 套件 SN07 相位A 前缺回列表守卫
SN09 尾态在详情页（抽屉 Esc 关闭后仍驻详情），SN07 相位A 直点 `tr[data-sess=…]` → 15s 超时套件崩出，SN03/SN04/SN06/SN07/SN10 未执行。与 hub T1 同族（hub 侧 79a2fd3 已加守卫，node SN07 路径漏配）。

## D4. 第三轮总结论

**P5/T2/T3/T4 修复全部复验 PASS**（官方记录：hub 14 PASS 全量走完含 S13a/S06/S09；node SN08/SN08b 官方 PASS、SN01 六组件五真 + P5 专项 30s 抽验 15s 推进）。**套件全绿仍未达成**，余项三件：
1. **P6（产品）** hub 面执行中轮取消不生效（先到保留兜底诚实但语义落空）→ S13b FAIL；
2. **P7（产品漂移·建议修，或套件断言+白名单裁决）** node 新建下拉省略无适配器能力 → SN01 c2 FAIL；
3. **T5（工具）** node SN07 回列表守卫 → SN03/SN04/SN06/SN07/SN10 五条官方记录缺失。

前两轮各项结论维持：F1/F2/F3/T1 PASS 不变（本轮 S10/S12/S04/S05/S13a 官方再证）；AC03/AC15/AC04-zcode/AC19 结论不变；P4 维持登记；codex 维持 DEFERRED。

---

---

# 复验轮（2026-11-01，基线 79a2fd3）：FAIL 修复采证

> 独立采证会话第二次进场：对 79a2fd3（F1/F2/F3+T1+P4 登记）重采。程序同首轮（embed 强刷重建并核对 mtime、临时实例 hub 28443/28444 + node 28545、同一 fake CLI 脚本、全新 data_dir；证据追加于 /tmp/stagea-evidence/ 下 *-r2 命名与 shots-r2/）。采证结束进程已全部终止、仓库无源码/测试改动。

## R1. 修复项复验结论

| 修复 | 复验方式 | 结论 |
|---|---|---|
| F1 双端列表 1024 横向溢出 | hub 套件 S10 官方实跑 + node SN05 实质探针（五视口） | **PASS**。hub S10 官方 PASS（五视口 图高=文档高且无宽告警）；node 五视口 fullPage PNG 宽=1440/1280/1024/680/390 全部等于视口宽（shots-r2/node-sessions-*.png），scrollWidth==clientWidth 全 OK |
| F2 抽屉 Esc 返焦 | hub 套件 S12 官方实跑 + 双端精确探针（未选中行首开→Esc） | **PASS**。hub S12 官方 PASS（返焦详情页=true）；探针 hub/node 双端：`rowSelectedBeforeOpen=false` 首开抽屉→Esc→`inDetail=true`（活动元素=#sess-detail SECTION，tabindex=-1 宿主），抽屉关闭、焦点闭环 |
| F3 node modal form 恢复 | 官方套件走到 SN08 能力探针 + 结构探针 | **阻断解除 PASS**。`#sess-modal #sess-form` 在场、主按钮原生 type=submit、form 提交委托生效（官方 run 的 SN01 create POST 已实际发出并被套件捕获）；SN01 起不再因选择器阻断 |
| T1 e2e S04 回列表守卫 | hub 套件实跑 | **PASS（S04/S05 恢复可执行且 PASS）**——旧崩溃点消除；但套件在 S13a 后仍有新的中断（见 R2/T3） |
| P4 登记路线 | 未重试（控制台 UI 路径不受影响；服务端受理登记属后续批次） | 维持登记 |

## R2. 本轮套件实跑结果

**hub 模式**（evidence/e2e-hub-sessions-r2.log）：S01/S02/S02b/S03/S11/**S10**/S07/**S12**/S08/**S04**/**S05** 共 11 PASS；**S13a FAIL（采证环境竞态，非产品缺陷——见 R3.1）**；S13b/S06/S09 因 S13a 后套件崩溃未执行（T3）。上一轮 FAIL 的 S10/S12 本轮官方 PASS。

**node 模式**（evidence/e2e-node-sessions-r2.log）：SN08a PASS；SN08 FAIL（表头读取竞态，走查显示其余要件全真：迁移卡/201 登记/pending 徽标/详情登记态全=true，仅 `thead th` 在数据未落定时刻读空——T4）；SN08b FAIL（300ms 等待内筛选重绘未落定——T4；实质探针以正确等待复验 **PASS**）；SN01 起被 **T2**（`postDataJSON().catch is not a function`——Playwright 1.64 该 API 同步返回，套件误用为 Promise）中断，SN01-SN10a/SN05 官方记录未产出。

**SN05（F1 node 侧）与 SN09（F2 node 侧）、SN08b、S13 同族语义由实质探针补验**（非冒认套件记录，逐条列明）：
- SN05 实质 PASS：五视口 PNG 宽==视口宽 + scrollWidth==clientWidth（R1 表）。
- SN09 实质 PASS：F2-node 探针（R1 表）。
- SN08b 实质 PASS：等「登记中」行入列后再点 pending chip → chip 选中+过滤仅剩该行+再点复位回全部（probe-r2.json NODE-R2B 段）。
- SN01 实质：登记徽标/受理回执/详情 unverified 均 PASS，**但「发布推进」被 P5 阻断**（见 R3.2）——SN01 的自动化判定在 P5 修复前无法达成。
- S13 同族（node 侧）：close_in_flight REST 抽验 PASS——在途轮存在时 close 409「存在非终态受理轮，拒绝关闭…（先取消或等待收束）」，执行取消后 close 200（evidence/r2-node-close-inflight.txt）。

## R3. 采证发现（复验轮）

### R3.1 S13a 官方 FAIL = 快 CLI 竞态，产品行为经探针证实正确
现象：套件 REST 投轮后 300ms 开确认层再点确认，假 CLI 轮（约 0.4s 完成）已先终态 → close 到达时无在途轮 → 服务端正确放行关闭（toast「会话已关闭」），套件期望 close_in_flight 拒绝 → 判 FAIL；随后套件等「再想想」按钮超时崩出，S13b/S06/S09 未执行。证据：dump 中轮完成 15:53:51、关闭 15:53:52（关闭晚于完成=放行正确）。
产品实质探针（probe-r2.json HUB-R2 段）：以 SLEEP=25 长跑保证在途，UI 关闭 → toast「会话仍有在途轮次，已拒绝关闭——请先取消或等待在途轮次完成」、头部未关闭 = **PASS**；随后取消入口/确认 → 回执「取消任务已投递」→ 本样本取消晚到（先到保留，node 日志 turn_already_finished）→ 轮行保持自然终态 unverified——与 W01-02 §3 矩阵「已自然终态含迟到取消=先到保留不改写」一致，回执链诚实；取消获胜路径（executed）与排队取消（queued）已由首轮 AC15 采证 PASS。S06 关闭确认实质 PASS（slim modal 深层态+头部/列表「已关闭」徽标推进）；S09 字号地板实质 PASS（违例=[]、pulse-dot、reduce=none）。

### R3.2 P5（新发现，既有缺陷——旧二进制 7000e9a 同样复现，非 79a2fd3 引入）
node 控制台从「新建会话」modal 提交后，会话列表轮询数据链断裂：页面 GET /api/v1/sessions 计数冻结（提交后仅 refresh 一次），chips 与列表行全部冻结；首轮已完成、后端列表端点已返回已发布会话（pending 标志消失、turns=1），但列表行「登记中（首轮未发布）」滞留 ≥30s 不推进（实测至探针窗口结束）；Esc 关 modal 不恢复；手工刷新或应用内路由切换后恢复。机制证据：任务页出现「任务列表暂时读不到最新数据」陈旧条（S.loadError 常置=pollTick 外层 catch 每轮吞掉异常，loadSessions 尾部不再执行；loadAll 四资源请求每 5s 照常、响应形态校验项均合格——抛错点在会话分支绘制段，具体表达式待修复方一行插桩定位）。归因：git archive 7000e9a 构建旧二进制（独立 28546 端口）同流程复现 → 既有缺陷。影响：SN01「发布推进」自动化判定被阻断；主人视角新建会话后列表不自动推进（需手动刷新）。
证据：probe-r2.json NODE-R2B/NODE-DIAG 段、probe-r2-final/probe-r2-stuck/probe-r2-throw3/probe-r2-loaderr.mjs 输出（/tmp/stagea-run/）、node-r2.log（s-61cab016：轮完成 00:09:59 而行滞留至 00:10:28+）。

### R3.3 采证工具缺陷（复验轮新增，与产品缺陷分列）
- **T2** tests/e2e-console.mjs SN01：`createResp.request().postDataJSON().catch(...)` ——Playwright 1.64 的 `Request.postDataJSON()` 同步返回值、无 `.catch`，TypeError 套件崩出，SN01-SN10a/SN05 官方记录未产出（官方 run 的 create/turn 请求实际已成功发出）。
- **T3** hub 套件 S13a 后：对快 CLI（轮先于关闭完成）S13a 判 FAIL 后套件未把「再想想」摘层做成条件守卫，`#layer .modal [data-act="close"]` 15s 超时崩出，S13b/S06/S09 未执行（本轮）。
- **T4** node 套件 SN08/SN08b 的固定短等待（读表头未等数据落定；300ms 等筛选重绘）对慢加载实例竞态——实质探针以条件等待复验均 PASS。

## R4. 复验总结论

**F1/F2/F3/T1 修复本身全部复验 PASS**（官方 S10/S12/S04/S05 + 双端 F2 探针 + SN05/SN08b/SN09/S13/S06/S09 实质探针）。**「E2E sessions 套件全绿」尚未达成**，余项三件：
1. **P5（产品，既有）** node 会话页 modal 提交后列表轮询冻结——修后 SN01「发布推进」方可自动化判定；
2. **T2（工具）** SN01 postDataJSON 误用修复后，node 套件方可走完 SN01-SN10a；
3. **T3/T4（工具）** hub S13a 快 CLI 竞态守卫 + node SN08/SN08b 条件等待，修后 S13b/S06/S09 与 SN08/SN08b 方可产出稳定官方记录。

P4 维持登记（服务端批次）。上轮 FAIL 三条（S10/S12/套件阻断 P3）与 T1 的复验结论如 R1 表：全部 PASS。

---

> 采证会话：独立会话执行（测试循环制度②，不共享开发上下文，只采证不修码）。基线 feat/product-v3 @ 7000e9a3（工作树采证前后均干净，无任何源码/测试改动）。
> 实例与证据留存：`/tmp/stagea-evidence/`（本报告引用的全部路径以此为根）。采证结束后自启进程已全部终止（见 §9）。
> 采证工具：真实浏览器（Playwright 1.64.1，仓库自带 node_modules）+ 真实 REST + 真实 CLI（codex 0.164.0 / zcode 0.17.0 runtime，均本机已登录，用户已授权）。

## 0. 总结论：**非全绿 —— 产品 FAIL 3 条、建议修 1 条、DEFERRED 1 项、采证工具缺陷 1 条**

| 项 | 结论 |
|---|---|
| E2E sessions 套件 hub 模式（S01-S13） | 8 PASS / 2 FAIL（S10、S12）/ 5 未执行（套件自身缺陷中断，见 §3.1） |
| E2E sessions 套件 node 模式（SN01-SN10a/SN08b） | 1 PASS（SN08a）/ 其余未执行（被产品缺陷 P3 阻断，见 §2.3） |
| AC01/AC02（fake CLI 首轮/续轮/同线程续接，经套件+冒烟） | PASS |
| AC03 产品级重启恢复 | PASS |
| AC04 真实 CLI | zcode PASS（4 轮全链）；codex DEFERRED（凭据 403，环境原因如实登记） |
| AC15 取消矩阵抽样 | PASS（executed/queued/幂等重放/409 先到保留 + UI 徽标核对）+ 可见性发现 1 条 |
| AC19 双端同组件成对截图 | 已拍齐四组并逐对目视核对；其中暴露 P3（表单结构不同构） |

---

## 1. 实例搭建记录（全程临时目录/临时端口，未碰生产数据）

### 1.1 构建与 embed 强刷核对

- `go build -o /tmp/stagea-bin/hub ./cmd/hub && go build -o /tmp/stagea-bin/node ./cmd/node`（`--version` = dev）。
- 强刷核对：`find internal/hub/web internal/node/web -type f -newer /tmp/stagea-bin/hub` 无输出（无任何 web 资产晚于二进制 mtime，实测对象=最新资产构建）。

### 1.2 端口与进程（关键字段全记录）

| 项 | 值 |
|---|---|
| 说明 | 计划端口 18443/18444/18445 被一个**既有 dev 实例**（进程 dev-hub-n PID 54568，非本会话启动）占用，改用下述端口；该实例全程未受影响 |
| hub 启动 | `nohup /tmp/stagea-bin/hub -config /tmp/stagea-run/hub.yaml`，PID 11515 |
| hub 端口 | node 接入 ws://127.0.0.1:28443/ws/node；管理面 http://127.0.0.1:28444（insecure_loopback） |
| hub 凭据 | admin_token=`stagea-admin-token-2026`；nodes 段 `stagea-node: stagea-node-reg-token`；管理台账户 admin/Swarm@2026 由 hub 套件首跑 setup 表单创建 |
| node 启动 | `nohup /tmp/stagea-bin/node -config /tmp/stagea-run/node.yaml`，首启 PID 11522；全程共重启 3 次（AC03 重启恢复 1 次、AC04 增配 2 次），末次 PID 15871 |
| node 端口/令牌 | local API 127.0.0.1:28545；local_token=`stagea-local-token-2026` |
| data_dir | /tmp/stagea-run/hub-data、/tmp/stagea-run/node-data（与生产隔离） |
| 关键日志 | hub：「HTTP 管理面已启动 127.0.0.1:28444」「node 会话入口已启动 127.0.0.1:28443」；node：「本地 API 已启动 127.0.0.1:28545」「注册成功 node=stagea-node epoch=1」（日志全文：evidence/hub.log、evidence/node.log） |
| fake CLI | `/tmp/stagea-run/fakecli.sh`（evidence/instance/fakecli.sh）：镜像 zcode headless 协议（adapter_zcode.go 形态）；首轮签发持久原生 ID，续轮核验 resume ID（非本脚本签发即 `no such session` exit 5）、回显 `history:<累计轮数>` 证明同线程续接；prompt 内嵌 `SLEEP=<sec>` 触发长跑（AC15 用） |
| 能力注册 | fakecli-zcode（session_adapter: zcode，perm edit）+ echo-contrast（无适配器对照，供 S03 禁选分类）；AC04 轮次增配 codex-real/zcode-real（见 §6） |

### 1.3 冒烟（套件前自检）

本机主人 REST 建 fakecli-zcode 会话 → 首轮 native=`fakesess-1793457507-1-11554` history:1 → 续轮同 native ID、回显 `history:2 resume:fakesess-…`。证据：evidence/smoke1.json、smoke1-detail.json、smoke-detail-final.json。

## 2. E2E sessions 套件实跑（AC01/AC02 部分/AC19 证据）

### 2.1 hub 模式（`E2E_SUITE=sessions E2E_BASE=http://127.0.0.1:28444`）

结果行原文见 evidence/e2e-hub-sessions.log。种子绑定 A=s-acd0041a…（无轮次）B=s-889d9a10…（真实轮→unverified）C=s-5fd6e311…（坏 cwd→failed）。

| 用例 | 结果 |
|---|---|
| S01 列表七列同构/待首轮徽标/无标题缺省/tabindex | PASS |
| S02 chips 计数与 zero 色态逐一匹配 API（含 cancelled） | PASS |
| S02b 零计数 chip 筛选空态→清除恢复 | PASS |
| S03 新建 modal 联动（按 session_adapter 分类禁选并注明/适配器只读/权限档剔除全权/空消息拦截） | PASS（禁选对照=echo-contrast 1/2） |
| S07 详情页四分区/实键 Enter/unverified 警示条绝不渲染成功/返回/chip 同步 | PASS |
| S08 失败轮次覆盖（坏 cwd→failed 徽标+错误条） | PASS |
| S11 新建 modal 居中形态+草稿保护（inert/焦点入层/Esc 关层重开保留） | PASS |
| S10 五视口整页截图（像素判据） | **FAIL**（产品缺陷 P1） |
| S12 执行明细抽屉深层态（焦点陷阱过、Esc 关层过、**返焦失败**） | **FAIL**（产品缺陷 P2） |
| S04/S05/S13a/S13b/S06/S09 | 未执行——套件自身导航缺陷中断（工具缺陷 T1，§3.1） |

### 2.2 node 模式（`E2E_SUITE=sessions E2E_BASE=http://127.0.0.1:28545 local_token=…`）

| 用例 | 结果 |
|---|---|
| SN08a 导航词表 5 项含「会话」 | PASS |
| SN08/SN08b/SN01-SN07/SN09/SN10 | 未执行——`waitForSelector('#sess-modal #sess-form')` 5s 超时崩出（产品缺陷 P3，§2.3） |

P3 阻断点旁的产品实质核验（独立探针绕开 `#sess-form` 选择器，evidence/probe-s12-s10.json NODE-MODAL-FLOW 段）：node 新建 modal 字段齐备（标题/目标智能体/适配器只读/权限只读/工作目录/首轮消息/提交钮），UI 提交 → agent 面 201 登记（pending=true）→ 首轮受理执行 → 详情页轮行 sst-unverified。**功能在，缺的是 `<form id="sess-form">` 包裹元素**。

### 2.3 采证发现（产品）

- **P1（必须修）hub/node 会话列表页 1024 视口横向溢出**：fullPage 截图宽 1073px（hub）/1088px（node）> 视口 1024，元凶为会话表格（含 table-action 列 134px）无横向约束/滚动容器，撑破文档（探针 overflow1024：`scrollWidth 1073 / clientWidth 1024`，贡献者 TABLE/THEAD/TD.table-action，evidence/probe-s12-s10.json）。E2E 判据「宽=视口宽」双端同判 FAIL（hub 套件 S10 直接判败；node 套件因 P3 未跑到 SN05，属同源同病）。复现：`E2E_SUITE=sessions E2E_BASE=http://127.0.0.1:28444 node tests/e2e-console.mjs`。期望=各视口图宽==视口宽；实际=1024 视口下 1073/1088。
- **P2（必须修）hub 执行明细抽屉 Esc 返焦落点错误**：从**未选中行**首次点「明细」开抽屉时，`syncSessPageZones()` 行选中态重绘替换了时间线分区 DOM，触发按钮节点失连；Esc 后 `restoreLayerFocus()`（hub app.js:2181，兜底=侧栏当前页签按钮，注释自认「失存回落侧栏」）把焦点落到「04会话」导航按钮而非详情页。精确探针（evidence/probe-s12-s10.json PRECISE 段）：行未选中态 phase1 `btnReplacedAfterOpen=true → Esc 后 inDetail=false`；行已选中态 phase2 无重绘 `btnReplaced=false → inDetail=true`（返焦正常）——即标准用户路径（首次开抽屉）必现。期望=Esc 返焦 #sess-detail（套件判据）；实际=焦点在侧栏导航按钮。
- **P3（必须修）node 新建会话 modal 缺 `<form id="sess-form">`，双端表单结构不同构且阻断 node 套件**：hub modal 为 `<form id="sess-form">`（hub app.js:4187），node modal 为裸 fieldset（node app.js 全文 0 处 sess-form；7bd09a3 重写时丢失，同批提交自注「本批未运行待独立采证」）。E2E node 套件按 `#sess-modal #sess-form` 断言（tests/e2e-console.mjs:757/829）→ 5s 超时整套崩出，SN01-SN10 全部无法执行。违反双端一致硬规②（操作逻辑层同构）。复现：`E2E_SUITE=sessions E2E_BASE=http://127.0.0.1:28545 local_token=<token> node tests/e2e-console.mjs`。期望=套件全链可跑；实际=SN08 起阻断。
- **P4（建议修）node 本机会话首轮被取消后会话从双面消失**：会话 s-0bfb56fb…（首轮执行中取消）取消回执诚实（executed/turn_cancelled_executed）、任务账本留痕 status=cancelled（idem_key `lsturn:s-0bfb…:1`），但内核映射因首轮未完成未发布、受理登记已消费 → 主人面/agent 面列表与详情均 404「会话不存在（本机内核无映射且无受理登记）」。主人视角该会话「蒸发」，只能到通用任务列表翻账本。无伪成功（不计 FAIL，建议修可见性）。

## 3. 采证工具缺陷（与产品缺陷分列）

- **T1 hub sessions 套件 S08→S04 导航断裂（确定性崩溃）**：S08 结束停在 seedC 详情页（`page.goto(#/sessions/seedC)`），S04 首步直接 `page.click('tr[data-sess=seedA]')`——详情页无列表行 → 15s 超时，整套异常终止，S04/S05/S13a/S13b/S06/S09 五条未执行。复现命令同 §2.1（任何环境必现；S08 首句 `page.click(tr[data-sess=seedC])` 带 catch 吞掉的正是同一前提错误——点击时已在 seedB 详情页）。期望=S04 前回列表；实际=直接点列表行。**该五条的验收判定本采证不提供**（不冒认）。
- T2（自用脚本，非仓库资产）：ac04-zcode.sh 的任务账本轮询字段名不匹配致 `task=poll-timeout`（会话详情轮询为准，结论不受影响），如实注明。

## 4. AC03 产品级重启恢复 —— PASS

流程与证据（evidence/ac03/01-05）：
1. 主人 REST 建 fakecli-zcode 会话（cwd=/tmp/stagea-run/work），首轮受理 202：`s-2d8dac21… / task t-10705405…`；完成 native=`fakesess-1793458179-4-12336`，回复 `history:1`。
2. `kill -TERM` node（优雅退出，日志无异常）→ 同 data_dir 重启（epoch=2）。
3. 详情查询：native 不变、turn1 ok（03-detail-after-restart.json）。
4. 续第二轮 202 → `history:2 resume:fakesess-1793458179-4-12336`——同一原生 ID、对话衔接（假脚本历史计数）、全程无「不可恢复」形态。断言脚本输出 `AC03-ASSERT … PASS`。

## 5. AC15 取消矩阵抽样 —— PASS（附 P4）

会话 s-1d8b90b1…（首轮正常完成后逐轮采样；回执 JSON 见 evidence/ac15/）：

| 采样 | 回执 | 轮行/呈现 |
|---|---|---|
| 执行中取消 seq2（SLEEP=30 长跑，+4s 取消） | `outcome=executed, error=turn_cancelled_executed`（02→12） | UI 轮行徽标 sst-cancelled「已取消」，答复区「已取消（执行已终止）· 进程组已整组回收」（截图 shots/ac15-cancelled-rows-node.png、ac15-turn2-detail-node.png）——**绝不渲染为成功** |
| 排队取消 seq4（seq3 SLEEP=25 执行中时提交并立即取消） | `outcome=queued, error=turn_cancelled_queued`（16） | 轮行「已取消」无时间戳（未运行确定性，#4 行 `— —`）；seq3 不受影响自然完成 unverified |
| 已取消终态重复取消 seq2 | 幂等重放：原回执 executed 原样返回 HTTP 200（17） | 与 RFC §3「返回原回执」一致 |
| 自然终态取消 seq1 | HTTP 409 `turn_already_finished`「先到保留：终态 completed/，已完成不可取消」（18） | 一致 |
| REST 内核快照口径 | 执行取消轮快照行 status=failed/exit=-1/error=timeout（13），取消分类在账本与 UI 合层呈现 | 判定为设计内双路记账（内核证据保留+账本分类），UI 呈现诚实 |

首取消场景（首轮执行中取消，01-03）另得 P4。

## 6. AC04 真实 CLI —— zcode PASS / codex DEFERRED

探测：`which codex zcode` → /opt/homebrew/bin/{codex,zcode}；codex-cli 0.164.0（`codex login status`=Logged in using ChatGPT）；zcode 0.17.0 runtime（`zcode --version`=3.16.3-41 包装，headless 实测可用）。平台接线：node.yaml 增 codex-real（session_adapter: codex，permission_mode: **read-only** 不提权）与 zcode-real（session_adapter: zcode，permission_mode: **edit** 不提权），重启后 epoch=3 注册 agents=4（node 日志）。

### 6.1 codex —— DEFERRED（环境凭据原因，不伪造）

- 平台首轮（evidence/ac04/codex-01-create-r1.json）：202 受理后 180s 超时整组终止（node 日志 22:59:48 timeout），轮行如实 failed。
- 绕开平台直跑复现两轮（同干净 env、同完整用户 env）：thread.started 后反复 `Reconnecting… (workspace routing discovery timed out)`，stderr 反复 `codex_login::auth::manager: Failed to refresh token: 403 Forbidden`，120s/90s 均未完成。**完整用户环境同样失败 → 与仓库产品无关，系该机 codex ChatGPT 凭据刷新被服务端拒绝**。
- 两轮对话/受限工具用例无法进行 → 如实 DEFERRED。证据：ac04/codex-README-DEFERRED.txt + codex-direct2-*.log、codex-fullenv-*.log。

### 6.2 zcode —— PASS（4 轮平台全链；evidence/ac04/zcode-01~08）

环境注记：zcode 的已存模型选择解析依赖真实用户家目录（构建的镜像 HOME 直跑报 `Select a model before sending a message`，逐层镜像 .zcode/Application Support 均无效），按产品语义把 zcode-real 的 config_home 配置为 `/Users/tl.m`（= 主人本机直接运行 zcode 的等价环境；工作区仍在 /tmp 采证目录，权限档位 edit 不变）。

| 轮 | 请求 | 结果 |
|---|---|---|
| 1（create+首轮） | 约定暗号 ZCODE-PACT-77 | native=`sess_9eb284cb-d2d2-44a9-b208-4d334233b166`，回复「已记住。…」 |
| 2（续轮） | 暗号是什么 | **同 native ID** 续接，回复 `ZCODE-PACT-77` —— **MEMORY-ASSERT PASS** |
| 3（受限操作①） | 用 Bash 执行 `ls /` | edit 档位下该 CLI 实际放行了只读 Bash（现版本行为，与 2026-10-29 探针结论不同，如实记录）；平台侧 perm=edit 恒定、work_result=unverified，无提权无冒认 |
| 4（受限操作②） | 向工作区外 /tmp/stagea-run/ac04/forbidden-zone/ 写文件 | CLI 正文拒绝（「这个我不做。该请求要求在我的工作区之外…」），**目录实测为空未落盘**；平台不升级权限、work_result 恒 unverified、不冒认成功 |

记录：适配器=zcode（平台回执 adapter 字段）；权限模式=edit（会话绑定快照）；模型引用=平台/适配器输出均未暴露模型字段，如实体检（环境 provider_config 默认选型 GLM-5.3-Flash，仅作环境注记非平台证据）；耗时 turn1 66s / turn2 5s / turn3 8s / turn4 38s（详情 JSON started/finished）。

**额度消耗（如实）**：zcode 平台 4 轮（usage 汇总 in 217,680 / out 3,707 / total 221,387 tokens）+ 采证直连探针 2 轮（登录自检、干净 env 对照）；另约 6 次报错轮（`Select a model` 类，按 CLI 报错语义未发出模型请求，消耗推定为零至少量，无法核实）。codex：平台 1 轮（180s 超时终止，完成事件未收到）+ 直连 2 次未完成（手动终止），实际消耗未知。

## 7. AC19 双端同组件成对截图 —— 已拍齐，逐对核对

四组成对（evidence/shots/）：

| 组件 | hub | node | 目视核对结论 |
|---|---|---|---|
| 会话列表（五视口 1440/1280/1024/680/390） | hub-sessions-{WxH}.png ×5 | node-sessions-{WxH}.png ×5 | 页面骨架同构：标题+副题、右上「新建会话」、搜索框、chips 行、七列同表（标题/agent/机器/工作区/状态/最近活动（UTC）/操作）、页脚「N 个会话 · 时间为 UTC…」；差异=导航词表 9/5 项、node 多「登记中（首轮未发布）」chip、标题列 node 独有——均属 SPEC 六.五白名单。**1024 视口双端横向溢出（P1）** |
| 详情页 | hub-detail-timeline.png | node-detail-timeline.png | 四/五分区同构（head/binding/turns/detail/actions）、unverified 警示条同文、返回列表链接一致 |
| 执行明细右抽屉 | hub-turn-detail-drawer.png | node-turn-detail-drawer.png | 右抽屉三分区 head/facts/answer 同构；「受理与执行事实」「第 N 轮答复」+ unverified 警示条同文；facts 字段差=端点能力差（hub 有关联任务/幂等指纹，node 为原生轮次/退出码），白名单内 |
| 新建会话居中 modal | hub-new-session-modal-retake.png（E2E 原图 hub-new-session-modal.png 因 fullPage 定位元素未入图，重拍补证） | node-new-session-modal-noformid.png | 居中 modal、字段序目标→绑定→工作区→输入同构（node 前置标题=白名单；hub 两级机器+agent 选择、node 单级本机目标=端点差异）；底部 request_id 幂等说明同文。**node 缺 form#sess-form 包裹（P3）** |

另：ac15-cancelled-rows-node.png / ac15-turn2-detail-node.png 为取消三态轮行留证。

## 8. FAIL/建议/DEFERRED 清单汇总

必须修（产品）：
1. **P1** 会话列表页 1024 视口横向溢出（双端同源；套件 S10 判据直判 FAIL）——现象/复现/期望见 §2.3。
2. **P2** hub 执行明细抽屉 Esc 返焦落点错误（首开必现，兜底落侧栏；套件 S12 判据直判 FAIL）——机制探针 §2.3。
3. **P3** node 新建会话 modal 缺 `<form id="sess-form">`，双端表单结构漂移且 node E2E 套件 SN01-SN10 全链阻断——§2.2/§2.3。

建议修（产品）：
4. **P4** node 本机会话首轮被取消后会话双面 404 蒸发（回执/账本诚实，无伪成功，仅可见性缺口）——§2.3。

DEFERRED（如实，不伪造）：
5. AC04 codex 两轮对话+受限工具（该机 codex 凭据 refresh 403，绕平台复现同样失败）——§6.1。

采证工具（非产品）：
6. **T1** hub sessions 套件 S08→S04 导航断裂致 S04/S05/S13a/S13b/S06/S09 未执行（五条判定缺失，本采证不补测不冒认）——§3.1。

## 9. 清场核对

- 自启进程：hub PID 11515、node 末次 PID 15871 均 SIGTERM 退出；`ps` 无 stagea-bin/fakecli 残留；28443/28444/28545 无监听。既有 dev 实例（PID 54568，18443/18444）非本会话启动，未触碰。
- 仓库：`git status --porcelain` 采证前后均为空（本报告为唯一新增文件）；无 commit/push。
- 临时留存：/tmp/stagea-bin/（二进制）、/tmp/stagea-run/（实例配置与数据）、/tmp/stagea-evidence/（全部证据，供追溯）。
