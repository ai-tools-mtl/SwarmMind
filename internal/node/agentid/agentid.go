// Package agentid 计算 node 本机 agent 的 workspace 互斥键（P1.5/SPEC 4.10）：
// 对 ExecProfile.Workdir 做路径规范化后派生稳定短 ID，随 AgentCard 上报 hub；
// 路径本体不出本机（hub 不感知路径，互斥只用派生键）。
//
// 已知局限（SPEC 九声明）：hardlink / bind mount 指向同一目录时路径不同，无法
// 用路径识别（会生成双键）；大小写不敏感文件系统（APFS 默认盘）上大小写变体
// 路径会分裂为双键。二者为文档声明局限，V0 不做 inode 级归并。
package agentid

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

// workspaceIDLen 互斥键长度（hex 字符数，取 SHA-256 摘要前缀；16 字符 = 64bit，
// 冲突概率对本系统任务量可忽略，且键短利于信封体积）。
const workspaceIDLen = 16

// WorkspaceID 由 workdir 派生 workspace 互斥键：规范化路径 SHA-256 hex 前 16 字符。
// 规范化 = Abs + Clean + EvalSymlinks；EvalSymlinks 失败（如目录尚不存在）时
// Resolve 回退 Clean+Abs——见 Resolve 的 fallback 注记。同目录不同别名（symlink、
// macOS /tmp vs /private/tmp）规范化后同键，避免同目录双键击穿互斥。
func WorkspaceID(workdir string) string {
	canonical, _ := Resolve(workdir)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:workspaceIDLen]
}

// Resolve 返回 workdir 的规范化路径与是否走了回退。规范化三步（SPEC 4.10）：
//  1. filepath.Abs（相对 workdir 依进程 cwd；内部含 Clean）；
//  2. filepath.Clean（显式兜底，自文档化）；
//  3. filepath.EvalSymlinks（消解 symlink 别名；目录不存在等失败时回退第 2 步
//     结果并注记 fallback=true——此时 symlink 别名未消解，同目录可能双键，
//     由调用方按需记日志；node 启动时目录通常已存在，回退是边缘路径）。
func Resolve(workdir string) (canonical string, fallback bool) {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		// 无法取 cwd 等极罕见失败：尽力 Clean 后回退（workdir 配置侧强制绝对路径，
		// 生产不可达；测试与防御路径保持确定性输出）
		return filepath.Clean(workdir), true
	}
	abs = filepath.Clean(abs)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs, true
	}
	return resolved, false
}
