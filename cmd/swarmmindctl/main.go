// swarmmindctl 人工操作 CLI（SPEC 六交互形态）：submit/query/agents/nodes/review
// 五命令，薄包装 hub 管理面 API（登录 → 会话 cookie → 端点调用）。
//
// 反馈闭环（INTERACTION 四.5）：技术态永远配人话（双语格式）；needs_review 详情
// 第一句固定为「执行到一半机器重启了，结果不确定——等待本机主人核对，请勿重新提交」；
// 任何终态输出带「下一步」指引。review 在 V0 仅展示待核对上下文与处理指引，
// 不写核对记录（P3 增补）。
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// 全局默认与 token 环境变量名。
const (
	defaultHub     = "http://127.0.0.1:8444"
	tokenEnvVar    = "SWARMMIND_HUB_TOKEN"
	httpTimeout    = 45 * time.Second // 覆盖产物取回 30s 超时
	loginBodyLimit = 1 << 16
)

func main() {
	args := os.Args[1:]
	// 全局旗标可置于子命令前（swarmmindctl --hub URL --token TOK submit …）：
	// 前置形态先摘除为默认值，子命令内 FlagSet 仍接受同型旗标（就近覆盖）。
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		take := func() string {
			if len(args) < 2 {
				usage()
				os.Exit(2)
			}
			v := args[1]
			args = args[2:]
			return v
		}
		switch args[0] {
		case "--hub", "-hub":
			preHub = take()
		case "--token", "-token":
			preToken = take()
		case "--json", "-json":
			preJSON = true
			args = args[1:]
		default:
			usage()
			os.Exit(2)
		}
	}
	if len(args) < 1 {
		usage()
		os.Exit(2)
	}
	if err := dispatch(args[0], args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "swarmmindctl: %v\n", err)
		os.Exit(1)
	}
}

// 前置全局旗标（子命令前形态的默认值）。
var (
	preHub   string
	preToken string
	preJSON  bool
)

func usage() {
	fmt.Fprintf(os.Stderr, `swarmmindctl — SwarmMind 人工操作 CLI（包装 hub 管理面 API）

用法:
  swarmmindctl submit  --node NODE --agent NAME [--arg V]... [--stdin] [--idem KEY] [--by NAME]
  swarmmindctl query   --id TASK_ID
  swarmmindctl agents
  swarmmindctl nodes
  swarmmindctl review  --id TASK_ID

公共旗标:
  --hub URL    管理面地址（默认 %s）
  --token TOK  管理令牌（缺省读环境变量 %s）
  --json       以原始 JSON 输出（默认人话摘要）
`, defaultHub, tokenEnvVar)
}

// cliArgs 公共旗标（各子命令共用）。
type cliArgs struct {
	hub  string
	tok  string
	json bool
	fs   *flag.FlagSet
}

// newArgs 注册公共旗标（默认值 = 前置形态摘除值，可被子命令内旗标覆盖）。
func newArgs(name string) (*cliArgs, *flag.FlagSet) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	a := &cliArgs{fs: fs}
	hubDefault := defaultHub
	if preHub != "" {
		hubDefault = preHub
	}
	fs.StringVar(&a.hub, "hub", hubDefault, "管理面地址")
	fs.StringVar(&a.tok, "token", preToken, "管理令牌（缺省读 "+tokenEnvVar+"）")
	fs.BoolVar(&a.json, "json", preJSON, "原始 JSON 输出")
	return a, fs
}

// resolveToken 旗标 → 环境变量兜底。
func (a *cliArgs) resolveToken() (string, error) {
	if a.tok != "" {
		return a.tok, nil
	}
	if v := os.Getenv(tokenEnvVar); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("缺少管理令牌：--token 或环境变量 %s", tokenEnvVar)
}

// client 登录后的 API 客户端（会话 cookie 自动携带）。
type client struct {
	hub string
	hc  *http.Client
}

// login 管理令牌登录，返回会话客户端。
func login(hub, token string) (*client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Timeout: httpTimeout, Jar: jar}
	body, _ := json.Marshal(map[string]string{"token": token})
	resp, err := hc.Post(strings.TrimSuffix(hub, "/")+"/api/v1/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("hub 不可达 %s: %w", hub, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("登录失败（HTTP %d）：核对管理令牌", resp.StatusCode)
	}
	return &client{hub: strings.TrimSuffix(hub, "/"), hc: hc}, nil
}

// call 执行一次 API 调用；非 2xx 解析 {"error","message"} 报错。
func (c *client) call(method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.hub+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		return fmt.Errorf("HTTP %d %s: %s", resp.StatusCode, e.Error, e.Message)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("应答解析失败: %w", err)
		}
	}
	return nil
}

const maxBodyBytes = 4 << 20

