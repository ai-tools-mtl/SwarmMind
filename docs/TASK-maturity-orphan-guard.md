# TASK-maturity-orphan-guard

> 条目6 / maturity-p0 批次收官 / 分支 developing/maturity-p0
> 前置：条目1-5 全部提交；本条目标志批次收官。

## 1. 背景与问题（现状实测，2026-10-26）

**Windows 侧无此问题**：process_windows.go 已声明「node 进程自身退出（服务停止/崩溃/被杀）时句柄由 OS 关闭，Job 内残留进程一并终止」（KILL_ON_JOB_CLOSE + 兜底 SelfJobGuard），「服务停止后子进程绝不孤儿化」由 OS 保证，无需新机制。

**Unix 侧存在崩溃孤儿缺口**：runner 的 setpgid + kill(-pgid) 整组终止只在「node 进程活着」时生效（超时/取消/服务优雅关闭路径均完备）。若 node 被 kill -9 / 断电 / 崩溃：

1. 执行中的 agent 进程组成为孤儿（pgid 仍在、组内进程继续跑，无任何机制回收）；
2. node 重启后不知道重启前曾有哪些 pgid 在跑，无从清扫；
3. 任务本身会按既有链路收敛 needs_review/node_restarted_midway（DoD 10 已验证），**任务状态无缺口，缺的是残留进程组的清扫**。

孤儿 agent 的实际危害：吞 CPU/内存、写 workdir 与产物目录（与重启后的新任务产生写入竞争）、外呼网络。低概率高影响，值得闭环。

## 2. 目标

**崩溃恢复清扫**：node 重启时，识别重启前仍在运行的执行进程组并安全终止；识别不了（证据不足）就保守放过，绝不误杀无关进程。

安全红线（本条目第一原则）：**宁可漏杀；误杀以多重证据 + 逐轮重验逼近「绝不」，残余窗口非零且如实声明（2026-10-28 R3 裁决口径，见附录 6）**。pgid 在 node 重启期间可能被 OS 复用给无关进程组，无条件 kill(-pgid) 是误杀事故源。清扫必须先验证组内进程身份与本次任务匹配，证据不足一律放过（任务已收敛 needs_review，主人看得到；漏杀后果 = 资源浪费，可人工处理）。

## 3. 方案

### 3.1 账本记 pgid

- `ledger.Entry` 增 `Pgid int64 json:"pgid,omitempty"`：runner spawn 成功后 pgid 上报（Unix=cmd.Process.Pid；Windows 恒 0 并注释「Job Object 兜底，无需清扫」，不落账本噪声）。
- 时机：service 侧标记 started 的同事务（或紧随）落盘；崩溃时该字段随账本持久化，重启后可读。
- 旧账本条目无 pgid 字段：恢复扫描对 `pgid==0` 的 started 条目直接放过（向后兼容，零迁移）。

### 3.2 身份校验（安全清扫的前提）

清扫前必须确认 pgid 组长的身份与该任务记录的 agent 匹配，双平台各自的校验途径：

- **Linux**：`/proc/<pgid>/stat` 确认该 pid 是组长（stat 第 5 字段 pgrp == pid）；`/proc/<pgid>/exe` readlink 得真实路径，与任务记录的 agent 可执行路径比对（前缀/完全匹配口径实现时定，写清注释）。
- **darwin**：sysctl `KERN_PROC_ALL` 过滤 `eppgid==pgid` 取组长 pid，`proc_pidpath`（libproc cgo 或 sysctl KERN_PROC_PID+KERN_PROCPATHVALUE 路径，实现时探明最轻正道）取真实路径比对。
- 路径校验的匹配口径宁严勿松：完全一致优先；agent 是脚本/解释器时 exe 可能是解释器路径——此时退化为「组长存在且是组长 + 启动时间早于 node 重启时间」的组合证据，任一证据不足即放过。**实现时若身份校验在某平台做不可靠，该平台直接放过清扫并在注释声明**（保守优先）。最终实现以「证据三元组（pgid+指纹+启动周期）+ 逐轮重验」落地方案（见附录 6 裁决），残余窗口非零如实声明。
- x/sys/unix 若因 proc_pidpath/sysctl 引入新符号，按 SPEC 三白名单规矩登记理由。

### 3.3 启动恢复扫描

- node 启动恢复流程中（账本加载后、服务就绪前），扫 `state==started && pgid>0` 的条目：
  1. 身份校验通过 → `kill(-pgid, SIGKILL)` 整组终止（复用 runner 现有整组终止语义），账本条目照常走既有 needs_review 收敛（不动终态逻辑）；
  2. 校验不过/进程已不存在（ESRCH）→ 放过，日志记一行「跳过清扫：pgid=N 无匹配证据/已退出」；
- 清扫动作幂等、一次扫完即止，不引入后台周期任务（崩溃清扫是启动时一次性动作）。
- Windows：恢复扫描遇 Windows 产物账本（pgid 恒 0）自然全跳过，代码路径恒空转——注释声明即可。

### 3.4 runner 侧接口

- runner 暴露最小接口：spawn 后取 pgid（Unix；Windows 返回 0）+ 恢复清扫函数（输入 pgid + 任务 agent 信息，内部完成身份校验 + 整组终止，返回清扫结果供日志）。
- 超时/取消/优雅关闭路径**零改动**（它们已完备，不动既有行为）。

## 4. DoD（验收标准，逐条给证据）

