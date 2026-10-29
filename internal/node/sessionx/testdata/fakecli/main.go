// fakecli 是 sessionx 的测试专用假 CLI（R1 审查 S1 修订：以真 JSON 编码器产出
// 输出，任意含引号/换行/反斜杠/非 ASCII 的合法 prompt 都不会生成非法 JSON；单
// 一 Go 源构建为各平台原生二进制，Windows 亦可执行，不再依赖 /bin/sh）。
//
// 行为由 FAKE_* 环境变量驱动（与旧 sh fake 同名同义，测试可平移）：
//
//	FAKE_KIND          输出形态："codex"（JSONL 事件流）| "zcode"（整份终态 JSON）
//	FAKE_ARGV_LOG      命令形态留痕（每参数一行追加）
//	FAKE_LOG           start/end 序贯留痕（等待文件命中后追加 saw）
//	FAKE_SLEEP         秒（可为小数），串行化/超时验证
//	FAKE_WAIT_FILE     轮询等待文件出现（至多 250×20ms），跨会话并行屏障
//	FAKE_TOUCH         开跑即创建的标记文件
//	FAKE_STDOUT        原样输出该串并退出（失败语义注入）
//	FAKE_EXIT_CODE     非 0 退出码注入（stderr 输出 boom）
//	FAKE_THREAD_ID     codex 形态的原生 thread id（必需）
//	FAKE_SESSION_ID    zcode 形态的原生 sessionId（必需）
//	FAKE_MISMATCH_ID   续轮时改以该 ID 回带（「另起线程」模拟）
//	FAKE_ECHO_ENV      答复正文追加 " env:<该环境变量取值>"（env 注入面验证）
//	FAKE_ON_RESUME     置位后 FAKE_SLEEP/WAIT_FILE/TOUCH/SPAWN_DELAY_WRITER 仅在续轮
//	                   （携 resume ID）生效——隔离 Create 与 Send 两相的用例注入
//	FAKE_SPAWN_DELAY_WRITER  "秒|路径"：派生组内延迟写手后立即退出（孙进程治理验证；
//	                   延迟写手不改进程组，随本进程同组留驻，由轮终整组收尾终止）。
//	                   派生先于本 fake 的一切阻塞动作，且孙进程启动即写「路径.started」
//	                   自证存活、父 fake 等栅格命中才继续——超时截止必然发生在后代
//	                   真实派生之后（R2 复审 S1：旧形态 sleep 在派生前，截止先杀
//	                   父进程，孙进程从未诞生，超时路断言空过）
//
// 仅标准库，无外部依赖（SPEC 三白名单纪律）；属测试资产，随测试构建，不入生产链。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"
)

