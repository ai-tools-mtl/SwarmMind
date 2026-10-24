# REVIEW: 跨机取回（P4④第二子轮）codex 审查意见与裁决

> 审查：codex，2026-10-24 22:38 完成（只读，119k tokens，实跑 vet/-race/windows build/diff-check 全过）。
> 裁决（Hana）：**必须修 4 条 + 建议修 2 条全部成立，全部回流修复**。建议修不降级理由：第 5 条鉴权/脱敏职责分离是本项目一贯红线（第一子轮同源标准）；第 6 条顺手收束。已核实项（挂载事务复用/失败分类/Local 零回归）无需动作。

## 必须修

1. **mid 冲突覆盖在途 waiter**（internal/hub/core/artifact.go:256）
   `fetches` 同时容纳 HTTP 与 node 请求却直接以 node 提供的 MessageID 为键，无冲突检查。同 mid 第二请求覆盖第一 waiter：旧请求应答可能被新 waiter 消费，旧请求超时回调因 `cur != w` 不再通知原请求方。源 node 会看到中继 mid，不能靠随机编号回避。
   → 修法：hub 分配中继编号，表内保存原 requester/mid，回传时恢复关联。

2. **来源校验之前拆表停表**（internal/hub/core/artifact.go:190、287）
   任意匹配 ReplyTo 的帧先删 waiter、停超时，随后才查 env.From；来源不符直接 return，合法应答与 hub 超时通知均丢失，只剩下游准备超时兜底。
   → 修法：锁内确认预期来源后再消费 waiter；补「伪应答之后合法应答仍成功」测试。

3. **Remote 源读取未遵守「不跟随符号链接、仅常规文件」契约**（internal/node/service/remotefetch.go:69 → internal/node/service/artifact.go:109）
   Remote 路径复用既有 readArtifactFile：字符串路径检查后直接 os.Open，无 os.Root、逐段 Lstat、fd 常规文件校验。Local 拒绝的符号链接 Remote 可能接受；链接指向阻塞对象可卡死源 node 串行帧处理，hub 超时无法中断读取。底层函数虽是既有代码，但本轮将其升级为自动挂载源，两路径安全语义必须一致。
   → 修法：统一源读取校验（对齐第一子轮 249941b 的 os.Root 定界+逐段 Lstat+fd 校验），覆盖中间目录链接/末段链接/特殊文件测试。

4. **Remote 超限额测试缺失**（internal/node/inputs/remote_test.go:215）
   已覆盖离线/超时/404/SHA 不符，缺「>8 件」「登记超 640KiB」「实际返回大小不符」用例。共享 Prepare 的 Local 限额测试不能证明 Remote 分支。
   → 修法：补三条用例，断言永久失败、无发布、无暂存残留，登记已超限时不调用 Fetch。

## 建议修（本轮一并修）

5. **鉴权前的错误响应差异泄漏**（internal/hub/core/artifact.go:225）
   未授权 node 可经 task_not_found/artifact_not_found/forbidden 差异判断任务存在性并探测产物下标。
   → 修法：存在任务先鉴权再查结果与下标；无权限场景统一响应；补探测无效化测试。

6. **Core.Close 未收束 relay timer**（internal/hub/core/core.go:143）
   Close 只停 sweep；存活 relay timer 继续持有 Core/waiter，可能在关闭后调 transport（有限延迟残留，约 30s）。
   → 修法：关闭时停止 timer、清表。

## 已核实通过（codex 确认，无需动作）

- 挂载事务：Remote 仅替换内容源，复制/落盘 SHA/journal 发布回滚全复用（inputs.go:631）
- 失败语义：离线/fetch_timeout 可重试，404/forbidden/mismatch 永久失败，符合 SPEC 4.12（remotefetch.go:101）
- Local 命中优先、配置漂移不转 Remote，零回归（remote.go:232）
- DoD 1/2/4/5 实测过：双 node 逐字节一致、离线恢复与穷尽失败双路径、-race+windows 编译、SPEC 同步

## 修复要求

修复完成后：全量 -race 绿 + GOOS=windows 编译过 + vet 干净 + 新增测试全部包含。输出改动文件清单与逐条闭合说明。

---

# 第二轮：codex 复审意见（2026-10-24 23:35，判定暂不可验收）

