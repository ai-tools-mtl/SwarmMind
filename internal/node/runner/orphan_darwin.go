//go:build darwin

package runner

// darwin 孤儿组长身份校验（maturity-orphan-guard，R1 必修2 + R2 必修1）：证据
// 源 sysctl kern.proc.pid 单进程 KinfoProc——P_pid/Eproc.Pgid 判组长、P_starttime
// 做实例指纹与 bootTime 防线、P_comm 兜底进程名；启动周期用 kern.boottime 限定。
// darwin 专属符号（KinfoProc/SysctlKinfoProc）隔离在本文件，不进 unix 通用文件
//（R1 必修2：GOOS=linux 交叉编译此前失败于此）。
//
// R2 必修1（真机实证）：P_starttime 秒精度下「同秒不同实例」指纹碰撞——node
// 崩溃后同秒内 pid 复用给同名进程、或 KeepAlive 秒级重启场景，旧实现秒值等值
// 误判通过（真机 TC-ORP 实测 skip 误拒合法孤儿的反面同源）。指纹升级为微秒
//（Timeval Sec*1e6+Usec），bootTime 防线同用微秒比较，同秒碰撞被精度自然消解。

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// probeLeader darwin 身份证据（与 orphan_linux.go 同签名，build tag 分派）：
// alive=false 即组长不存在/不可证；alive=true 时 deny 非空为拒绝原因，空串=
// 全部证据命中。bash 自改 comm 的名实分离走「进程名不匹配」放过安全侧。
func probeLeader(pid, pgid int, wantBase string, procStart int64) (alive bool, deny string) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false, "组长不存在或证据不可读"
	}
	if int(k.Proc.P_pid) != pid || int(k.Eproc.Pgid) != pgid {
		return false, "组长不存在（pid 已非组长，疑似编号易主）"
	}
	comm := commString(k.Proc.P_comm[:])
	if comm != wantBase {
		return true, fmt.Sprintf("进程名不匹配（期望 %s）", wantBase)
	}
	start := darwinStartMicros(k)
	if start != procStart {
		// 实例指纹不符（微秒精度）：pgid 复用给新进程，放过。
		return true, "组长启动指纹与账本不符，疑似 pgid 复用"
	}
	// 第二道防线（同微秒精度）：组长启动微秒必须早于本 node 进程启动——双实例
	// 微秒等值且均早于新 node 的碰撞（R2 审查 identity.log 同秒实验形态）由本
	// 防线拒绝；更深的巧合叠加已归入残余窗口声明（orphan_unix.go 头注释）。
	if start >= bootTime.UnixNano()/1e3 {
		return true, "组长不早于本 node 启动（疑似同刻复用）"
	}
	return true, ""
}

// darwinStartMicros KinfoProc.P_starttime（Timeval）→ 微秒（R2 必修1）。
func darwinStartMicros(k *unix.KinfoProc) int64 {
	return k.Proc.P_starttime.Sec*1e6 + int64(k.Proc.P_starttime.Usec)
}

// procStartTime 组长启动时刻指纹（spawn 侧落账本用）：微秒（R2 必修1）。
// 读取失败返回 0（恢复时证据不足放过，安全侧）。
func procStartTime(pid int) int64 {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0
	}
	return darwinStartMicros(k)
}

// currentBootIdent 当前系统启动周期标识（R2 必修1）：kern.boottime 合成微秒
// 字符串。spawn 时随指纹落账本，恢复时比对——不等即跨启动周期，孤儿必已被
// OS 全灭（断电/重启全灭），整条证据视为过期（放过）。
func currentBootIdent() string {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d.%06d", tv.Sec, tv.Usec)
}