func main() {
	args := os.Args[1:]
	env := func(k string) string { return os.Getenv(k) }

	if p := env("FAKE_ARGV_LOG"); p != "" {
		appendLines(p, args...)
	}

	// 续轮识别：codex = resume 子命令后随 ID；zcode = --resume 旗标后随值。
	// prompt = 末位参数（两家 argv 形态均以 prompt 收尾，探针已验证）。
	resume := ""
	for i := 1; i < len(args); i++ {
		if (env("FAKE_KIND") == "codex" && args[i-1] == "resume") ||
			(env("FAKE_KIND") == "zcode" && args[i-1] == "--resume") {
			resume = args[i]
		}
	}
	prompt := ""
	if len(args) > 0 {
		prompt = args[len(args)-1]
	}

	if p := env("FAKE_LOG"); p != "" {
		appendLines(p, "start")
	}
	// 屏障/耗时/派生类动作可限定为仅续轮生效（FAKE_ON_RESUME）：同一 agent 配置
	// 会被 Create 与 Send 共用，两相隔离靠此开关。
	resumeOnly := env("FAKE_ON_RESUME") != "" && resume == ""
	if !resumeOnly {
		// 派生先于阻塞（R2 复审 S1）：延迟写手先真实派生并等 started 自证栅格命中，
		// 父进程随后才进入 SLEEP 等阻塞/退出——超时杀组时孙进程必然已真实在场。
		if p := env("FAKE_SPAWN_DELAY_WRITER"); p != "" {
			spawnDelayWriter(p)
		}
		if s := env("FAKE_SLEEP"); s != "" {
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				time.Sleep(time.Duration(f * float64(time.Second)))
			}
		}
		if p := env("FAKE_WAIT_FILE"); p != "" {
			for i := 0; i < 250; i++ {
				if fileExists(p) {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if env("FAKE_LOG") != "" && fileExists(p) {
				appendLines(env("FAKE_LOG"), "saw")
			}
		}
		if p := env("FAKE_TOUCH"); p != "" {
			_ = os.WriteFile(p, nil, 0o644)
		}
		if p := env("FAKE_SPAWN_DELAY_WRITER"); p != "" {
			spawnDelayWriter(p)
		}
	}
	if s := env("FAKE_STDOUT"); s != "" {
		fmt.Println(s)
		os.Exit(exitCode(env))
	}
	if s := env("FAKE_EXIT_CODE"); s != "" {
		fmt.Fprintln(os.Stderr, "boom")
		code, _ := strconv.Atoi(s)
		os.Exit(code)
	}

	switch env("FAKE_KIND") {
	case "codex":
		tid, ok := os.LookupEnv("FAKE_THREAD_ID")
		if !ok || tid == "" {
			fmt.Fprintln(os.Stderr, "fakecli: FAKE_THREAD_ID 必填")
			os.Exit(3)
		}
		if resume != "" && resume != tid {
			fmt.Println(`{"type":"error","message":"resume: no such thread"}`)
			os.Exit(4)
		}
		if m := env("FAKE_MISMATCH_ID"); resume != "" && m != "" {
			tid = m
		}
		emitJSONLines(
			map[string]any{"type": "thread.started", "thread_id": tid},
			map[string]any{"type": "turn.started"},
			map[string]any{"type": "item.completed", "item": map[string]any{
				"id": "item_0", "type": "agent_message", "text": echoText(env, prompt),
			}},
			map[string]any{"type": "turn.completed", "usage": map[string]any{
				"input_tokens": 42082, "cached_input_tokens": 34048,
				"cache_write_input_tokens": 0, "output_tokens": 139,
				"reasoning_output_tokens": 0,
			}},
		)
	case "zcode":
		sid, ok := os.LookupEnv("FAKE_SESSION_ID")
		if !ok || sid == "" {
			fmt.Fprintln(os.Stderr, "fakecli: FAKE_SESSION_ID 必填")
			os.Exit(3)
		}
		if resume != "" && resume != sid {
			fmt.Println(`{"error":"no such session"}`)
			os.Exit(5)
		}
		if m := env("FAKE_MISMATCH_ID"); resume != "" && m != "" {
			sid = m
		}
		emitJSON(map[string]any{
			"sessionId": sid,
			"traceId":   "trace-x",
			"turnId":    "turn_fixed",
			"response":  echoText(env, prompt),
			"usage": map[string]any{
				"source": "provider", "modelRequestCount": 3,
				"inputTokens": 59170, "outputTokens": 1240, "totalTokens": 60410,
				"cacheReadTokens": 27264, "cacheWriteTokens": 0, "reasoningTokens": 0,
			},
			"eventCount": 1178,
			"projection": map[string]any{
				"status": "idle", "turnCount": 1, "totalTokenCount": 60410,
				"contextUsed": 20773, "contextWindow": 1000000,
			},
		})
	default:
		fmt.Fprintln(os.Stderr, "fakecli: FAKE_KIND 必填（codex|zcode）")
		os.Exit(3)
	}
	if p := env("FAKE_LOG"); p != "" {
		appendLines(p, "end")
	}
}

// echoText 答复正文：echo:<prompt>（真 JSON 编码保真任意 prompt）+ 可选 env 注入面。
func echoText(env func(string) string, prompt string) string {
	s := "echo:" + prompt
	if k := env("FAKE_ECHO_ENV"); k != "" {
		s += " env:" + env(k)
	}
	return s
}

// spawnDelayWriter 派生组内延迟写手（不改进程组 → 留驻本进程组）后立即退出、
// 不等待：轮终整组收尾必须终止它，否则它会在轮次返回后写入 marker（治理回归断言）。
// 同步栅格（R2 复审 S1）：孙进程启动即写 marker+".started" 自证存活，本函数轮询
// 等到该文件出现才返回——父进程后续的任何阻塞与被杀都必然发生在后代真实派生之后。
func spawnDelayWriter(spec string) {
	var delaySecs int
	var path string
	for i := 0; i < len(spec); i++ {
		if spec[i] == '|' {
			delaySecs, _ = strconv.Atoi(spec[:i])
			path = spec[i+1:]
			break
		}
	}
	if path == "" {
		return
	}
	started := path + ".started"
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", fmt.Sprintf(`echo x>"%s" & ping -n %d 127.0.0.1 >nul & echo survived>"%s"`, started, delaySecs+1, path))
	} else {
		cmd = exec.Command("sh", "-c", fmt.Sprintf("echo x > '%s'; sleep %d; echo survived > '%s'", started, delaySecs, path))
	}
	// Start 返回时子进程已 spawn（同进程组——不改 pgid，轮终整组收尾必须终止它）；
	// started 栅格命中后本 fake 才继续（输出并结束），延迟写手由 fake 之死的组收尾
	// 负责回收。
	_ = cmd.Start()
	for i := 0; i < 250; i++ { // 与 FAKE_WAIT_FILE 同款轮询界限（至多 5s）
		if fileExists(started) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func emitJSONLines(objs ...map[string]any) {
	enc := json.NewEncoder(os.Stdout)
	for _, o := range objs {
		_ = enc.Encode(o) // Encode 自带换行
	}
}

func emitJSON(obj map[string]any) {
	b, err := json.Marshal(obj)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakecli:", err)
		os.Exit(3)
	}
	fmt.Println(string(b))
}

func appendLines(path string, lines ...string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func exitCode(env func(string) string) int {
	if s := env("FAKE_EXIT_CODE"); s != "" {
		code, _ := strconv.Atoi(s)
		return code
	}
	return 0
}
