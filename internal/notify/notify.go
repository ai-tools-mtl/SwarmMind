// Package notify 任务终态通知（PRODUCT-DIRECTION v2 P4③ 及其控制台化升级）：
// 任务首次落终态时，Dispatcher 把通知路由到全部启用且订阅该事件的渠道
// （notify_channels.json，管理台「设置 → 通知」可视化配置）——首批支持飞书
// 自定义机器人、email（SMTP）、通用 webhook（JSON POST）、inbox（站内=控制台
// 通知中心）；sms 占位暂未开放。
//
// 可靠性边界（设计定稿）：发送在独立 goroutine 进行，不阻塞状态机持久化；
// 单渠道单次 5s 超时；渠道级失败独立重试 3 次指数退避（1s/2s/4s），穷尽只记
// 日志（凭证与 webhook URL query 脱敏），绝不影响任务终态与主流程。去重不在
// 本包——触发点（core 终态旁路）只在非终态→终态首次转换时调用一次（重复
// 结果/冲突/清扫重扫均不触发），Sweep/重放天然不产生重复通知。
//
// 装配位置（2026-10-24 自 internal/hub/notify 提升，channel/dispatch/store 为
// hub/node 通用组件）：hub 组网模式由 core 终态旁路触发；node standalone 模式
// （hub_url 为空，hub 不存在）由 node service 终态落账本触发补盲区——networked
// 模式通知归 hub，node 不装配（零调用零重复）。inbox 仅 hub 控制台装配（铃铛
// 专用，见 inbox.go）；node 侧 Dispatcher 以 nil inbox 装配，inbox 渠道类型在
// node 管理面准入即拒绝。
//
// 可靠性边界（设计定稿）：发送在独立 goroutine 进行，不阻塞状态机持久化；
// 单渠道单次 5s 超时；渠道级失败独立重试 3 次指数退避（1s/2s/4s），穷尽只记
// 日志（凭证与 webhook URL query 脱敏），绝不影响任务终态与主流程。去重不在
// 本包——触发点（core 终态旁路 / node service 落终态）只在非终态→终态首次
// 转换时调用一次（重复结果/冲突/清扫重扫均不触发），重放天然不产生重复通知。
//
// 兼容性：hub.yaml 旧 notify 段（webhook_url+format）启动时一次性导入为一个
// 渠道（幂等，见 store.go ImportLegacy，hub 专用）；旧 Format 常量保留用于
// 导入口径。
package notify

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// 可靠性参数（设计定稿）：单次请求 5s 超时；失败重试 3 次，退避 1s/2s/4s。
const (
	defaultTimeout = 5 * time.Second
	retryBackoff   = 3
)

var defaultBackoff = [retryBackoff]time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// summaryMaxLen 通知摘要截取长度（FinalAnswer 前 200 字符，按 rune 计）。
const summaryMaxLen = 200

// Format 取值（hub.yaml 旧 notify.format 的兼容口径；导入渠道时 raw→webhook
// 类型、feishu→feishu 类型）。
const (
	FormatRaw    = "raw"
	FormatFeishu = "feishu"
)

// terminalPayload webhook（raw）格式通知载荷（设计定稿字段表；缺失字段
// omitempty 省略）。
type terminalPayload struct {
	Event       string `json:"event"`
	TaskID      string `json:"task_id"`
	Status      string `json:"status"`
	NodeID      string `json:"node_id,omitempty"`
	Agent       string `json:"agent,omitempty"`
	SubmittedBy string `json:"submitted_by,omitempty"`
	DurationSec *int64 `json:"duration_sec,omitempty"`
	Error       string `json:"error,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

// durationSec 任务耗时秒数（UpdatedAt-CreatedAt；CreatedAt 缺失不可计=nil 省略）。
func durationSec(t protocol.Task) *int64 {
	if t.CreatedAt.IsZero() {
		return nil
	}
	d := int64(t.UpdatedAt.Sub(t.CreatedAt).Seconds())
	return &d
}

func resultError(r *protocol.TaskResult) string {
	if r == nil {
		return ""
	}
	return r.Error
}

// summary FinalAnswer 前 200 字符（rune 计，不切断多字节字符）；无结果=省略。
func summary(r *protocol.TaskResult) string {
	if r == nil || r.FinalAnswer == "" {
		return ""
	}
	runes := []rune(r.FinalAnswer)
	if len(runes) > summaryMaxLen {
		runes = runes[:summaryMaxLen]
	}
	return string(runes)
}

// feishuMessage 飞书自定义机器人文本消息格式（设计定稿）。
type feishuMessage struct {
	MsgType string        `json:"msg_type"`
	Content feishuContent `json:"content"`
}

type feishuContent struct {
	Text string `json:"text"`
}

// feishuText 一行人话摘要（设计文案）：完成「✅ 任务完成 t-x · node/agent ·
// 耗时 3m20s」；失败附原因；needs_review 等人工确认；取消同口径。字段缺失
// （无节点/无耗时/无原因）对应片段整体省略。
func feishuText(t protocol.Task) string {
	var b strings.Builder
	switch t.Status {
	case protocol.StatusCompleted:
		b.WriteString("✅ 任务完成 ")
	case protocol.StatusFailed:
		b.WriteString("❌ 任务失败 ")
	case protocol.StatusNeedsReview:
		b.WriteString("⚠️ 任务结果拿不准，等你确认 ")
	case protocol.StatusCancelled:
		b.WriteString("🚫 任务已取消 ")
	default:
		b.WriteString("任务终态 ")
	}
	b.WriteString(t.ID)
	if t.TargetNode != "" || t.TargetAgent != "" {
		b.WriteString(" · " + t.TargetNode + "/" + t.TargetAgent)
	}
	if !t.CreatedAt.IsZero() {
		b.WriteString(" · 耗时 " + durationText(t))
	}
	if t.Status == protocol.StatusFailed && t.Result != nil && t.Result.Error != "" {
		b.WriteString("（原因：" + t.Result.Error + "）")
	}
	return b.String()
}

// durationText 人话时长（Truncate 到秒：3m20s，不出小数）。
func durationText(t protocol.Task) string {
	return t.UpdatedAt.Sub(t.CreatedAt).Truncate(time.Second).String()
}

// mustJSON 静态结构序列化兜底（不可达分支防御，返回 nil 由调用方跳过发送）。
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("[notify] 任务终态通知载荷序列化失败（跳过发送）: %v", err)
		return nil
	}
	return b
}