> 复审核实：原 1/2/4/5 已闭合（mid 冲突 hub 编号彻底、伪应答锁内预检有效、限额测试真过 Remote 分支、统一 forbidden 保留已鉴权精确错误）。无锁顺序反转、无新增死锁，artClosed 可见性成立。以下为残余与新发现。

## 必须修

R1. **源读取仍可能阻塞，未彻底实现「不跟随符号链接」**（internal/node/service/artifact.go:136）
   末段 Lstat 只拒链接后直接 r.Open(rel)：登记文件被换为无写端 FIFO 则 Open 阻塞，fd 常规文件检查无法执行，hub 超时不能解除源 node 阻塞。Lstat 与 Open 之间仍可换入根内链接（os.Root 防越界但允许根内链接，fstat 无法判断是否经链接）；中间目录同样有替换窗口。Local 侧 inputs.go:701 同类缺口，复制其流程不能证明契约成立。artifact_source_test.go:111 的「特殊文件」测试实际仅用目录未覆盖 FIFO。
   → 修法：统一采用能避免阻塞、在打开时落实不跟随约束的源读取方式（方案由实现者按 POSIX 语义选定并论证），Local/Remote 同步；补 FIFO/中间目录链接/末段 TOCTOU 窗口测试。

## 建议修（按原裁决一并闭合）

R2. **Close 与登记、已启动回调之间竞态**（artifact.go:239、core.go:156）
   交错窗口：请求读 artClosed=false → Close 置位清表返回 → 请求继续建 timer/登记 waiter/Send，关闭后新 waiter 未收束；timer 回调已拆表读 shutting=false 后 Close 才返回，回调才 Send。timer.Stop() 不等已启动回调；应答拆表后锁外转发有同类窗口。现 Close 测试仅串行。
   → 修法：协调登记与在途发送生命周期（世代号或关闭后登记拒绝+已启动回调统一收敛点），不得持 muArt 调 Send（membus 同步回调重入）；补并发 Close vs 新请求/timer 回调测试。

R3. **timer 先于 waiter 登记启动，短超时可丢超时兑底**（artifact.go:266）
   短超时或调度暂停下回调先执行，表中无 waiter 退出；后登记的 waiter 无有效超时。
   → 修法：timer 初始化、登记、关闭复核组织为同一锁保护发布过程。

## 第二轮修复要求

同首轮：全量 -race 绿 + windows 编译 + vet 干净 + 新增测试包含并发场景。逐条闭合说明。

---

# 第三轮：codex 三审意见（2026-10-25 00:55）

> 三审核实：Unix R1 通过（openat 结构性成立）、Local/Remote 入口统一、R2 锁序契约、R3 先登后启全部闭合。残余问题集中在 **Windows 侧实现**（本轮新引入）+ 两条收尾。

## 必须修

W1. **Windows 逐段 Lstat 未累计前缀，功能回归**（sourceopen_windows.go:28）
   读 outputs/t1/sub/a.txt 实际检查的是根下 outputs、t1、sub（全是根直接子路径），第二步即失败——Windows 上 Local/Remote 源读取基本不可用。
   → 修法：累计前缀逐段 Lstat。

W2. **Windows 未落实「不跟随链接」，等价性论证不成立**（sourceopen_windows.go:37）
   无末段 Lstat 直接 OpenFile；中间段预检与打开有替换窗口；根内链接可被跟随且 fstat 看到的是常规目标，SHA 也拦不住。「Windows 无 POSIX FIFO」「创建链接需特权」均不构成安全前提。
   → 裁决方向（Hana）：以「打开时约束 reparse/link 解析」为目标修——逐段以 FILE_FLAG_OPEN_REPARSE_POINT 语义打开并验证组件形态（方案细节 zcode 按 Go/Windows 语义选定并论证，需查证 os.Root 在 Windows 对 reparse point 的实际行为后落笔），如实声明残余边界；不采纳仅补 Lstat 的方案。

## 建议修（一并闭合）

W3. 第三个外发点（应答已拆表、转发 Send 挂起时 Close）缺确定性并发测试；gate 模式现成，补一条；或收窄测试覆盖声明（remote_relay_close_test.go:138/187/268）。
W4. Close 注释承诺过宽（core.go:149）：关闭后拒绝回帧仍发、HTTP FetchArtifact 未纳入 artWG——收窄为「已受理 node relay 生命周期」。

## 第三轮修复要求

