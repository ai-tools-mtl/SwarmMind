// groups_collab_integration_test.go 群协作链集成回归（R2 复审建议固化，
// 2026-10-28，雏形见 codex 复审 /tmp 补充验证 REVIEW-R2.md 保留建议 1）：
// ① 真实 MCP 子进程（自建 ./cmd/swarmmind-mcp，stdio JSON-RPC）→ 带 Bearer PAT
// 真实鉴权的 httptest hub → REST 群工具/提交链——groups_auto_test.go 的指引链
// 测试只走 REST，本文件补上「agent 实际拿到的 MCP 入口」这一环；
// ② 混合失败补派 + 并发重放 + 终态回流唯一（R1-02/R1-03 语义的过程级守护）。
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/group"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// —— MCP 二进制自建（整测进程一次，全包复用）——

var (
	mcpBinOnce sync.Once
	mcpBinPath string
	mcpBinErr  error
)

// swarmModuleRoot 自本测试源文件位置向上找 go.mod（不依赖测试进程 cwd）。
func swarmModuleRoot() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller 不可得")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("自 %s 向上未找到 go.mod", thisFile)
		}
		dir = parent
	}
}

// findGoToolchain go 工具链定位：PATH 优先，常见安装位逐个探测（enroll.go 同款
// 兜底，另补 darwin arm64 Homebrew 位）。
func findGoToolchain() (string, error) {
	if p, err := exec.LookPath("go"); err == nil {
		return p, nil
	}
	for _, cand := range []string{"/usr/local/go/bin/go", "/usr/lib/go/bin/go",
		"/opt/go/bin/go", "/usr/local/bin/go", "/snap/bin/go", "/opt/homebrew/bin/go"} {
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	return "", fmt.Errorf("PATH 与常见安装位均未找到 go 工具链")
}

// buildMCPBinary 自建 ./cmd/swarmmind-mcp 到临时目录并缓存路径；工具链不可得或
// 构建失败 → skip 并注明原因（本机无工具链时不拖垮 CI）。
func buildMCPBinary(t *testing.T) string {
	t.Helper()
	mcpBinOnce.Do(func() {
		goBin, err := findGoToolchain()
		if err != nil {
			mcpBinErr = err
			return
		}
		root, err := swarmModuleRoot()
		if err != nil {
			mcpBinErr = err
			return
		}
		dir, err := os.MkdirTemp("", "swarmmind-mcp-itest-")
		if err != nil {
			mcpBinErr = err
			return
		}
		bin := filepath.Join(dir, "swarmmind-mcp")
		cmd := exec.Command(goBin, "build", "-o", bin, "./cmd/swarmmind-mcp")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(dir)
			tail := out
			if len(tail) > 500 {
				tail = tail[len(tail)-500:]
			}
			mcpBinErr = fmt.Errorf("构建 ./cmd/swarmmind-mcp 失败: %v\n%.500s", err, tail)
			return
		}
		mcpBinPath = bin
	})
	if mcpBinErr != nil {
		t.Skipf("跳过（MCP 二进制自建失败，不拖垮无工具链环境）: %v", mcpBinErr)
	}
	return mcpBinPath
}

// —— stdio MCP 客户端（initialize + tools/call，够用最小实现）——

type mcpClient struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	enc    *json.Encoder
	dec    *json.Decoder
	stderr *bytes.Buffer
	nextID int
}

// newMCPClient 启动 MCP 子进程并以 SWARM_HUB_URL/SWARM_HUB_TOKEN 指向 hub
// （Bearer PAT 鉴权）。进程 120s 看门狗：卡死快速失败并带 stderr 尾部，不烧穿
// 整包 go test 超时。
func newMCPClient(t *testing.T, bin, hubURL, token string) *mcpClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, bin)
	// 剥离继承环境里的 SWARM_* 再注入本测目标：同名键重复时 darwin getenv 取
	// 第一个，append 会失效
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "SWARM_TARGET=") || strings.HasPrefix(kv, "SWARM_HUB_URL=") ||
			strings.HasPrefix(kv, "SWARM_HUB_TOKEN=") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env, "SWARM_TARGET=hub", "SWARM_HUB_URL="+hubURL, "SWARM_HUB_TOKEN="+token)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("MCP 子进程启动失败: %v", err)
	}
	c := &mcpClient{t: t, cmd: cmd, stdin: in,
		enc: json.NewEncoder(in), dec: json.NewDecoder(out), stderr: stderr}
	t.Cleanup(func() {
		_ = in.Close() // stdin EOF → 子进程退出；超时被 kill 时本次 Close 报错可忽略
		_ = cmd.Wait()
	})
	return c
}