func dispatch(cmd string, args []string) error {
	switch cmd {
	case "submit":
		return cmdSubmit(args)
	case "query":
		return cmdQuery(args)
	case "agents":
		return cmdAgents(args)
	case "nodes":
		return cmdNodes(args)
	case "review":
		return cmdReview(args)
	case "-h", "--help", "help":
		usage()
		return nil
	}
	usage()
	return fmt.Errorf("未知命令 %q", cmd)
}

// ---------- submit ----------

func cmdSubmit(args []string) error {
	a, fs := newArgs("submit")
	node := fs.String("node", "", "目标 node_id（必填）")
	agent := fs.String("agent", "", "目标 agent 名（必填）")
	by := fs.String("by", "", "提交方标记（人/agent 名，默认 swarmmindctl）")
	idem := fs.String("idem", "", "幂等键（缺省自动生成，重试安全）")
	stdin := fs.Bool("stdin", false, "读取标准输入作为任务 stdin")
	var argList argFlags
	fs.Var(&argList, "arg", "任务参数（可重复）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *node == "" || *agent == "" {
		return errors.New("submit 需要 --node 与 --agent")
	}
	tok, err := a.resolveToken()
	if err != nil {
		return err
	}
	req := protocol.TaskSubmitRequest{
		TargetNode:  *node,
		TargetAgent: *agent,
		SubmittedBy: *by,
		Input:       protocol.TaskInput{Args: argList},
	}
	if req.SubmittedBy == "" {
		req.SubmittedBy = "swarmmindctl"
	}
	// idem 自动生成：非交互重试安全（同键重试必返回同一 task_id，SPEC 4.5）
	if *idem != "" {
		req.IdemKey = *idem
	} else {
		k, err := randomID()
		if err != nil {
			return err
		}
		req.IdemKey = "ctl-" + k
	}
	if *stdin {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("读取 stdin 失败: %w", err)
		}
		req.Input.Stdin = string(b)
	}

	c, err := login(a.hub, tok)
	if err != nil {
		return err
	}
	var ack struct {
		TaskID  string `json:"task_id"`
		TraceID string `json:"trace_id"`
		Dup     bool   `json:"dup"`
	}
	if err := c.call(http.MethodPost, "/api/v1/tasks", req, &ack); err != nil {
		return err
	}
	if a.json {
		return json.NewEncoder(os.Stdout).Encode(ack)
	}
	if ack.Dup {
		fmt.Printf("幂等命中，返回原任务。\n任务 ID: %s\n链路 ID: %s\n下一步: swarmmindctl query --id %s\n", ack.TaskID, ack.TraceID, ack.TaskID)
	} else {
		fmt.Printf("已接收，任务持久化完成。\n任务 ID: %s\n链路 ID: %s\n下一步: swarmmindctl query --id %s\n", ack.TaskID, ack.TraceID, ack.TaskID)
	}
	return nil
}

// argFlags 可重复 --arg。
type argFlags []string

func (l *argFlags) String() string { return strings.Join(*l, " ") }
func (l *argFlags) Set(v string) error {
	*l = append(*l, v)
	return nil
}

func randomID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ---------- query ----------

func cmdQuery(args []string) error {
	a, fs := newArgs("query")
	id := fs.String("id", "", "任务 ID（必填）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("query 需要 --id")
	}
	t, err := fetchTask(a, *id)
	if err != nil {
		return err
	}
	if a.json {
		return json.NewEncoder(os.Stdout).Encode(t)
	}
	printTaskDetail(t)
	return nil
}