同前：全量 -race 绿 + windows 编译 + vet 干净 + W1/W2 的 Windows 形态测试（至少链接拒绝/前缀累计可验证；受限处如实声明）。逐条闭合说明。

---

# 第四轮：codex 四审意见（2026-10-25 01:50，收敛轮）

> 四审核实：W1/W2 三要求成立（检查即打开/句柄链前缀/reparse 拒绝）、W3 真覆盖第三外发点、W4 收窄准确、Unix 侧无回退。Windows 重写新引入一处必须修 + 两条测试/声明精确度。

## 必须修

V1. **根句柄永久泄漏**（sourceopen_windows.go:48）：根目录校验通过后无任何路径关闭 rootfd（66/75 行明确跳过）；成功、中间段失败、末段失败均泄漏一个句柄，裸句柄无 GC 回收且未共享删除权限会影响目录删除/重命名。
   → 修法：统一保证根句柄所有退出路径释放（含已返回文件关闭时的协调），避免重复关闭。

## 建议修（一并闭合）

V2. 挂起声明收窄（sourceopen_windows.go:24）：「目录命名空间内不存在 open 挂起对象类别」→「无本路径可达的命名管道/FIFO 型等待」（常规文件打开仍可能等 oplock break）。
V3. 测试精确度：sourceopen_windows_test.go:138 所谓「中间段符号链接」实际末段为 ldir 且目标为文件——应使用目录链接并打开其子文件；remote_relay_close_test.go:304 仅断言 ReplyTo 非空，应保存请求 mid 后精确比较恢复。

## 第四轮修复要求

同前全绿口径 + V1 泄漏路径说明（哪些退出路径如何释放）。逐条闭合说明。

---

# 第二轮修复闭合记录（2026-10-24，zcode）

> 全量 22 包 `go test ./... -race -count=1` 绿 + `GOOS=windows go build ./...` 过 + `go vet ./...` 干净。首轮 1/2/4/5 闭合代码未触碰（仅 Close 收束域内新增收敛组，见 R2）。

## R1（必须修）：源读取统一 OpenSourceFile，内核在 open 原语上强制不跟随+不阻塞

**方案论证**（按 POSIX 语义选型）：

1. **不能靠 os.Root + 旗标**：实测与源码核实（go1.24 `os/root_unix.go` `rootOpenFileNolog`），os.Root 的 Unix 实现本就恒带 `O_NOFOLLOW` 调 openat，但末段 ELOOP 时经 `checkSymlink` 读链接判根内性并**重试跟随**（根内链接跟随是 Root 的设计语义，`splitPathInRoot`/`doInRoot` 游走路径）——用户传入的 O_NOFOLLOW 被库逻辑架空，「打开时不跟随」无法经 Root 落实。本仓最初方案（`r.OpenFile(rel, O_RDONLY|O_NOFOLLOW|O_NONBLOCK)`）即因此被本仓单测 `TestOpenSourceFileKernelNoFollow` 当场证伪（链接被打开），遂废弃。
2. **不能靠标准库 syscall**：darwin 桩不导出 `syscall.Openat`（Darwin 走 libc，无 SYS_OPENAT 常量），linux 有而 darwin 无——跨开发/生产平台不可用。
3. **选定：逐段 openat 游走（`internal/node/inputs/sourceopen_unix.go`，经白名单 x/sys/unix，用途扩展已同步登记 go.mod 注释 + SPEC 三白名单行）**：根路径为配置管理路径（与 runner exec 同源可信，非 agent 可控），`unix.Open(root, O_DIRECTORY|O_CLOEXEC)` 锚定根 fd；中间段逐个 `openat(dirfd, seg, O_RDONLY|O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC)`——非目录/链接内核即拒，且每段解析锚定上一段已打开的目录 fd，**检查即打开，Lstat→Open 窗口在结构上不存在**；末段 `openat(dirfd, last, O_RDONLY|O_NOFOLLOW|O_NONBLOCK|O_CLOEXEC)`——链接 ELOOP（TOCTOU 窗口内换入同样被拒），FIFO 无写端 open 立即返回（O_NONBLOCK，POSIX：常规文件上该位无语义，无需清除）交由 fd fstat 常规文件校验拒绝；fd fstat 确认 regular（校验对象=读取对象）。fd 全程 O_CLOEXEC，openat 语义下根外路径不可达，`..`/绝对路径入口即拒（OpenSourceFile 守卫）。中间段在窗口内被换入根内链接的最坏残余：打开根内另一常规文件——内容过不了两路调用方共同下游的 SHA-256 对照登记终检，读不出登记外内容、不阻塞、不越界。
4. **Local/Remote 字面同源**：Local 挂载（inputs.stageOne）与 Remote 源读取（service.readArtifactFile）统一调用 `inputs.OpenSourceFile`（单一实现，平台分文件 sourceopen_unix.go / sourceopen_windows.go）；Windows 无 POSIX FIFO open 阻塞语义（命名管道经文件系统命名空间 open 在无服务端时立即失败）、无对应旗标，且符号链接/闸道创建需特权——保留 os.Root 定界 + 逐段 Lstat + fd fstat（原语义），残差由 fstat + SHA 终检兜底。
5. **连带**：原「逐段 Lstat 预检」在 Unix 路径废除（其静态形态拒绝职能由 openat 原语承担，文案保留同等信息：中间段/末段均注明拒绝跟随）；`stageUpstream` 不再预开 os.Root（根打开错误归入逐件错误映射，缺失根 → fs.ErrNotExist 口径不变）；既有用例 `TestPrepareSymlinkRejected` 断言从「非常规文件」更新为「拒绝跟随」（拒绝时点从 fstat 前移到 open，语义等价、强制更强）。

