package sessionx

// 适配器公共层：一轮 CLI 的执行与四路分离判定（TASK-agent-session-core 改动范围 3
// 与 DoD 6 失败语义附加要求）。四路独立建模（R1 审查 R6 修订）：①进程退出码 ②输出
// 结构解析 ③原生轮次完成（codex=turn.completed 事件 / zcode=整份终态 JSON）④工作
// 是否成功——前三路由 Turn.Status 判定，第四路 T1 内核无法从 CLI 输出证明（两家退出
// 码 0 都不等于工作成功，探针 zcode-r1 正文明说 Bash 无权限工作未完成而三路全过），
// 恒记 protocol.WorkUnverified，禁止文本关键词猜测；真实答复原样保留供后续验收。
//
// 进程治理复用 runner 的单一真相源（TASK-agent-session-core 改动范围 4）：经
// runner 导出薄包装调用同一实现，本包零复制（R1 审查 R7 修订）。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/mtl802/swarmmind/internal/node/runner"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// turnOutputLimit 单轮 stdout 解析缓冲字节上限：超过即判解析失败（turn_output_exceeded），
// 防异常 CLI 撑爆内存。正常一轮 JSONL/整份 JSON 远小于此。
const turnOutputLimit = 8 << 20

// turnStderrTail 失败错误消息携带的 stderr 摘录上限。
const turnStderrTail = 2 << 10

// turnWaitDelay 超时/取消后等待管道排干的兜底时长（对齐 runner waitDelay=5s）。
const turnWaitDelay = 5 * time.Second

// adapter 双端 CLI 适配器接口：命令拼装与输出解析封装首/续轮差异（探针已验证形态
// 见 adapter_codex.go / adapter_zcode.go 顶部注释，不得凭想象改写）。
type adapter interface {
	// kind 适配器类型标识（protocol.SessionAdapterXxx）。
	kind() string
	// argv 按探针形态拼装命令行。first=true 首轮；否则 expectID 为存储的原生 ID。
	argv(first bool, cwd, expectID, prompt string) []string
	// parse 解析本轮 stdout 为结构化产物；输出非法（非 JSON/必填字段缺失或类型
	// 不符）返回错误。
	parse(stdout []byte) (TurnOutput, error)
	// nativeTurnComplete 原生轮次完成判据（四路分离的第三路）：事件流未见
	// turn.completed / 终态 JSON 不可得时返回错误。它只证明本轮 CLI 侧已终态，
	// 不证明工作成功（第四路恒 unverified，见包注释）。
	nativeTurnComplete(out TurnOutput) error
}

// TurnOutput 适配器解析产物（一轮的结构化输出）。
type TurnOutput struct {
	// NativeID 本轮回带的原生会话 ID（codex thread.started / zcode sessionId）。
	NativeID string
	// NativeTurnID 原生轮次 ID（zcode turnId；codex 事件流无独立轮次 ID，为空）。
	NativeTurnID string
	// Response 答复正文（UTF-8 清洗并截断至 FinalAnswerLimit）。
	Response string
	// Usage token 用量摘要（解析不出时零值）。
	Usage protocol.TurnUsage
	// nativeTurnComplete 原生轮次完成持有位：codex=turn.completed 已见；zcode 恒真
	//（整份 JSON 即终态）。由 parse 置位、nativeTurnComplete() 判定；非工作成功声明。
	nativeTurnComplete bool
}

// newAdapter 按配置构建适配器实例（无状态，可并发复用）。
func newAdapter(spec AgentSpec) (adapter, error) {
	switch spec.Adapter {
	case protocol.SessionAdapterCodex:
		return codexAdapter{command: spec.Command, sandbox: spec.PermissionMode}, nil
	case protocol.SessionAdapterZcode:
		return zcodeAdapter{command: spec.Command, mode: spec.PermissionMode}, nil
	default:
		return nil, fmt.Errorf("适配器类型未知: %q", spec.Adapter)
	}
}

