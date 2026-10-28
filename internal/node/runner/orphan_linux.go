//go:build linux

package runner

// Linux 孤儿组长身份校验（maturity-orphan-guard，R1 必修2/必修3 + R2 必修1）：
// 证据源 /proc/<pid>/stat——pgrp 字段判组长、starttime 字段做实例指纹、comm
// 兜底进程名；启动周期用 /proc/sys/kernel/random/boot_id 限定（R2 必修1：
// ticks 是「本次开机起算」的相对值，账本跨 OS 重启保留，无 boot_id 则跨周期
// 的同 pid/同 tick/同名无法区分）。R1 必修3 勘误：去 comm 后字段序为
// state,ppid,pgrp,...,starttime（0-based 0/1/2/…/19）——旧实现把 fields[1]
//（ppid）当 pgrp 读，合法组长全被错误放过。

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// probeLeader Linux 身份证据（与 orphan_darwin.go 同签名，build tag 分派）：
// alive=false 即组长不存在/不可证；alive=true 时 deny 非空为拒绝原因（指纹
// 不符/名称不符），空串=全部证据命中。
func probeLeader(pid, pgid int, wantBase string, procStart int64) (alive bool, deny string) {
	pgrp, start, comm, err := parseProcStat(pid)
	if err != nil {
		return false, "组长不存在或证据不可读"
	}
	if pgrp != int64(pgid) {
		return false, "组长不存在（pid 已非组长，疑似编号易主）"
	}
	if start != procStart {
		// 实例指纹不符：pgid 复用给新进程（tick 粒度下的等值碰撞已由 boot_id
		// 周期校验 + pid 分配递增语义兜住），放过。
		return true, "组长启动指纹与账本不符，疑似 pgid 复用"
	}
	if comm != wantBase {
		return true, fmt.Sprintf("进程名不匹配（期望 %s）", wantBase)
	}
	return true, ""
}

// procStartTime 组长启动时刻指纹（spawn 侧落账本用）：/proc/<pid>/stat 的
// starttime（clock ticks since boot，10ms 级粒度）。读取失败返回 0（恢复时
// 证据不足放过，安全侧）。
func procStartTime(pid int) int64 {
	_, start, _, err := parseProcStat(pid)
	if err != nil {
		return 0
	}
	return start
}

// currentBootIdent 当前系统启动周期标识（R2 必修1）：/proc/sys/kernel/random/
// boot_id（内核每次启动生成的 UUID）。spawn 时随指纹落账本，恢复时比对——
// 不等即跨启动周期，孤儿必已被 OS 全灭，整条证据视为过期（放过）。
func currentBootIdent() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// parseProcStat 解析 /proc/<pid>/stat。comm 可能含空格与右括号：以第一个 '('
// 与最后一个 ')' 为界提取，其后空白分隔字段 0-based 为 0=state 1=ppid 2=pgrp
// 3=session … 19=starttime（内核 man proc 第 3/4/5/22 字段，1-based）。
// 返回 pgrp、starttime、comm。字段不足或数值异常返回错误（调用方按证据不足
// 放过处理）。
func parseProcStat(pid int) (pgrp int64, starttime int64, comm string, err error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, "", err
	}
	return parseProcStatBytes(stat, pid)
}

// parseProcStatBytes parseProcStat 的纯函数核心（合成串可测：comm 含空格与
// 右括号、字段序、pgrp/starttime 提取，R2 必修3）。
func parseProcStatBytes(stat []byte, pid int) (pgrp int64, starttime int64, comm string, err error) {
	s := string(stat)
	open := strings.IndexByte(s, '(')
	close_ := strings.LastIndex(s, ")")
	if open < 0 || close_ < open || close_+2 > len(s) {
		return 0, 0, "", fmt.Errorf("stat 格式异常 pid=%d", pid)
	}
	comm = s[open+1 : close_]
	fields := strings.Fields(s[close_+2:])
	if len(fields) < 20 {
		return 0, 0, "", fmt.Errorf("stat 字段不足 pid=%d: %d", pid, len(fields))
	}
	pgrp, e1 := strconv.ParseInt(fields[2], 10, 64)
	starttime, e2 := strconv.ParseInt(fields[19], 10, 64)
	if e1 != nil || e2 != nil {
		return 0, 0, "", fmt.Errorf("stat 数值解析失败 pid=%d", pid)
	}
	return pgrp, starttime, comm, nil
}