**新增测试**（`sourceopen_unix_test.go`、`artifact_source_unix_test.go`，unix 限定——FIFO/mkfifo 为 unix 语义）：
- `TestOpenSourceFileKernelNoFollow`：末段换入符号链接（等价 Lstat→Open 窗口被攻破的形态）经 openat 原语被拒，链接目标本身可打开证明拒自不跟随；
- `TestOpenSourceFileRejectsFIFO`：无写端 FIFO，断言 5s 内返回（防 open 挂起回归——旧病即 hub 超时解不开源 node 串行帧阻塞）且以非常规文件拒绝；
- `TestOpenSourceFileRejectsInRootIntermediateSymlink`：中间目录换入**根内**链接（os.Root 允许的形态）仍拒；
- `TestReadArtifactFileRejectsFIFO`：Remote 源读取路径（readArtifactFile）同一 FIFO 形态快速拒绝 + 防挂起护栏。

## R2（建议修）：Close 统一收敛点（artWG），登记关闭后拒绝

新 `Core.artWG`（sync.WaitGroup）+ 锁序契约：**凡外发（transport 调用）先在 muArt 临界区内做 `artClosed=false` 复核并在锁内 `artWG.Add(1)`，锁外 Send 完成后 Done；Close 置位 artClosed 后 Wait**。锁序保证：Add 与 Close 置位同锁序线性化——置位前的一切 Add 都 happens-before Wait；置位后的复核必见 closed、零新增 Add（符合 WaitGroup「计数为零时的 Add 必须先于 Wait」契约）。三个外发点全部收口：

1. `handleNodeFetch` 初始 `sendFetchRequest` 及其失败收尾回帧：登记临界区（见 R3）内 Add，defer Done；
2. `relayTimeoutFired`（超时回调）：锁内复核 `cur==w && !artClosed` 才 Add，锁外回 `err(fetch_timeout)`；
3. `handleFetchReply` 中继转发：拆表/停表/复核/Add 同临界区，`artClosed` 后到达的应答就地消化不再外发，已启动转发由 Close 等待。

登记生命周期：`handleNodeFetch` 的关闭复核→表登记→启动 timer→Add 在**同一 muArt 临界区**一气呵成——Close 清表与登记同锁序，「请求读 closed=false → Close 清表返回 → 请求才登记/Send」的交错不复存在（Close 返回后新请求在登记临界区即被拒，回 err(node_offline)，零登记零 timer）。锁内零 Send（membus 同步回调重入纪律全程遵守：所有 Send 均在 muArt 临界区外）。登记前的无状态拒绝回帧（invalid/forbidden/not_found/关闭 offline）不占收敛组：即发即弃，Close 后至多得 transport 已关的 ErrClosed（忽略）。已核实残留边界：Close 在登记与 Send 之间插入时，初始 Send 至多在已关 transport 上得 ErrClosed（忽略，有限一次）。

