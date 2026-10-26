module github.com/mtl802/swarmmind

go 1.24.0

require (
	github.com/coder/websocket v1.8.15
	gopkg.in/yaml.v3 v3.0.1

	// modernc.org/sqlite：SPEC 三依赖白名单登记项（2026-10-26 hub 存储迁 SQLite）。
	// 理由：纯 Go 实现（无 cgo，保 Windows 构建与交叉编译，跨平台纪律不断）+ 单文件
	// 嵌入库零部署（对齐自用轻运维定位）+ WAL/synchronous=FULL 兑现掉电耐久性升级。
	// 用途：hub 全量状态持久层（internal/hub/store，实体分表 + key + JSON 体列，
	// diff 增量写入）；node ledger 保持 JSON 不引库。间接依赖树较大（libc/memory 等）
	// 属纯 Go SQLite 方案的已知代价，接受。
	modernc.org/sqlite v1.46.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/exp v0.0.0-20251023183803-a4bb9ffd2546 // indirect
	modernc.org/libc v1.67.6 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

// golang.org/x/sys：SPEC 三依赖白名单登记项（2026-10-24 P4② Windows 服务化）。
// 理由：Go 官方扩展库（半标准库），Windows 服务 SCM 编程（svc/svc/mgr）与
// Job Object 进程治理（windows）的唯一正道，无更轻替代。用途扩展登记（2026-10-24
// 跨机取回复审 R1 + 2026-10-25 三审 W2）：unix 路径 internal/node/inputs/
// sourceopen_unix.go 经 unix.Openat 实现源文件逐段 openat 游走（内核强制不跟随
// 符号链接/不阻塞）——标准库 syscall 在 darwin 不导出 openat（Darwin 走 libc
// 桩），os.Root 的 Unix 实现会跟随根内末段链接（ELOOP 读链重试）无法承担该契约；
// windows 路径 internal/node/inputs/sourceopen_windows.go 经 windows.NtCreateFile
// +OBJ_DONT_REPARSE 实现逐段游走（打开时约束 reparse/link 解析）——os.Root 在
// Windows 遇 reparse point 读链后跟随根内链接，OBJ_DONT_REPARSE 等价旗标
// O_NOFOLLOW_ANY 封装在 internal/syscall 不可导入。同模块零新增；仅 Windows
// 服务化/Job Object + windows/unix 源打开引入。
require golang.org/x/sys v0.38.0