// runAdapterTurn 执行一轮 CLI 并完成判定链：①进程退出码 → ②输出解析 → 原生 ID
// 核验 → ③原生轮次完成。第四路（工作成功）不在本函数判定——Turn.WorkResult 恒
// unverified（见包注释）。expectID 为空表示首轮（要求回带非空原生 ID，否则会话
// 无从建立）；续轮回带缺失或不一致判失败且不触碰映射（错误消息含双方 ID，绝不
// 静默换线程）。返回的 TurnOutput 在失败路径也可能部分填充（如 mismatch 时新线程
// 的答复），仅供留痕诊断。turn 由调用方预构造（Seq/StartedAt，时钟源归 Manager），
// 本函数补齐其余字段。方法落 Manager 上是为了统一走注入时钟（m.now），不触任何
// 受锁状态。
func (m *Manager) runAdapterTurn(ctx context.Context, ad adapter, spec AgentSpec, cwd, expectID, prompt string, first bool, turn *protocol.Turn) (TurnOutput, error) {
	// 第四路（工作成功）T1 无判定通道：任何轮次（含成功与失败）恒 unverified，
	// 由验收链路（T2+）显式改判；此处绝不从答复正文猜测。
	turn.WorkResult = protocol.WorkUnverified
	res := runGoverned(ctx, spec, cwd, ad.argv(first, cwd, expectID, prompt))
	turn.ExitCode = res.exitCode
	turn.FinishedAt = m.now()
	if res.err != nil {
		turn.Status = protocol.TurnFailed
		turn.Error = res.err.Error()
		return TurnOutput{}, res.err
	}
	if res.overflow {
		err := fmt.Errorf("turn_output_exceeded: stdout 超过解析上限 %d 字节，拒绝解析", turnOutputLimit)
		turn.Status = protocol.TurnFailed
		turn.Error = err.Error()
		return TurnOutput{}, err
	}
	out, perr := ad.parse(res.stdout)
	turn.Response = out.Response
	turn.NativeTurnID = out.NativeTurnID
	turn.Usage = out.Usage
	if perr != nil {
		err := fmt.Errorf("output_parse_failed: %w", perr)
		turn.Status = protocol.TurnFailed
		turn.Error = err.Error()
		return out, err
	}
	if first {
		if out.NativeID == "" {
			err := errors.New("native_id_missing: 首轮输出未携带原生会话 ID（codex=thread.started / zcode=sessionId），会话不建立")
			turn.Status = protocol.TurnFailed
			turn.Error = err.Error()
			return out, err
		}
	} else {
		if out.NativeID == "" {
			err := errors.New("native_id_missing: 续轮输出未回带原生会话 ID，无法核验一致性，判失败（映射不动）")
			turn.Status = protocol.TurnFailed
			turn.Error = err.Error()
			return out, err
		}
		if out.NativeID != expectID {
			err := fmt.Errorf("native_id_mismatch: 存储 %s，回带 %s；已拒绝换线程，映射保持不变", expectID, out.NativeID)
			turn.Status = protocol.TurnFailed
			turn.Error = err.Error()
			return out, err
		}
	}
	if nerr := ad.nativeTurnComplete(out); nerr != nil {
		turn.Status = protocol.TurnFailed
		turn.Error = nerr.Error()
		return out, nerr
	}
	// TurnOK 仅声明三路成立（退出码/解析/原生轮次完成），不含工作成功声明——
	// 工作结果见 Turn.WorkResult（恒 unverified）。
	turn.Status = protocol.TurnOK
	return out, nil
}

// governedResult 单轮进程执行结果（三路判定前段：退出码与采集物）。
type governedResult struct {
	exitCode int
	stdout   []byte
	stderr   []byte
	overflow bool
	timedOut bool
	err      error
}