**新增测试**（`remote_relay_close_test.go`，gateTransport 可阻塞 Transport 挂起指定目标的 Send 以确定性复现「已过锁内复核、Send 未落 transport」窗口）：
- `TestCloseWaitsInFlightRelaySend`：初始 Send 挂起于闸门 → Close 阻塞（断言 150ms 未返回）→ 放行后 Close 才返回，且请求方零帧、表清空；
- `TestCloseWaitsStartedTimeoutCallback`：超时回调外发挂起于请求方闸门 → Close 阻塞 → 放行后 Close 返回且请求方恰收一帧 fetch_timeout；
- `TestCloseVsConcurrentRelayRequests`：4 goroutine 持续请求 vs Close 并发（-race）——关闭后所有回帧均为 err(node_offline)（零 data/timeout 帧），表恒空。

## R3（建议修）：先登后启，同临界区发布

timer 创建移入登记临界区、置于表登记**之后**（`c.fetches[relayMid] = w` → `time.AfterFunc` 同一 muArt 段内）：回调起手先取 muArt，与登记同锁序——回调可观察到的状态必然是「已登记」，短超时/调度暂停下回调先于登记执行在结构上不可能。旧实现（AfterFunc 先于登记启动，回调见表中无 waiter 即退出）对应的丢失形态由 `TestRelayTinyTimeoutStillFires`（RelayFetchTimeout=1ns + 黑洞源 node）回归锁定：极短超时仍恰回一帧 err(fetch_timeout)、表清空。

## 改动文件清单

- internal/node/inputs/sourceopen_unix.go（新）：Unix 源打开（逐段 openat 游走）
- internal/node/inputs/sourceopen_windows.go（新）：Windows 源打开（os.Root + Lstat + fstat）
- internal/node/inputs/inputs.go：openVerifiedFile → 导出 OpenSourceFile(root, rel)（入口守卫 + 常规文件收口）；stageUpstream/stageOne 适配（不再预开 os.Root）
- internal/node/service/artifact.go：readArtifactFile 统一经 inputs.OpenSourceFile
- internal/hub/core/core.go：artWG 字段 + Close 置位后 Wait + 注释
- internal/hub/core/artifact.go：handleNodeFetch 登记一气呵成 + relayTimeoutFired 收敛 + handleFetchReply 转发门控
- internal/node/inputs/sourceopen_unix_test.go（新）、internal/node/service/artifact_source_unix_test.go（新）、internal/hub/core/remote_relay_close_test.go（新）
- internal/node/inputs/inputs_test.go：TestPrepareSymlinkRejected 断言更新（拒绝时点前移，语义等价）
- go.mod / SPEC.md：x/sys 用途扩展登记（unix 源打开 openat，理由与边界）
- AGENTS.md / CLAUDE.md：错误模式清单新增「源文件打开」条目（两文件同步）

---

# 第三轮修复闭合记录（2026-10-25，zcode）

> 全量 22 包 `go test ./... -race -count=1` 绿 + `GOOS=windows go build ./...` 过 + `go vet ./...` / `GOOS=windows go vet ./...` 均干净（windows vet 覆盖 windows 标签测试文件编译）。Unix 侧已闭合部分零触碰。

## W1+W2（必须修）：Windows 源打开重写——逐段 NtCreateFile+OBJ_DONT_REPARSE 游走

**os.Root 在 Windows 对 reparse point 的实际行为（先查证后落笔）**：源码核实 go1.24.13 `os/root_windows.go` `openat` + `os/root_openat.go` `doInRoot`——Root 的每次打开恒带 `O_NOFOLLOW_ANY`（= `internal/syscall/windows` 发明旗标，落到 `NtCreateFile` 的 `OBJ_DONT_REPARSE`），打开遇 reparse point 失败（`STATUS_REPARSE_POINT_ENCOUNTERED`→ELOOP）后**并不就此拒绝**：`readReparseLinkAt` 以 `FILE_OPEN_REPARSE_POINT` 读链，把目标作 `errSymlink` 交 `doInRoot` 继续解析——根内相对链接（含中间段 junction 相对目标）被**跟随**，仅逃逸根外的绝对/junction 目标被拒。与 Unix 实现同病：「不跟随」契约无法经 Root 落实，三审 W2 前提成立。

**方案选定（按「打开时约束 reparse/link 解析」裁决方向）**：Root 的 `O_NOFOLLOW_ANY` 封装在 `internal/syscall/windows`（不可导入），白名单模块 x/sys/windows v0.38.0（同模块零新增，用途扩展登记见 go.mod 注释 + SPEC 三）直接提供同语义原语 `NtCreateFile` + `OBJ_DONT_REPARSE` + `STATUS_REPARSE_POINT_ENCOUNTERED` 常量。新实现（`sourceopen_windows.go` 全量重写，W1 一体闭合）：

