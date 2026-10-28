//go:build windows

package runner

// 孤儿清扫 Windows 侧（maturity-orphan-guard）：no-op。
//
// Windows 无此缺口（process_windows.go 头注释声明的既有契约）：任务级 Job Object
// 带 JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE——node 进程自身退出（服务停止/崩溃/被杀）
// 时句柄由 OS 关闭，Job 内残留进程一并终止；另有兜底 SelfJobGuard。即「node 崩溃
// 后子进程绝不孤儿化」由 OS 保证，恢复扫描无 Windows 条目（spawnIdentity 恒
// (0,0)，账本不落 pgid），本方法恒返回 not_applicable。
func (r *Runner) CleanupOrphanGroup(pgid, procStart int64, bootIdent string) string {
	return "not_applicable"
}

// currentBootIdent Windows 无孤儿清扫机制，启动周期标识恒空（spawnIdentity 恒
// (0,0)，账本不落证据，此值不会被消费）。
func currentBootIdent() string { return "" }