// rpc 发一帧请求并等同 id 应答（无 id 的推送帧跳过继续等）。
func (c *mcpClient) rpc(method string, params any) map[string]any {
	c.t.Helper()
	c.nextID++
	if err := c.enc.Encode(map[string]any{"jsonrpc": "2.0", "id": c.nextID,
		"method": method, "params": params}); err != nil {
		c.t.Fatalf("MCP %s 请求写入失败: %v", method, err)
	}
	for {
		var line struct {
			ID     json.RawMessage `json:"id"`
			Result map[string]any  `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := c.dec.Decode(&line); err != nil {
			c.t.Fatalf("MCP %s 应答读取失败（stderr 尾部 %.300s）: %v", method, c.stderr.String(), err)
		}
		if len(line.ID) == 0 {
			continue // 推送/notification 帧：非本次应答
		}
		if len(line.Error) > 0 && string(line.Error) != "null" {
			c.t.Fatalf("MCP %s 返回 RPC error: %s", method, line.Error)
		}
		return line.Result
	}
}

// initialize MCP 握手。
func (c *mcpClient) initialize() {
	c.t.Helper()
	c.rpc("initialize", map[string]any{"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "api-test", "version": "1"}})
}

// tool 调一个 MCP 工具并断言失败态是否符合预期（isError 即 REST 非 2xx 被
// 工具层转成失败结果）。
func (c *mcpClient) tool(name string, args map[string]any, wantFail bool) {
	c.t.Helper()
	r := c.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	bad, _ := r["isError"].(bool)
	if bad != wantFail {
		c.t.Fatalf("%s failure=%v want=%v: %v", name, bad, wantFail, r)
	}
}

// TestGroupCollabRealMCPBudgetChain 真实 MCP→REST 预算/FINAL 链（R2 复审建议
// 固化 2026-10-28）：重建的 swarmmind-mcp 子进程经 stdio 调 swarmmind_group_say/
// swarmmind_submit，hub 侧用临时 PAT 走真实 Bearer 鉴权。覆盖：缺 kind 落 user
// 插话（不计预算、不触发收敛）、agent 发言计入群预算、FINAL 注记恰一条、预算
// 耗尽拒绝（budget_exhausted → MCP isError）、预算耗尽后带 group_ref 的正式
// submit 仍成功且挂对群。
func TestGroupCollabRealMCPBudgetChain(t *testing.T) {
	bin := buildMCPBinary(t)
	s, f := newAutoFixtureServer(t, &autoMemberMock{})
	if err := f.core.RegisterNode("n", []protocol.AgentCard{{Name: "a", Exposed: true}}); err != nil {
		t.Fatal(err)
	}
	// 直调群 store 建群（不经 REST handler → 不触发协作派发，任务账本零基线）；
	// 发言/提交链全程走真实 MCP 子进程
	g, err := s.cfg.Groups.CreateGroup("MCP 预算链", "admin", []string{"n/a"})
	if err != nil {
		t.Fatal(err)
	}
	// PAT 真创建路径（tokens_test.go 同款）：明文仅此一次
	_, plain, err := s.tokens.create("collab-mcp", protocol.NowUTC())
	if err != nil {
		t.Fatal(err)
	}
	c := newMCPClient(t, bin, f.srv.URL, plain)
	c.initialize()

	say := func(kind, body string, wantFail bool) {
		t.Helper()
		args := map[string]any{"group_id": g.ID, "from": "n/a", "body": body}
		if kind != "" {
			args["kind"] = kind
		}
		c.tool("swarmmind_group_say", args, wantFail)
	}
	say("", "FINAL: 缺 kind 应按 user 插话处理", false)
	say("agent", "贡献观点", false)
	say("agent", "FINAL: 完成", false)
	for i := 2; i < g.Budget.MaxRounds; i++ {
		say("agent", "补充观点", false)
	}
	say("agent", "超预算发言", true) // budget_exhausted → MCP isError

	msgs, _, err := s.cfg.Groups.ListMessages(g.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	agents, users, notes := 0, 0, 0
	for _, m := range msgs {
		switch {
		case m.Kind == group.KindAgent:
			agents++
		case m.Kind == group.KindUser:
			users++
		case m.Kind == group.KindSystem && strings.Contains(m.Body, "最终版本候选"):
			notes++
		}
	}
	if agents != g.Budget.MaxRounds || users != 1 || notes != 1 {
		t.Fatalf("agents=%d（应=%d 预算） users=%d（应=1） FINAL注记=%d（应=1）: %+v",
			agents, g.Budget.MaxRounds, users, notes, msgs)
	}

	// 预算耗尽只封群发言：带 group_ref 的正式 submit 经 MCP 仍成功，且任务挂对群
	c.tool("swarmmind_submit", map[string]any{"node_id": "n", "agent": "a",
		"stdin": "预算耗尽后的正式提交", "group_ref": g.ID}, false)
	tasks := f.st.View().Tasks
	if len(tasks) != 1 {
		t.Fatalf("正式提交应恰落一单, got %d", len(tasks))
	}
	for _, tk := range tasks {
		if tk.GroupRef != g.ID {
			t.Fatalf("正式任务应挂群 %s, got group_ref %q", g.ID, tk.GroupRef)
		}
	}
}

// TestGroupCollabMixedFailureReplayReflux 混合失败补派 + 并发重放 + 终态回流
// （R2 复审建议固化 2026-10-28）：一成一败后失败成员的预登记回滚（TaskRefs 只
// 留成功单）；缺席成员入网后 12 路并发手动补派，同 idem 键收敛为每成员恰一任务/
// 一 TaskRef/一创建注记；两任务结果各重复上报两次，终态回流注记恰各一条。
func TestGroupCollabMixedFailureReplayReflux(t *testing.T) {
	s, f := newAutoFixtureServer(t, &autoMemberMock{})
	if err := f.core.RegisterNode("n", []protocol.AgentCard{{Name: "a", Exposed: true}}); err != nil {
		t.Fatal(err)
	}
	// 混合成员：n/a 已在网（派发成功），later/b 未注册（派发失败）
	g, err := s.cfg.Groups.CreateGroup("混合失败与重放", "admin", []string{"n/a", "later/b"})
	if err != nil {
		t.Fatal(err)
	}
	s.dispatchGroupCollab(g, "admin")
	tasks := f.st.View().Tasks
	if len(tasks) != 1 {
		t.Fatalf("应一成一败（仅 n/a 落单）, got %d", len(tasks))
	}
	cur, _ := s.cfg.Groups.GetGroup(g.ID)
	if len(cur.TaskRefs) != 1 {
		t.Fatalf("失败成员预登记应已回滚, got TaskRefs %v", cur.TaskRefs)
	}

	// 缺席成员事后入网 → 手动补派 12 路并发重放（同 idem 键幂等收敛）
	if err := f.core.RegisterNode("later", []protocol.AgentCard{{Name: "b", Exposed: true}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.dispatchGroupCollab(g, "admin")
		}()
	}
	wg.Wait()
	tasks = f.st.View().Tasks
	cur, _ = s.cfg.Groups.GetGroup(g.ID)
	if len(tasks) != 2 || len(cur.TaskRefs) != 2 {
		t.Fatalf("并发重放后应恰两任务两引用, got tasks=%d refs=%v", len(tasks), cur.TaskRefs)
	}
	msgs, _, err := s.cfg.Groups.ListMessages(g.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("创建注记不得重复, got %d 条: %+v", len(msgs), msgs)
	}

	// 两任务结果各重复上报两次 → 每任务终态回流注记恰一条（幂等）
	for _, tk := range tasks {
		res := protocol.TaskResult{Status: protocol.ResultCompleted,
			FinalAnswer: "result " + tk.ID, FinishedAt: protocol.NowUTC()}
		payload := mustJSON(t, protocol.TaskResultPayload{TaskID: tk.ID, Status: res.Status, Result: &res})
		for i := 0; i < 2; i++ {
			if err := f.bus.Endpoint(tk.TargetNode).Send("hub", &protocol.Envelope{
				Version: protocol.ProtocolVersion, Type: protocol.MsgTaskResult,
				MessageID: protocol.NewMessageID(), To: "hub", Payload: payload,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	msgs, _, err = s.cfg.Groups.ListMessages(g.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("终态回流应缺注记或重复, got %d 条: %+v", len(msgs), msgs)
	}
	for _, tk := range tasks {
		// 派发时快照仍是 submitted 态：注记体须按回流后的最新任务算
		fresh, ok := f.st.View().Tasks[tk.ID]
		if !ok {
			t.Fatalf("任务 %s 应仍在账本", tk.ID)
		}
		want := group.TaskTerminalBody(fresh)
		n := 0
		for _, m := range msgs {
			if m.Kind == group.KindSystem && m.Body == want {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("任务 %s 终态注记应恰一条, got %d: %q", tk.ID, n, want)
		}
	}
}