1. **根句柄**：`syscall.Open(root, O_RDONLY|O_CLOEXEC)`（与 go1.24 `openRootNolog` 同源——读方式自带 FILE_FLAG_BACKUP_SEMANTICS 可开目录）+ `GetFileInformationByHandle` 显式确认目录形态；根路径为配置管理路径（与 runner exec 同源可信），自身解析允许途经 reparse point（与 Unix 实现根打开无 O_NOFOLLOW 同口径）。
2. **逐段游走**：slash 拆段，每段 `NtCreateFile(RootDirectory=上一段句柄, ObjectName=单组件, OBJ_DONT_REPARSE)`——中间段加 `FILE_DIRECTORY_FILE`（须真实目录），末段裸开（目录留给 fstat 统一拒绝）；每段解析锚定上一段**已打开目录句柄**（句柄绑定对象不绑定名字），检查即打开，无 Lstat→Open 窗口；前缀累计由锚定关系结构保证（W1 的「第二步即失败」不复存在）。`STATUS_REPARSE_POINT_ENCOUNTERED` 显式映射「reparse point（拒绝跟随）」文案。
3. **语义强度**：OBJ_DONT_REPARSE = 组件是任何 reparse point（符号链接/junction/挂载点/OneDrive 占位/Dedup）即拒——比 os.Root（跟随根内链接）严格，正是 Unix O_NOFOLLOW 的「一律不跟随」等价物；末段换入链接的 TOCTOU 窗口内形态同样被拒（打开即校验）。
4. **残余边界（如实声明）**：① 根路径自身可信（配置管理），打开根允许途经 reparse point；② 拒绝一切 reparse point 比合法文件形态更宽（OneDrive 占位/Dedup 文件也被拒）——产物口径为本地卷常规文件，可接受，换来的恰是不跟随完整语义；③ 无需 O_NONBLOCK 对应物：命名管道不在目录句柄相对可达的 NTFS 命名空间（`\\.\pipe` 设备名重写不生效于 RootDirectory 相对名，且拆段后组件不含分隔符），目录命名空间内不存在「open 挂起」对象类别，网络重定向路径不在产物根支持口径（本机 workdir）；④ 形态测试为 windows 构建标签文件，开发环境 darwin 仅验证构建与 vet，执行需 Windows 机器/CI。

**新增测试**（`sourceopen_windows_test.go`，windows 标签）：`TestOpenSourceFileWindowsNestedPrefix`（W1 回归锁：outputs/t1/sub/a.txt 嵌套前缀逐段累计打开可读）；`TestOpenSourceFileWindowsRejectsJunction`（junction 无需特权：末段/中间段/根外逃逸三种形态打开时拒绝，同形态 os.Root 会跟随根内目标）；`TestOpenSourceFileWindowsRejectsSymlink`（根内相对符号链接——os.Root 会读链跟随的形态——打开时拒绝，且链接目标可打开证明拒自不跟随；无特权时跳过，受限处如实声明）。

## W3（建议修）：第三个外发点的确定性并发测试

`TestCloseWaitsInFlightRelayForward`（remote_relay_close_test.go，gate 模式复用）：源 node 合法应答已拆表停表、锁内复核+收敛组登记完成、转发 Send 挂起于请求方闸门（`waitPending` 确定性就位）→ Close 阻塞（断言 150ms 未返回）→ 放行后 Close 返回且请求方恰收一帧 artifact_data（原 mid 关联）、表清空。三个外发点（初始 Send/超时回调/中继转发）至此各有确定性并发用例。

## W4（建议修）：Close 注释承诺收窄

core.go Close 注释改述为「承诺范围=已受理的 node 中继生命周期」，显式列出不在承诺内的两处残余：① 无状态拒绝回帧（invalid/forbidden/artifact_not_found/关闭后 node_offline）即发即弃不占收敛组，Close 后仍可能发出（至多得 transport 已关的 ErrClosed）；② hub 自身取回（HTTP 数据面）无 timer waiter 只清表、未纳入 artWG，生命周期归调用方超时预算。

## 连带（文档同步）