// runGoverned 每轮进程治理与采集（复用 runner 治理实现，SPEC 五）：spawn 前置
// 独立进程组（Unix setpgid / Windows CREATE_SUSPENDED+Job），唯一超时源是 ctx
// （AgentSpec.TimeoutSec 从它派生），取消/超时整组终止，返回前进程必被 Wait 且
// 整组收尾——Turn 返回后工作目录再无组内写入者。kill 后必须 Wait（CLAUDE.md）。
// afterStart 传 nil：sessionx 无 spawn 期回调需求（runner 原语义不变）。
func runGoverned(ctx context.Context, spec AgentSpec, dir string, argv []string) governedResult {
	res := governedResult{exitCode: protocol.ExitCodeNotRun}
	if spec.TimeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutSec)*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, spec.Command, argv...)
	cmd.Dir = dir
	if len(spec.Env) > 0 {
		cmd.Env = spec.Env // 整体替换；空 = 继承（T2 接线时由调用方给 SPEC 五干净基线）
	}
	if err := runner.ConfigureProcessGroup(cmd); err != nil {
		res.err = fmt.Errorf("process_governance_failed: %w", err)
		return res
	}
	cmd.Cancel = func() error { return runner.KillProcessGroup(cmd) }
	cmd.WaitDelay = turnWaitDelay
	stdout := &capBuffer{limit: turnOutputLimit}
	stderr := &tailBuffer{limit: turnStderrTail}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := runner.LaunchGoverned(cmd, nil)
	res.timedOut = ctx.Err() != nil // Cancel 路径下 Wait 可能回传信号错误，以 ctx 为准
	if cmd.ProcessState != nil {
		res.exitCode = cmd.ProcessState.ExitCode() // 信号终止/未运行 = -1
	}
	if cmd.Process != nil && cmd.ProcessState != nil {
		runner.ReapProcessGroup(cmd) // 整组收尾（含正常返回路径）：目录再无组内写入者
	}
	res.stdout = stdout.bytes()
	res.stderr = stderr.bytes()
	res.overflow = stdout.overflow

	switch {
	case res.timedOut:
		// 超时/调用方取消：本轮失败，如实标注进程已终止、原生侧状态不确定
		//（TASK-agent-session-core 改动范围 3）。
		res.err = fmt.Errorf("%s: context 已取消或到期，进程已整组终止，原生侧状态不确定", protocol.CodeTimeout)
	case runErr != nil:
		if cmd.ProcessState == nil {
			// 未成功 spawn（命令不存在/工作目录缺失等）
			res.err = fmt.Errorf("spawn_failed: %w", runErr)
			return res
		}
		// 退出码非 0：failed（退出码与解析分离——不做解析挽救，原生侧状态不确定）
		res.err = fmt.Errorf("exit_code=%d: %s", res.exitCode, firstLine(res.stderr))
	}
	return res
}

// firstLine 取 stderr 首个非空行（诊断摘要；已按 turnStderrTail 截尾）。
func firstLine(b []byte) string {
	for _, l := range bytes.Split(b, []byte("\n")) {
		l = bytes.TrimSpace(l)
		if len(l) > 0 {
			return string(l)
		}
	}
	return "（无 stderr 输出）"
}

// capBuffer 满额即停止累积并标记溢出；Write 恒成功（返回错误会让 exec 的拷贝协程
// 中断并以 Write 错误冒充进程错误，污染判定）。
type capBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	if b.buf.Len() >= b.limit {
		b.overflow = true
		return len(p), nil
	}
	if b.buf.Len()+len(p) > b.limit {
		b.buf.Write(p[:b.limit-b.buf.Len()])
		b.overflow = true
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *capBuffer) bytes() []byte { return b.buf.Bytes() }

// tailBuffer 只留末尾 limit 字节（诊断摘要）。
type tailBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf.Write(p)
	if b.buf.Len() > b.limit {
		keep := b.buf.Bytes()[b.buf.Len()-b.limit:]
		next := make([]byte, len(keep))
		copy(next, keep)
		b.buf.Reset()
		b.buf.Write(next)
	}
	return len(p), nil
}

func (b *tailBuffer) bytes() []byte { return b.buf.Bytes() }
