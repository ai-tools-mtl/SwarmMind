# SwarmMind 错误模式清单（zcode 自动加载）

> 条目格式：规则式一句话 + 根因 + 自查命令。禁止空话。发现新模式当轮追加。

## 通用工程

- 禁止直接覆盖写状态文件 → 崩溃时留下半截 JSON → 原子写：temp + rename。自查：`grep -rn "os.WriteFile" internal/ | grep -v tmp`
- exec 超时 kill 后必须 Wait → 否则僵尸进程堆积。自查：`grep -n "Process.Kill" internal/node/ | wc -l`（每处 kill 附近应有 Wait）
- 时间序列化一律 UTC，勿格式化成本地字符串存盘
- 禁止在 V0 引入 SPEC 第三节白名单之外的依赖；新增依赖必须在代码注释写理由
- 工人不执行 git commit；提交按钮在伦哥手里。改完跑 `go build ./... && go test ./...` 并在最终输出列出改动文件清单与每条 DoD 的验证结果
