package sessionx

// codexAdapter codex exec 适配器：命令形态与 JSONL 事件流解析严格对齐本机探针
//（docs/evidence/native-session-20261029/codex-r1|r2.jsonl，codex-cli 0.164.0），
// 不得凭想象改写：
//
//	首轮：codex exec --json --skip-git-repo-check -s <sandbox> -c 'approval_policy="never"' -C <cwd> <prompt>
//	续轮：…同前缀… -C <cwd> resume <thread_id> <prompt>
//
// exec 通用参数置于 resume 子命令之前（探针已验证）。解析取 thread.started /
// item / turn.completed / 错误事件；item.completed 的 agent_message 文本按序拼接
// 为答复正文（探针 r2 一轮内两条 agent_message 实证）。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// codexDefaultPermission 保守默认沙箱：只读（-s 取值，codex 标准档位）。绝不默认
// 全权；workspace-write 等提权档位由调用方显式配置。
const codexDefaultPermission = "read-only"

// codexApprovalPolicy 恒 never：exec 非交互无审批应答通道，探针形态即此值；
// 与沙箱档位分离固定，不随 PermissionMode 联动提权。
const codexApprovalPolicy = "never"

// codexAdapter codex exec 命令拼装与 JSONL 解析。
type codexAdapter struct {
	command string // CLI 绝对路径
	sandbox string // -s 沙箱档位（PermissionMode 映射）
}

func (a codexAdapter) kind() string { return protocol.SessionAdapterCodex }

func (a codexAdapter) argv(first bool, cwd, expectID, prompt string) []string {
	base := []string{
		"exec", "--json", "--skip-git-repo-check",
		"-s", a.sandbox,
		"-c", `approval_policy="` + codexApprovalPolicy + `"`,
		"-C", cwd,
	}
	if first {
		return append(base, prompt)
	}
	return append(append(base, "resume", expectID), prompt)
}

// codexEvent JSONL 单行事件（字段名以探针样本为准）。事件处理纪律（R1 审查 S2、
// R2 复审 S2 收紧）：未知事件类型可静默忽略（CLI 演进加新事件不应击穿旧解析）；
// 已知事件的载荷损坏必须显式报错——item.started/item.completed 的 item 缺失、null、
// 非对象、缺 type，以及 agent_message 缺 text，均为结构损坏，不得零值化后静默
// 吞掉冒充完整成功。
type codexEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id"`
	Item     json.RawMessage `json:"item"`
	Message  string          `json:"message"`
	Error    json.RawMessage `json:"error"`
	Usage    *codexUsage     `json:"usage"`
}

// codexItem item 事件载荷（探针样本字段）。Type 必须在场且非空（已知事件的必要性
// 校验项）；agent_message 的 Text 以指针声明（R2 复审 S2，对齐 zcodeResult 纪律）：
// 指针 nil = 字段缺失或显式 null，与「合法空串」可区分——缺 text 判载荷损坏，
// text="" 合法（本轮无正文，不进答复拼接）。id 不做存在性强制（探针未确立其为
// 必填；未知 item 类型以 type 非空为兼容边界）。
type codexItem struct {
	ID   string  `json:"id"`
	Type string  `json:"type"`
	Text *string `json:"text"`
}

// codexUsage turn.completed 用量（探针样本字段；cached 含于 input 口径，
// TotalTokens 由 input+output 派生——codex 无独立总数字段）。
type codexUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

func (a codexAdapter) parse(stdout []byte) (TurnOutput, error) {
	var out TurnOutput
	var msgs []string
	complete := false
	for i, line := range bytes.Split(stdout, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var ev codexEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return out, fmt.Errorf("第 %d 行非 JSON: %w", i+1, err)
		}
		switch ev.Type {
		case "thread.started":
			// 冲突原生 ID 拒绝（S2）：同一事件流出现两个不同 thread id = 结构损坏
			// 或线程漂移，保留第一个并继续会掩盖损坏。空 id 占位事件忽略。
			if ev.ThreadID == "" {
				continue
			}
			if out.NativeID != "" && ev.ThreadID != out.NativeID {
				return out, fmt.Errorf("native_id_conflict: 事件流出现冲突原生 ID（已取 %s，又见 %s），拒绝歧义线程", out.NativeID, ev.ThreadID)
			}
			if out.NativeID == "" {
				out.NativeID = ev.ThreadID
			}
		case "item.started", "item.completed":
			// 已知事件载荷损坏显式报错（R2 复审 S2）：item 必须是有效对象且 type
			// 非空——JSON null 解码到 struct 不报错、空对象成零值，都不得静默放行
			// 冒充完整轮次；agent_message 缺 text 或 null 同为损坏。未知事件类型
			// 可忽略、未知合法 item 类型（type 非空）兼容放行。
			var itp *codexItem
			if err := json.Unmarshal(ev.Item, &itp); err != nil {
				return out, fmt.Errorf("第 %d 行 %s 载荷损坏（item 不可解码）: %w", i+1, ev.Type, err)
			}
			if itp == nil {
				return out, fmt.Errorf("第 %d 行 %s 载荷损坏（item 为 null）", i+1, ev.Type)
			}
			if itp.Type == "" {
				return out, fmt.Errorf("第 %d 行 %s 载荷损坏（item 缺 type）", i+1, ev.Type)
			}
			if itp.Type == "agent_message" && itp.Text == nil {
				return out, fmt.Errorf("第 %d 行 %s 载荷损坏（agent_message 缺 text）", i+1, ev.Type)
			}
			if ev.Type == "item.completed" && itp.Type == "agent_message" && *itp.Text != "" {
				msgs = append(msgs, *itp.Text)
			}
		case "turn.completed":
			complete = true
			if ev.Usage != nil {
				out.Usage = protocol.TurnUsage{
					InputTokens:  ev.Usage.InputTokens,
					OutputTokens: ev.Usage.OutputTokens,
					TotalTokens:  ev.Usage.InputTokens + ev.Usage.OutputTokens,
				}
			}
		case "error", "turn.failed":
			msg := ev.Message
			if msg == "" && len(ev.Error) > 0 {
				msg = string(ev.Error)
			}
			if msg == "" {
				msg = "未知错误事件"
			}
			return out, fmt.Errorf("cli_error_event: %s", msg)
		}
	}
	out.Response = sanitizeLimit(strings.Join(msgs, "\n\n"), protocol.FinalAnswerLimit)
	out.nativeTurnComplete = complete
	return out, nil
}

// nativeTurnComplete 原生轮次完成判据（四路分离的第三路）：事件流必须见过
// turn.completed——它只证明本轮 CLI 侧已终态，不证明工作成功（第四路恒 unverified）。
func (a codexAdapter) nativeTurnComplete(out TurnOutput) error {
	if !out.nativeTurnComplete {
		return errors.New("turn_incomplete: 退出码 0 且解析成功，但事件流未见 turn.completed，原生轮次完成判据不成立")
	}
	return nil
}
