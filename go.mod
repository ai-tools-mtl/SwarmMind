module github.com/mtl802/swarmmind

go 1.24.0

require (
	github.com/coder/websocket v1.8.15
	gopkg.in/yaml.v3 v3.0.1
)

// golang.org/x/sys：SPEC 三依赖白名单登记项（2026-10-24 P4② Windows 服务化）。
// 理由：Go 官方扩展库（半标准库），Windows 服务 SCM 编程（svc/svc/mgr）与
// Job Object 进程治理（windows）的唯一正道，无更轻替代；仅 //go:build windows
// 路径引入，非 Windows 平台编译产物不含相关代码，行为零变化。
require golang.org/x/sys v0.38.0