- `inputs.OpenSourceFile` / `service.readArtifactFile` / `artifact_source_test.go` 头注：Windows 半边描述由「os.Root+Lstat 预检+fstat」更新为新方案（旧描述系三审证伪的前提）。
- go.mod 注释 / SPEC 三白名单行 / AGENTS.md / CLAUDE.md「源文件打开」条目：x/sys 用途扩展登记（windows NtCreateFile+OBJ_DONT_REPARSE，os.Root Windows 行为查证结论），两文件同步。

## 改动文件清单

- internal/node/inputs/sourceopen_windows.go：全量重写（逐段 NtCreateFile+OBJ_DONT_REPARSE 游走）
- internal/node/inputs/sourceopen_windows_test.go（新）：W1/W2 Windows 形态测试
- internal/node/inputs/inputs.go：OpenSourceFile 文档注释更新
- internal/node/service/artifact.go：readArtifactFile 文档注释更新
- internal/node/service/artifact_source_test.go：头注更新（旧 os.Root+Lstat 描述）
- internal/hub/core/remote_relay_close_test.go：新增 TestCloseWaitsInFlightRelayForward + 头注
- internal/hub/core/core.go：Close 注释收窄（W4）
- go.mod / SPEC.md / AGENTS.md / CLAUDE.md：登记同步（两文件一致）
- docs/REVIEW-artifact-fetch-remote.md：本闭合记录

---

# 第四轮修复闭合记录（2026-10-25，zcode）

> 全量 22 包 `go test ./... -race -count=1` 绿 + `GOOS=windows go build ./...` 过 + `go vet ./...` / `GOOS=windows go vet ./...` 均干净。核心方案（NtCreateFile 游走/artWG/先登后启）零触碰，仅收尾三处。

## V1（必须修）：根句柄全退出路径释放

`openSourceFile` 在根句柄打开成功后立即 `defer syscall.CloseHandle(rootfd)` **单点关闭**（无重复关闭），覆盖全部退出路径：

- **根校验失败**（GetFileInformationByHandle 出错 / 非目录）：原两处显式 CloseHandle 删除，统一走 defer 关闭（消除「新增退出路径忘关」的结构性风险）；
- **中间段失败**（任一段 openatNoReparse 返错）：先关当前锚定 dirfd（≠rootfd 时），再经 defer 关 rootfd；
- **末段失败**：同上，关锚定 dirfd + defer 关 rootfd；
- **fstat 失败**：f.Close() 末段句柄 + defer 关 rootfd；
- **成功路径**：返回的文件句柄是末段 NtCreateFile 的产物（Windows 句柄为对象引用、锚定父目录对象而非名字，不依赖根句柄存活），根句柄仅游走期间需存活，函数退出时经 defer 关闭，已返回文件不受影响。

正确性依据：defer 置于 Open 成功后第一个语句，其后无任何对 rootfd 的 CloseHandle（grep 核实唯一关闭点），零双关零泄漏。

## V2（建议修）：挂起声明收窄

sourceopen_windows.go 残余边界条目改述：「目录命名空间内不存在 open 挂起对象类别」→「本路径（目录句柄相对打开）无命名管道/FIFO 型 open 等待」，并如实注明**常规文件打开仍可能等待 oplock break**（过滤器授予语义，非本契约约束范围）。

## V3（建议修）：测试精确度

- sourceopen_windows_test.go：TestOpenSourceFileWindowsRejectsSymlink 的「中间段」形态修正——旧形态 ldir 实为**末段**且链接目标为文件，未覆盖中间段；改为建目录 realdir（含子文件 f.txt）、符号链接 ldir→realdir、打开 `outputs/t1/ldir/f.txt`（链接组件真居中间段），断言文案含「中间段」；
- remote_relay_close_test.go：TestCloseWaitsInFlightRelayForward 保存请求帧（`req := reqEnv(...)`），转发应答断言从「ReplyTo 非空」收窄为 `fs[0].ReplyTo == req.MessageID` **精确比较**——中继回传必须恢复原请求 mid 关联，非任意非空值。

## 改动文件清单

- internal/node/inputs/sourceopen_windows.go：V1 根句柄 defer 单点关闭 + V2 声明收窄
- internal/node/inputs/sourceopen_windows_test.go：V3 中间段符号链接形态修正（目录链接+子文件）
- internal/hub/core/remote_relay_close_test.go：V3 转发应答 ReplyTo 精确比较
- docs/REVIEW-artifact-fetch-remote.md：本闭合记录
