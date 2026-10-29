package sessionx

// zcodeAdapter zcode headless 适配器：命令形态与整份 JSON 解析严格对齐本机探针
//（docs/evidence/native-session-20261029/zcode-r1|r2，runtime 0.17.0），
// 不得凭想象改写：
//
//	首轮：zcode --cwd <dir> --mode <mode> --json -p <prompt>
//	续轮：zcode --cwd <dir> --mode <mode> --json --resume <session_id> -p <prompt>
//
// 输出为整份终态 JSON（非实时事件流），取 sessionId/turnId/response/usage；
// projection.turnCount 恒 1（两轮探针实证），显式不读——累计轮数口径 =
// Session.TurnCount 平台侧自计。

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// zcodeDefaultPermission 保守默认模式：edit（探针唯一实测档位）。edit 可写文件但
// Bash 无权限客户端（headless 无权限应答，探针第二轮实证）——绝不默认 yolo；
// 提权档位由调用方显式配置（探针适配决策 4）。
const zcodeDefaultPermission = "edit"

// zcodeAdapter zcode headless 命令拼装与整份 JSON 解析。
type zcodeAdapter struct {
	command string // CLI 绝对路径
	mode    string // --mode 值（PermissionMode 映射）
}

func (a zcodeAdapter) kind() string { return protocol.SessionAdapterZcode }

func (a zcodeAdapter) argv(first bool, cwd, expectID, prompt string) []string {
	base := []string{"--cwd", cwd, "--mode", a.mode, "--json"}
	if first {
		return append(base, "-p", prompt)
	}
	return append(append(base, "--resume", expectID), "-p", prompt)
}

// zcodeResult 整份终态 JSON（探针样本字段）。必填字段以指针声明（R1 审查 R5）：
// 指针为 nil = 字段缺失或显式 null，与「合法空值」可区分，缺失判失败不得当完整
// 成功；类型不符由 json.Unmarshal 整体报错。usage 为可选字段：可缺省（零值），
// 出现时必须是对象。
type zcodeResult struct {
	SessionID *string `json:"sessionId"`
	TurnID    *string `json:"turnId"`
	Response  *string `json:"response"`
	Usage     *struct {
		InputTokens  int64 `json:"inputTokens"`
		OutputTokens int64 `json:"outputTokens"`
		TotalTokens  int64 `json:"totalTokens"`
	} `json:"usage"`
	// Projection 结构存在但禁用：turnCount 恒 1 不可当累计轮数（探针报告明示），
	// 编译期占位防误用。
	Projection struct {
		TurnCount int `json:"turnCount"`
	} `json:"projection"`
}

// zcode 必填字段（sessionId/turnId/response）存在性 + 取值边界：
//   - sessionId 必须在场且非空（平台↔原生映射的键，空值无从建映射）；
//   - turnId 必须在场且非空（原生轮次标识，缺席=结构损坏）；
//   - response 必须在场（字符串类型即可；空串视为「合法空值」——本轮无正文不算
//     结构损坏，工作成败另见 WorkResult）。
func (a zcodeAdapter) parse(stdout []byte) (TurnOutput, error) {
	var r zcodeResult
	if err := json.Unmarshal(stdout, &r); err != nil {
		return TurnOutput{}, fmt.Errorf("非整份 JSON 或字段类型不符: %w", err)
	}
	if r.SessionID == nil {
		return TurnOutput{}, errors.New("sessionId 字段缺失")
	}
	if *r.SessionID == "" {
		return TurnOutput{}, errors.New("sessionId 字段为空")
	}
	if r.TurnID == nil {
		return TurnOutput{}, errors.New("turnId 字段缺失")
	}
	if *r.TurnID == "" {
		return TurnOutput{}, errors.New("turnId 字段为空")
	}
	if r.Response == nil {
		return TurnOutput{}, errors.New("response 字段缺失")
	}
	usage := protocol.TurnUsage{}
	if r.Usage != nil {
		usage = protocol.TurnUsage{
			InputTokens:  r.Usage.InputTokens,
			OutputTokens: r.Usage.OutputTokens,
			TotalTokens:  r.Usage.TotalTokens,
		}
	}
	_ = r.Projection.TurnCount // 显式不用：恒 1（防误用，见 zcodeResult 注释）
	return TurnOutput{
		NativeID:           *r.SessionID,
		NativeTurnID:       *r.TurnID,
		Response:           sanitizeLimit(*r.Response, protocol.FinalAnswerLimit),
		Usage:              usage,
		nativeTurnComplete: true, // 整份 JSON 即终态
	}, nil
}

// nativeTurnComplete 原生轮次完成判据（四路分离的第三路）：整份终态 JSON 可解析
// 即原生轮次终态在场。它不证明工作成功（第四路恒 unverified）。
func (a zcodeAdapter) nativeTurnComplete(TurnOutput) error { return nil }