1. 单测：spawn 后 Entry.Pgid 已落账本；Windows 语义（pgid=0 不落）有断言。
2. 单测（Unix 真进程）：构造 agent 进程组（复用 runner_test.go:260 的 sh+sleep 组模式）→ 模拟崩溃（直接杀 node 测试进程或绕过取消路径）→ 新实例恢复扫描 → 组内 sleep 被终止（pgrep 探测无幸存）→ 账本该条目收敛 needs_review。
3. 单测（误杀防线）：伪造 pgid 指向无关进程（如当前测试自身所在进程组的无关 sleep，或路径不匹配的进程组）→ 恢复扫描放过（无关进程存活断言 + 日志含「跳过清扫」）。
4. 单测：旧账本条目（pgid=0）恢复扫描直接放过，零行为变化（向后兼容锁）。
5. 回归：`go build/vet/test ./...` 全绿；既有取消/超时/优雅关闭测试零回归（本条目不得破坏既有路径）。
6. Windows 交叉编译 `GOOS=windows go build ./...` 过（pgid=0 语义 + no-op 清扫编译干净）。
7. 真机实测（Unix）：本机 kill -9 node 进程（带在跑 agent 组）→ 重启 → 孤儿组被清扫 + 任务 needs_review，证据留存。
8. 真机实测（Windows）：**需伦哥 Windows 机器在线配合**——Windows 服务化 node + 在跑任务 → 强杀服务进程 → 验证 Job 兜底（无孤儿）。此项**不阻塞本条目交付**：若伦哥机器不在线，列为「待真机补验」记录在案（Windows 语义 2026-10-24 P4② 已有服务化验证背书，本轮为补强证据）。
9. SPEC 同步：五（runner 进程治理）补崩溃清扫语义一段；六.六不涉及（无新端点/工具）。
10. 版本纪律：不 commit；最终输出改动文件清单 + DoD 逐条结果。

## 5. 已知陷阱

- **误杀是唯一不可接受结局**：身份校验宁严勿松，某平台证据不可靠就整平台放过（注释声明），不为覆盖率牺牲安全。标准库无 pidfd 的非原子残余窗口按附录 6 裁决如实声明（多重巧合概率趋零非零），不承诺原子性。
- pgid==组长 pid 的前提是 Setpgid（process_unix.go:16 已保证），恢复扫描可依赖此不变式，但校验仍以「stat pgrp==pid」实证而非假设。
- darwin 无 /proc：身份校验走 sysctl/proc_pidpath，勿照抄 Linux 实现条件编译；若引入 cgo 需论证无更轻替代（CGO_ENABLED=0 的发布矩阵会切掉 cgo 代码！——优先选 sysctl 纯 Go 路径，golang.org/x/sys unix 包有 SysctlRawString/SysctlUvmeter 等原语可探）。
- needs_review 收敛链路（service.go:453 区域）是既有成熟路径，清扫**不得**改动终态语义，只加进程终止动作。
- 账本写入时机注意崩溃窗口：pgid 落盘晚于 spawn 则崩溃时字段丢失——落盘点尽量前移（started 标记事务内），残余窗口如实注释声明（概率极小且后果=放过不误杀）。
- 恢复扫描在账本加载后服务就绪前执行，注意与 fence/信箱恢复等既有启动逻辑的顺序与锁序，勿引入死锁。
- 版本纪律：不 commit；Windows 真机验证不阻塞交付（列待补验）。

## 6. 附录：Hana 裁决（2026-10-28，R3 上报触发）

R3 按返工上限规则上报「实例身份安全保证/绝不误杀承诺」与实现的冲突：probe→SIGKILL
非原子（Go 标准库无 pidfd），窗口内 goroutine 可被调度暂停任意时长（无上界），原组
退出 + pgid 复用在窗口内完成即构成误杀面；微秒指纹与多重证据把触发条件压成多重巧合
但概率非零，「绝不误杀」的绝对承诺在标准库层面无法兑现。

裁决：**接受方案 A——多重防线 + 诚实声明残余窗口非零**，不做平台级安全放过，暂不
引入 pidfd：

1. 本系统为自用轻运维（SPEC 定位），清扫对象恒为本 node 自己 spawn 的组。误杀需
   四重巧合叠加：同启动周期 + 实例指纹等值 + comm 与 agent 同名 + probe→kill 窗口
   内恰好完成原组退出与 pid 释放复用——概率趋零但非零，如实声明。
2. 漏杀方向安全：任务已收敛 needs_review/node_restarted_midway，主人可见，漏杀后果
   仅资源浪费（与红线第一原则的权衡方向一致）。
3. pidfd 方案（Linux pidfd_open + P_PIDFD 信号）可给 Linux 强保证，但 darwin 内核
   无等价物——引入后双平台行为分化（Linux 强/darwin 弱），主力机（darwin）清扫
   反而降级为全放过，复杂度收益不配。列为后续可选增强，不属本条目。
4. 措辞纪律：SPEC 五清扫段、本任务书红线段、orphan_unix.go / orphan_darwin.go /
   process_unix.go 注释统一为「多重证据 + 逐轮重验，残余窗口非零（无时长上界）且
   已压缩至多重巧合」的诚实口径，不再使用「绝不误杀」绝对承诺与「纳秒级窗口」
   无上界表述。

措辞同步清单：SPEC.md 五（476 行段落）；本文件 2/3.2/5 节红线与陷阱段；
internal/node/runner/{orphan_unix.go,orphan_darwin.go,process_unix.go}。