func fetchTask(a *cliArgs, id string) (*protocol.Task, error) {
	tok, err := a.resolveToken()
	if err != nil {
		return nil, err
	}
	c, err := login(a.hub, tok)
	if err != nil {
		return nil, err
	}
	var t protocol.Task
	if err := c.call(http.MethodGet, "/api/v1/tasks/"+id, nil, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// statusHuman 技术态配人话（INTERACTION 四.5 双语格式）。
func statusHuman(s protocol.TaskStatus) string {
	switch s {
	case protocol.StatusSubmitted:
		return "已接收待派发 · submitted"
	case protocol.StatusOffered:
		return "已派发待确认 · offered"
	case protocol.StatusAcknowledged:
		return "已接收 · acknowledged"
	case protocol.StatusRunning:
		return "执行中 · running"
	case protocol.StatusCompleted:
		return "已完成 · completed"
	case protocol.StatusFailed:
		return "已失败 · failed"
	case protocol.StatusNeedsReview:
		return "待人工核对 · needs_review"
	case protocol.StatusCancelled:
		return "已取消 · cancelled"
	}
	return string(s)
}

// printTaskDetail 任务详情人话输出（query 终态展示 FinalAnswer/Artifacts/
// needs_review 说明，INTERACTION 规则 5；任何终态带「下一步」指引）。
func printTaskDetail(t *protocol.Task) {
	fmt.Printf("任务 %s\n", t.ID)
	fmt.Printf("状态: %s\n", statusHuman(t.Status))
	fmt.Printf("目标: %s / %s\n", t.TargetNode, t.TargetAgent)
	fmt.Printf("提交方: %s    创建: %s\n", t.SubmittedBy, fmtTime(t.CreatedAt))
	if t.Result == nil {
		fmt.Printf("\n尚无终态结果（任务在途或排队中）。\n下一步: 稍后重查 swarmmindctl query --id %s\n", t.ID)
		return
	}
	r := t.Result
	fmt.Printf("退出码: %d\n", r.ExitCode)
	if r.Error != "" {
		fmt.Printf("错误: %s\n", r.Error)
	}
	switch t.Status {
	case protocol.StatusNeedsReview:
		fmt.Printf("\n执行到一半机器重启了，结果不确定——等待本机主人核对，请勿重新提交。\n")
	case protocol.StatusCancelled:
		fmt.Printf("\n任务已被取消（进程已整组终止）；以下为终止前采集的输出，不确定是否完整。\n")
	case protocol.StatusCompleted:
		fmt.Printf("\n最终答复 (FinalAnswer):\n%s\n", withIndent(r.FinalAnswer))
	default:
		if r.FinalAnswer != "" {
			fmt.Printf("\n最终答复 (FinalAnswer):\n%s\n", withIndent(r.FinalAnswer))
		}
	}
	if r.StdoutTail != "" && t.Status != protocol.StatusCompleted {
		fmt.Printf("stdout 尾部:\n%s\n", withIndent(r.StdoutTail))
	}
	if r.StderrTail != "" {
		fmt.Printf("stderr 尾部:\n%s\n", withIndent(r.StderrTail))
	}
	if len(r.Artifacts) == 0 {
		fmt.Printf("产物: 无\n")
	} else {
		fmt.Printf("产物 %d 项（取回: GET /api/v1/tasks/%s/artifacts/{index}）:\n", len(r.Artifacts), t.ID)
		for i, art := range r.Artifacts {
			fmt.Printf("  [%d] %s  sha256=%s  %d 字节\n", i, art.Path, shortSha(art.Sha256), art.Size)
		}
	}
	fmt.Printf("\n下一步: %s\n", nextStep(t))
}

// nextStep 终态「下一步」指引（INTERACTION 四.2）。
func nextStep(t *protocol.Task) string {
	switch t.Status {
	case protocol.StatusNeedsReview:
		return "本机主人核对后处理（V0 核对记录持久化属 P3）；查询: swarmmindctl review --id " + t.ID
	case protocol.StatusCompleted:
		return "取回产物或复用结果；同类任务: swarmmindctl submit"
	case protocol.StatusFailed:
		if t.Result != nil && t.Result.Error == protocol.CodeExpired {
			return "任务已过期，请重新提交（swarmmindctl submit）"
		}
		return "排查错误后重试（swarmmindctl submit，携同 --idem 可追溯）"
	case protocol.StatusCancelled:
		return "任务已被取消，不会自动重跑；确有需要请重新提交（swarmmindctl submit，新幂等键）"
	}
	return "稍后重查: swarmmindctl query --id " + t.ID
}

// withIndent 多行文本缩进两格（详情可读性）。
func withIndent(s string) string {
	if s == "" {
		return "(空)"
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, ln := range lines {
		lines[i] = "  " + ln
	}
	return strings.Join(lines, "\n")
}

func shortSha(sha string) string {
	if len(sha) > 12 {
		return sha[:12] + "…"
	}
	return sha
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

// ---------- agents / nodes ----------

func cmdAgents(args []string) error {
	a, fs := newArgs("agents")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tok, err := a.resolveToken()
	if err != nil {
		return err
	}
	c, err := login(a.hub, tok)
	if err != nil {
		return err
	}
	var resp struct {
		Agents []struct {
			Card   protocol.AgentCard `json:"card"`
			NodeID string             `json:"node_id"`
			Online bool               `json:"online"`
		} `json:"agents"`
	}
	if err := c.call(http.MethodGet, "/api/v1/agents", nil, &resp); err != nil {
		return err
	}
	if a.json {
		return json.NewEncoder(os.Stdout).Encode(resp)
	}
	sort.Slice(resp.Agents, func(i, j int) bool {
		if resp.Agents[i].NodeID != resp.Agents[j].NodeID {
			return resp.Agents[i].NodeID < resp.Agents[j].NodeID
		}
		return resp.Agents[i].Card.Name < resp.Agents[j].Card.Name
	})
	fmt.Printf("%-16s %-16s %-6s %-8s %-8s %-14s %s\n", "NODE", "AGENT", "KIND", "EXPOSED", "ONLINE", "PERMISSION", "DESCRIPTION")
	for _, e := range resp.Agents {
		fmt.Printf("%-16s %-16s %-6s %-8s %-8s %-14s %s\n",
			e.NodeID, e.Card.Name, e.Card.Kind, onOff(e.Card.Exposed), onOff(e.Online), e.Card.PermissionMode, e.Card.Description)
	}
	fmt.Printf("\n共 %d 个 agent。暴露=可被派单（exposed），在线=node 已连上 hub。\n", len(resp.Agents))
	return nil
}

func cmdNodes(args []string) error {
	a, fs := newArgs("nodes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tok, err := a.resolveToken()
	if err != nil {
		return err
	}
	c, err := login(a.hub, tok)
	if err != nil {
		return err
	}
	var resp struct {
		Nodes []struct {
			NodeID   string `json:"node_id"`
			Online   bool   `json:"online"`
			Epoch    int64  `json:"epoch"`
			Inflight int    `json:"inflight"`
		} `json:"nodes"`
	}
	if err := c.call(http.MethodGet, "/api/v1/nodes", nil, &resp); err != nil {
		return err
	}
	if a.json {
		return json.NewEncoder(os.Stdout).Encode(resp)
	}
	sort.Slice(resp.Nodes, func(i, j int) bool { return resp.Nodes[i].NodeID < resp.Nodes[j].NodeID })
	fmt.Printf("%-24s %-8s %-8s %s\n", "NODE", "ONLINE", "EPOCH", "INFLIGHT")
	for _, n := range resp.Nodes {
		fmt.Printf("%-24s %-8s %-8d %d\n", n.NodeID, onOff(n.Online), n.Epoch, n.Inflight)
	}
	fmt.Printf("\n共 %d 个 node。epoch=注册代次（每次重连 +1），inflight=已派发待执行数。\n", len(resp.Nodes))
	return nil
}

func onOff(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

// ---------- review（V0 只读展示，INTERACTION/SPEC 六） ----------

func cmdReview(args []string) error {
	a, fs := newArgs("review")
	id := fs.String("id", "", "任务 ID（必填）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("review 需要 --id")
	}
	t, err := fetchTask(a, *id)
	if err != nil {
		return err
	}
	if a.json {
		return json.NewEncoder(os.Stdout).Encode(t)
	}

	fmt.Printf("任务 %s\n", t.ID)
	if t.Status != protocol.StatusNeedsReview {
		fmt.Printf("状态: %s\n该任务无需人工核对。\n", statusHuman(t.Status))
		return nil
	}
	// needs_review 详情第一句 = 固定人话（INTERACTION 四.5）
	fmt.Printf("\n执行到一半机器重启了，结果不确定——等待本机主人核对，请勿重新提交。\n\n")
	fmt.Printf("待核对上下文:\n")
	fmt.Printf("  目标: %s / %s\n", t.TargetNode, t.TargetAgent)
	fmt.Printf("  提交方: %s    创建: %s\n", t.SubmittedBy, fmtTime(t.CreatedAt))
	if t.Result != nil {
		fmt.Printf("  复核原因: %s\n", t.Result.Error)
		fmt.Printf("  开始: %s    标记终态: %s\n", fmtTime(t.Result.StartedAt), fmtTime(t.Result.FinishedAt))
	}
	fmt.Printf("  任务输入: args=%v stdin=%d 字节 env=%v\n", t.Input.Args, len(t.Input.Stdin), envKeys(t.Input.Env))
	if t.Review != nil {
		// 核对记录（SPEC 六.五）：管理面登记，协议终态仍为 needs_review
		fmt.Printf("\n核对记录: %s · %s · %s", t.Review.Action, t.Review.By, fmtTime(t.Review.At))
		if t.Review.Note != "" {
			fmt.Printf(" · %s", t.Review.Note)
		}
		fmt.Printf("\n")
	}

	fmt.Printf("\n处理指引:\n")
	fmt.Printf("  1. 到执行该任务的 node 本机核对：任务账本 data_dir/ledger.json 与产物目录 workdir/outputs/%s/\n", t.ID)
	fmt.Printf("  2. 判断进程实际是否产生了副作用（写文件/外发等）与结果是否可用\n")
	fmt.Printf("  3. 请勿直接重新提交同语义任务——副作用可能重复发生；确需重跑时先清理上一轮产物\n")
	if t.Result != nil && len(t.Result.Artifacts) > 0 {
		fmt.Printf("  4. 已登记产物 %d 项（可取回核对）:\n", len(t.Result.Artifacts))
		for i, art := range t.Result.Artifacts {
			fmt.Printf("     [%d] %s  sha256=%s  %d 字节\n", i, art.Path, shortSha(art.Sha256), art.Size)
		}
	}
	fmt.Printf("\n下一步: 核对完成后，如需重跑请用新的 --idem 提交（swarmmindctl submit）\n")
	return nil
}

func envKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
